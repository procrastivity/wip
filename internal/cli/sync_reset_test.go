package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func resetPTY(t *testing.T, dir string, env []string, answer string, args ...string) string {
	t.Helper()
	return resetStepsPTY(t, dir, env, [][2]string{{"Type yes to confirm:", answer}}, args...)
}

func resetStepsPTY(t *testing.T, dir string, env []string, steps [][2]string, args ...string) string {
	t.Helper()
	driver, err := filepath.Abs("testdata/sync-onboarding.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", append([]string{driver, binPath}, args...)...)
	cmd.Dir, cmd.Env = dir, hermeticEnv(t, env)
	raw, _ := json.Marshal(steps)
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	var result struct {
		Output    string
		ExitCode  int `json:"exit_code"`
		Remaining int `json:"remaining_steps"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.ExitCode != 0 || result.Remaining != 0 {
		t.Fatal(err, string(out))
	}
	if strings.Contains(result.Output, "reset-secret-token") {
		t.Fatal("credential leaked", result.Output)
	}
	return result.Output
}

func resetWrite(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resetRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSyncConnectionForget(t *testing.T) {
	for _, mode := range []string{"saved", "missing", "legacy", "retry", "pending-only"} {
		t.Run(mode, func(t *testing.T) {
			dir, home := newGitRepo(t, "reset"), t.TempDir()
			if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:Team/Reset.git").CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			db := filepath.Join(home, "selected.db")
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+db)
			init := runInDir(t, dir, env, "--json", "init")
			var local struct{ Repo string }
			if json.Unmarshal([]byte(init.stdout), &local) != nil || init.exitCode != 0 {
				t.Fatal(init)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(503) }))
			defer server.Close()
			name := "office"
			if mode == "legacy" {
				name = "default"
			}
			s, err := store.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "pending-only" {
				_, err = s.PairExchangeConnection(context.Background(), local.Repo, server.URL+"/api/organizations/Org/repos/Repo", name, "11")
				if err != nil {
					t.Fatal(err)
				}
			}
			_ = s.Close()
			credentialPath := syncCredentialFile(home)
			registry := fmt.Sprintf(`{"Version":1,"Connections":{%q:{"Server":%q,"Account":"11","Token":"reset-secret-token"},"personal":{"Server":%q,"Account":"22","Token":"other-token"}}}`, name, server.URL, server.URL)
			if mode == "legacy" {
				registry = fmt.Sprintf(`{"Server":%q,"Token":"reset-secret-token"}`, server.URL)
			}
			if mode != "missing" {
				resetWrite(t, credentialPath, registry)
			}
			configPath := filepath.Join(home, "wip", "config.yaml")
			config := fmt.Sprintf("unrelated: retained\nsync:\n  connections:\n    %s: {server: %q}\n    personal: {server: %q}\n  onboarding: {on_init: true, default_join: auto}\n  routes:\n    - {repo: '**', connection: %s}\n    - {repo: '**', connection: personal, join: {mode: auto, organization_id: Org}}\n    - {repo: 123, connection: unrelated-malformed}\n", name, server.URL, server.URL, name)
			if mode == "retry" {
				config = "unrelated: retained\nsync:\n  routes: malformed\n"
			}
			resetWrite(t, configPath, config)
			pendingPath := filepath.Join(filepath.Dir(credentialPath), "create-selected.json")
			resetWrite(t, pendingPath, fmt.Sprintf(`{"Version":1,"LocalRepo":%q,"Connection":%q,"Server":%q,"Account":"11","Organization":"Org","Payload":{"name":"Retained trial","slug":"retained-trial","remote_url":"github.com/Team/Reset","creation_key":"request"},"Token":"unknown-field-secret"}`, local.Repo, name, server.URL))
			originalPending := resetRead(t, pendingPath)
			otherPending := filepath.Join(filepath.Dir(credentialPath), "create-other.json")
			resetWrite(t, otherPending, `{"Version":1,"LocalRepo":"other","Connection":"personal","Payload":{"creation_key":"other"}}`)
			unknownPath := filepath.Join(filepath.Dir(credentialPath), "create-unknown.json")
			resetWrite(t, unknownPath, `{"Token":"unparsed-secret",invalid`)
			unsafePath := filepath.Join(filepath.Dir(credentialPath), "create-unsafe.json")
			resetWrite(t, unsafePath, `{"Token":"unsafe-secret"}`)
			if err := os.Chmod(unsafePath, 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"--json", "plumbing", "sync", "connection", "forget", name}
			preview := runInDir(t, dir, env, append(args, "--dry-run")...)
			if preview.exitCode != 0 || !strings.Contains(preview.stdout, db) || !strings.Contains(preview.stdout, configPath) || strings.Contains(preview.stdout, "reset-secret-token") {
				t.Fatal(preview)
			}
			var planned struct {
				Saved struct {
					Present         bool
					Server, Account string
				} `json:"saved_connection"`
				Pending map[string]struct {
					LocalRepo, Connection, Server, Account, Organization string
					Payload                                              struct {
						Name, Slug string
						Remote     string `json:"remote_url"`
						Key        string `json:"creation_key"`
					}
				} `json:"pending_creations"`
				Configuration struct {
					Definition   *struct{ Server string }
					Routes       []struct{ Repo, Connection string }
					CleanupIssue string
				}
				Issues []struct{ Path, Classification string } `json:"creation_record_issues"`
			}
			if err := json.Unmarshal([]byte(preview.stdout), &planned); err != nil {
				t.Fatal(err)
			}
			if planned.Saved.Present != (mode != "missing") || (mode != "missing" && planned.Saved.Server != server.URL) || (mode != "missing" && mode != "legacy" && planned.Saved.Account != "11") {
				t.Fatal(preview)
			}
			request, ok := planned.Pending[pendingPath]
			if !ok || request.LocalRepo != local.Repo || request.Server != server.URL || request.Organization != "Org" || request.Payload.Name != "Retained trial" || request.Payload.Slug != "retained-trial" || request.Payload.Remote != "github.com/Team/Reset" || request.Payload.Key != "request" {
				t.Fatal(preview)
			}
			if len(planned.Issues) != 2 || planned.Configuration.CleanupIssue == "" {
				t.Fatal("missing nonblocking diagnostics", preview)
			}
			if mode != "retry" && (planned.Configuration.Definition == nil || planned.Configuration.Definition.Server != server.URL || len(planned.Configuration.Routes) != 1 || planned.Configuration.Routes[0].Connection != name) {
				t.Fatal("selected route/definition absent", preview)
			}
			for _, secret := range []string{"unknown-field-secret", "unparsed-secret", "unsafe-secret", "other-token"} {
				if strings.Contains(preview.stdout, secret) {
					t.Fatal("preview leaked unknown fields", preview)
				}
			}
			refused := runInDir(t, dir, env, args...)
			if refused.exitCode == 0 || !strings.Contains(refused.stdout+refused.stderr, "--yes") {
				t.Fatal(refused)
			}
			if mode == "saved" {
				resetPTY(t, dir, env, "no", "plumbing", "sync", "connection", "forget", name)
				if resetRead(t, credentialPath) != registry {
					t.Fatal("decline mutated credentials")
				}
				resetPTY(t, dir, env, "yes", "plumbing", "sync", "connection", "forget", name)
			} else {
				applied := runInDir(t, dir, env, append(args, "--yes")...)
				if mode == "retry" {
					if applied.exitCode == 0 {
						t.Fatal("malformed routing cleanup unexpectedly succeeded", applied)
					}
					journalPath := filepath.Join(filepath.Dir(credentialPath), "forget-office.json")
					journal := resetRead(t, journalPath)
					if strings.Contains(journal, "reset-secret-token") || strings.Contains(journal, "other-token") {
						t.Fatal("journal leaked tokens")
					}
					info, _ := os.Stat(journalPath)
					if info.Mode().Perm() != 0o600 {
						t.Fatal(info.Mode())
					}
					blocked := runInDir(t, dir, env, "--json", "plumbing", "sync", "repos", "--connection", name, "--server", server.URL)
					if blocked.exitCode == 0 || !strings.Contains(blocked.stdout+blocked.stderr, "pending offline cleanup") {
						t.Fatal(blocked)
					}
					// A different selected DB must not complete the user-global reset.
					wrong := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(home, "other.db"))
					if r := runInDir(t, dir, wrong, append(args, "--yes")...); r.exitCode == 0 {
						t.Fatal(r)
					}
					// Store safety already transitioned despite cleanup failure.
					status := runInDir(t, dir, env, "--json", "plumbing", "sync", "status")
					var current store.ExchangeStatus
					if json.Unmarshal([]byte(status.stdout), &current) != nil || current.Paired {
						t.Fatal(status)
					}
					resetWrite(t, pendingPath, strings.Replace(originalPending, `"creation_key":"request"`, `"creation_key":"replacement"`, 1))
					if r := runInDir(t, dir, env, append(args, "--yes")...); r.exitCode == 0 || !strings.Contains(r.stdout+r.stderr, "pending creation replaced concurrently") {
						t.Fatal(r)
					}
					if !strings.Contains(resetRead(t, pendingPath), `"creation_key":"replacement"`) {
						t.Fatal("selected replacement request was changed")
					}
					resetWrite(t, pendingPath, originalPending)
					resetWrite(t, configPath, "unrelated: retained\nsync: {routes: []}\n")
					resetWrite(t, credentialPath, fmt.Sprintf(`{"Version":1,"Connections":{"office":{"Server":%q,"Account":"11","Token":"replacement-token"},"personal":{"Server":%q,"Account":"22","Token":"other-token"}}}`, server.URL, server.URL))
					if r := runInDir(t, dir, env, append(args, "--yes")...); r.exitCode == 0 || !strings.Contains(r.stdout+r.stderr, "replaced concurrently") {
						t.Fatal(r)
					}
					if !strings.Contains(resetRead(t, credentialPath), "replacement-token") {
						t.Fatal("replacement was erased")
					}
					resetWrite(t, credentialPath, fmt.Sprintf(`{"Version":1,"Connections":{"personal":{"Server":%q,"Account":"22","Token":"other-token"}}}`, server.URL))
					var retained struct{ ID string }
					if err := json.Unmarshal([]byte(journal), &retained); err != nil {
						t.Fatal(err)
					}
					archivePath := filepath.Join(filepath.Dir(credentialPath), fmt.Sprintf("retired-create-%s-%x.json", retained.ID, sha256.Sum256([]byte(local.Repo))))
					resetWrite(t, archivePath, `{"different":"evidence"}`)
					if r := runInDir(t, dir, env, append(args, "--yes")...); r.exitCode == 0 || !strings.Contains(r.stdout+r.stderr, "different retired creation evidence") {
						t.Fatal(r)
					}
					if resetRead(t, archivePath) != `{"different":"evidence"}` {
						t.Fatal("archive was overwritten")
					}
					if _, err := os.Stat(pendingPath); err != nil {
						t.Fatal("active request deleted before diagnostic durability", err)
					}
					if err := os.Remove(archivePath); err != nil {
						t.Fatal(err)
					}
					applied = runInDir(t, dir, env, append(args, "--yes")...)
				}
				if applied.exitCode != 0 || strings.Contains(applied.stdout+applied.stderr, "reset-secret-token") {
					t.Fatal(applied)
				}
			}
			if requests.Load() != 0 {
				t.Fatal("offline forget used HTTP", requests.Load())
			}
			if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
				t.Fatal("selected pending creation retained", err)
			}
			archives, err := filepath.Glob(filepath.Join(filepath.Dir(credentialPath), "retired-create-*.json"))
			if err != nil || len(archives) != 1 {
				t.Fatal("missing retired creation evidence", archives, err)
			}
			archive := resetRead(t, archives[0])
			var evidence struct {
				Version int
				ResetID string
				Request struct {
					LocalRepo, Connection, Server, Account, Organization string
					Payload                                              struct {
						Name, Slug string
						Key        string `json:"creation_key"`
					}
				}
			}
			if json.Unmarshal([]byte(archive), &evidence) != nil || evidence.Version != 1 || evidence.ResetID == "" || evidence.Request.LocalRepo != local.Repo || evidence.Request.Connection != name || evidence.Request.Server != server.URL || evidence.Request.Account != "11" || evidence.Request.Organization != "Org" || evidence.Request.Payload.Key != "request" || evidence.Request.Payload.Name != "Retained trial" {
				t.Fatal(archive)
			}
			info, err := os.Stat(archives[0])
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("archive is not private", err)
			}
			if strings.Contains(archive, "secret") || strings.Contains(archive, "other-token") {
				t.Fatal("archived unknown secret fields")
			}
			if resetRead(t, unknownPath) != `{"Token":"unparsed-secret",invalid` || resetRead(t, unsafePath) != `{"Token":"unsafe-secret"}` {
				t.Fatal("unknown/unsafe request changed")
			}
			if !strings.Contains(resetRead(t, otherPending), "personal") {
				t.Fatal("unrelated pending request changed")
			}
			if mode != "missing" && mode != "legacy" && !strings.Contains(resetRead(t, credentialPath), "other-token") {
				t.Fatal("other credentials removed")
			}
			if !strings.Contains(resetRead(t, configPath), "unrelated: retained") {
				t.Fatal("unrelated config removed")
			}
			if mode != "retry" {
				// Catch-all routing to the other account must not reconnect a held Repo.
				// Remove the intentionally malformed unrelated rule first.
				resetWrite(t, configPath, fmt.Sprintf("sync:\n  connections:\n    personal: {server: %q}\n  onboarding: {on_init: true, default_join: auto}\n  routes:\n    - {repo: '**', connection: personal, join: {mode: auto, organization_id: Org}}\n", server.URL))
				r := runInDir(t, dir, env, "--json", "init")
				if r.exitCode != 0 || !strings.Contains(r.stdout, "Kept local after connection forget") {
					t.Fatal(r)
				}
				if requests.Load() != 0 {
					t.Fatal("hold allowed catch-all HTTP")
				}
				if mode == "saved" || mode == "pending-only" {
					resetStepsPTY(t, dir, env, [][2]string{{"Choice [0]:", "0"}}, "init")
					if requests.Load() != 0 {
						t.Fatal("Keep local used HTTP")
					}
					resetStepsPTY(t, dir, env, [][2]string{{"Choice [0]:", "1"}}, "init")
					s, err := store.Open(db)
					if err != nil {
						t.Fatal(err)
					}
					held, err := s.ExchangeOnboardingHeld(context.Background(), local.Repo)
					_ = s.Close()
					if err != nil || held || requests.Load() == 0 {
						t.Fatal("Reconnect did not explicitly release hold and attempt onboarding", held, err, requests.Load())
					}
				}
			}
		})
	}
}

func TestSyncRepoDelete(t *testing.T) {
	for _, mode := range []string{"success", "decline", "lost", "lost-absent", "404", "403", "401", "delete404", "delete403", "500", "non204", "wrong-account", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			dir, home := newGitRepo(t, "remote-delete"), t.TempDir()
			credentials := syncCredentialFile(home)
			var deletes, identities atomic.Int32
			var lose atomic.Bool
			lose.Store(mode == "lost" || mode == "lost-absent")
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer reset-secret-token" {
					t.Error("unexpected auth")
				}
				switch r.URL.Path {
				case "/api/me":
					identities.Add(1)
					if mode == "401" {
						w.WriteHeader(401)
						return
					}
					if mode == "wrong-account" {
						_, _ = fmt.Fprint(w, `{"id":22}`)
					} else {
						_, _ = fmt.Fprint(w, `{"id":11}`)
					}
				case "/api/organizations/Org/repos/Repo/deletion":
					if mode == "404" || (mode == "lost-absent" && deletes.Load() != 0) {
						w.WriteHeader(404)
						return
					}
					if mode == "403" {
						w.WriteHeader(403)
						return
					}
					if mode == "replacement" {
						resetWrite(t, credentials, fmt.Sprintf(`{"Version":1,"Connections":{"office":{"Server":%q,"Account":"11","Token":"replacement"}}}`, server.URL))
					}
					_, _ = fmt.Fprint(w, `{"repo":{"id":"Repo","name":"Trial"},"changes":7,"origins":2}`)
				case "/api/organizations/Org/repos/Repo":
					if r.Method != "DELETE" {
						t.Error("not DELETE")
						w.WriteHeader(405)
						return
					}
					deletes.Add(1)
					if mode == "delete404" {
						w.WriteHeader(404)
						return
					}
					if mode == "delete403" {
						w.WriteHeader(403)
						return
					}
					if mode == "500" {
						w.WriteHeader(500)
						return
					}
					if mode == "non204" {
						w.WriteHeader(200)
						return
					}
					if lose.Swap(false) {
						conn, _, _ := w.(http.Hijacker).Hijack()
						_ = conn.Close()
						return
					}
					w.WriteHeader(204)
				default:
					t.Error("unexpected endpoint", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			registry := fmt.Sprintf(`{"Version":1,"Connections":{"office":{"Server":%q,"Account":"11","Token":"reset-secret-token"}}}`, server.URL)
			resetWrite(t, credentials, registry)
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(home, "local.db"))
			args := []string{"plumbing", "sync", "repo", "delete", "--connection", "office", "--org", "Org", "--server-repo", "Repo"}
			if mode == "success" || mode == "decline" {
				preview := runInDir(t, dir, env, append([]string{"--json"}, append(args, "--dry-run")...)...)
				if preview.exitCode != 0 || !strings.Contains(preview.stdout, `"changes":7`) || deletes.Load() != 0 {
					t.Fatal(preview)
				}
				refused := runInDir(t, dir, env, args...)
				if refused.exitCode == 0 || deletes.Load() != 0 {
					t.Fatal(refused)
				}
				answer := "yes"
				if mode == "decline" {
					answer = "no"
				}
				resetPTY(t, dir, env, answer, args...)
				want := int32(1)
				if mode == "decline" {
					want = 0
				}
				if deletes.Load() != want {
					t.Fatal(deletes.Load())
				}
			} else {
				r := runInDir(t, dir, env, append([]string{"--json"}, append(args, "--yes")...)...)
				if r.exitCode == 0 {
					t.Fatal(r)
				}
				if strings.Contains(r.stdout+r.stderr, "reset-secret-token") {
					t.Fatal("token leaked", r)
				}
				if mode == "lost" || mode == "lost-absent" {
					if !strings.Contains(r.stdout+r.stderr, "remote_outcome_unknown") || !strings.Contains(r.stdout+r.stderr, server.URL+"/api/organizations/Org/repos/Repo") {
						t.Fatal(r)
					}
					r = runInDir(t, dir, env, append([]string{"--json"}, append(args, "--yes")...)...)
					if mode == "lost-absent" {
						if r.exitCode == 0 || !strings.Contains(r.stdout+r.stderr, "absent OR inaccessible") || deletes.Load() != 1 {
							t.Fatal(r)
						}
					} else if r.exitCode != 0 || !strings.Contains(r.stdout, `"deleted":true`) || deletes.Load() != 2 {
						t.Fatal(r)
					}
				} else if mode == "delete404" || mode == "delete403" {
					if !strings.Contains(r.stdout+r.stderr, "deletion not confirmed") || deletes.Load() != 1 {
						t.Fatal(r)
					}
				} else if mode == "500" || mode == "non204" {
					if !strings.Contains(r.stdout+r.stderr, "remote_outcome_unknown") || deletes.Load() != 1 {
						t.Fatal(r)
					}
				} else if deletes.Load() != 0 {
					t.Fatal("DELETE despite failed revalidation")
				}
			}
			if mode != "replacement" && resetRead(t, credentials) != registry {
				t.Fatal("remote delete changed local credentials")
			}
		})
	}
}

// Hash calculation is deliberately independent of reset implementation; a
// missing saved entry is tested above rather than represented by an empty token.
func TestSyncForgetPendingJournalBlocksLogin(t *testing.T) {
	dir, home := newGitRepo(t, "pending-reset"), t.TempDir()
	credential := syncCredentialFile(home)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(`{"Server":"","Account":"","Token":""}`)))
	resetWrite(t, filepath.Join(filepath.Dir(credential), "forget-office.json"), fmt.Sprintf(`{"Version":1,"ID":"pending","Connection":"office","CredentialPresent":false,"CredentialHash":%q}`, hash))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(home, "local.db"))
	r := runInDir(t, dir, env, "--json", "plumbing", "sync", "login", "--connection", "office", "--server", server.URL, "--email", "dummy@example.test")
	if r.exitCode == 0 || !strings.Contains(r.stdout+r.stderr, "pending offline cleanup") || requests.Load() != 0 {
		t.Fatal(r)
	}
	if r := runInDir(t, dir, env, "--json", "init"); r.exitCode != 0 {
		t.Fatal("pending reset blocked local init", r)
	}
}

func TestSyncForgetCredentialOnlyPreview(t *testing.T) {
	dir, home := newGitRepo(t, "credential-preview"), t.TempDir()
	path := syncCredentialFile(home)
	registry := `{"Version":1,"Connections":{"office":{"Server":"https://offline.example.invalid","Account":"99","Token":"reset-secret-token"}}}`
	resetWrite(t, path, registry)
	env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(home, "local.db"))
	r := runInDir(t, dir, env, "--json", "plumbing", "sync", "connection", "forget", "office", "--dry-run")
	var preview struct {
		Saved struct {
			Present         bool
			Server, Account string
		} `json:"saved_connection"`
		Pending map[string]json.RawMessage `json:"pending_creations"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &preview); err != nil || r.exitCode != 0 || !preview.Saved.Present || preview.Saved.Server != "https://offline.example.invalid" || preview.Saved.Account != "99" || len(preview.Pending) != 0 || strings.Contains(r.stdout, "reset-secret-token") {
		t.Fatal(r, err)
	}
	if resetRead(t, path) != registry {
		t.Fatal("preview mutated credentials")
	}
	if _, err := os.Stat(filepath.Join(home, "local.db")); !os.IsNotExist(err) {
		t.Fatal("credential-only preview created a store", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "forget-office.json")); !os.IsNotExist(err) {
		t.Fatal("preview created journal", err)
	}
}
