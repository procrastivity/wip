package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestSyncInitOnboarding(t *testing.T) {
	for _, mode := range []string{"auto", "ask", "disabled", "duplicate", "wrong-org", "no-match", "partial", "missing-scope", "invalid-mode", "first-sync-fails"} {
		t.Run(mode, func(t *testing.T) {
			dir, home := newGitRepo(t, "onboarding"), t.TempDir()
			if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@GitHub.COM:Team/Project.git").CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			var reads, writes atomic.Int32
			var failed atomic.Bool
			failed.Store(mode == "first-sync-fails")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					reads.Add(1)
				} else {
					writes.Add(1)
				}
				switch r.URL.Path {
				case "/api/me":
					_, _ = fmt.Fprint(w, `{"id":11}`)
				case "/api/organizations":
					_, _ = fmt.Fprint(w, `{"data":[{"id":"Org","name":"Team"},{"id":"Other","name":"Other team"}],"next_page_url":null}`)
				case "/api/organizations/Org/repos":
					if r.URL.Query().Get("page") == "1" {
						_, _ = fmt.Fprint(w, `{"data":[{"id":"Fork","name":"Fork","remote_url":"https://github.com/Other/Project.git"}],"next_page_url":"more"}`)
						return
					}
					if mode == "partial" {
						w.WriteHeader(503)
						return
					}
					remote := "https://github.com/Team/Project.git"
					if mode == "no-match" {
						remote = "https://github.com/Team/Project-other.git"
					}
					extra := ""
					if mode == "duplicate" {
						extra = `,{"id":"Duplicate","name":"Duplicate","remote_url":"git@github.com:Team/Project.git"}`
					}
					_, _ = fmt.Fprintf(w, `{"data":[{"id":"Repo","name":"Website","remote_url":%q}%s],"next_page_url":null}`, remote, extra)
				case "/api/organizations/Other/repos":
					_, _ = fmt.Fprint(w, `{"data":[],"next_page_url":null}`)
				case "/api/organizations/Org/repos/Repo":
					_, _ = fmt.Fprint(w, `{"id":"Repo","name":"Website"}`)
				case "/api/organizations/Org/repos/Repo/origins":
					w.WriteHeader(204)
				case "/api/organizations/Org/repos/Repo/changes":
					if failed.Load() {
						w.WriteHeader(503)
						return
					}
					_, _ = fmt.Fprint(w, `{"changes":[],"cursor":0}`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			credentials := syncCredentialFile(home)
			if err := os.MkdirAll(filepath.Dir(credentials), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(credentials, []byte(fmt.Sprintf(`{"Version":1,"Connections":{"office":{"Server":%q,"Account":"11","Token":"dummy-onboarding-token"}}}`, server.URL)), 0o600); err != nil {
				t.Fatal(err)
			}
			enabled := mode != "disabled"
			join := mode
			if join != "ask" && join != "invalid-mode" {
				join = "auto"
			}
			org := "Org"
			if mode == "wrong-org" {
				org = "Other"
			}
			if mode == "missing-scope" {
				org = ""
			}
			config := fmt.Sprintf("sync:\n  connections:\n    office: {server: %q}\n  onboarding: {on_init: %v, default_join: ask}\n  routes:\n    - repo: github.com/Team/**\n      connection: office\n      join: {mode: %s, organization_id: %q}\n", server.URL, enabled, join, org)
			if err := os.WriteFile(filepath.Join(home, "wip", "config.yaml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(home, "local.db"))
			r := runInDir(t, dir, env, "--json", "init")
			wantExit := 0
			if mode == "first-sync-fails" {
				wantExit = 1
			}
			if r.exitCode != wantExit {
				t.Fatal(r)
			}
			var first struct {
				Repo, Clone, Worktree string
				AlreadyInitialized    bool
				Onboarding            *struct {
					State   string
					Binding *store.ExchangeBinding
				}
			}
			if err := json.Unmarshal([]byte(r.stdout), &first); err != nil || first.Repo == "" {
				t.Fatal(r, err)
			}
			if mode == "disabled" {
				if first.Onboarding != nil {
					t.Fatal(r)
				}
			} else if first.Onboarding == nil {
				t.Fatal(r)
			}
			if mode == "auto" && first.Onboarding.State != "synced" {
				t.Fatal(r)
			}
			if mode != "auto" && mode != "first-sync-fails" && writes.Load() != 0 {
				t.Fatal("unexpected remote mutation", r)
			}
			if (mode == "ask" || mode == "disabled" || mode == "invalid-mode" || mode == "missing-scope") && reads.Load() != 0 {
				t.Fatal("unrequested lookup", r)
			}
			if mode == "first-sync-fails" {
				if first.Onboarding.State != "failed" || first.Onboarding.Binding == nil {
					t.Fatal("lost successful pairing", r)
				}
				failed.Store(false)
				if retry := runInDir(t, dir, env, "plumbing", "sync", "run"); retry.exitCode != 0 {
					t.Fatal(retry)
				}
			}
			beforeRead, beforeWrite := reads.Load(), writes.Load()
			r = runInDir(t, dir, env, "--json", "init")
			var second struct {
				Repo, Clone, Worktree string
				AlreadyInitialized    bool
			}
			if err := json.Unmarshal([]byte(r.stdout), &second); r.exitCode != 0 || err != nil || !second.AlreadyInitialized || first.Repo != second.Repo || first.Clone != second.Clone || first.Worktree != second.Worktree {
				t.Fatal(r, err)
			}
			if (mode == "auto" || mode == "first-sync-fails") && (reads.Load() != beforeRead || writes.Load() != beforeWrite) {
				t.Fatal("repeat init resynced retained pairing", r)
			}
			if mode == "auto" {
				if off := runInDir(t, dir, env, "plumbing", "sync", "unpair"); off.exitCode != 0 {
					t.Fatal(off)
				}
				if err := os.WriteFile(filepath.Join(home, "wip", "config.yaml"), []byte("sync:\n  onboarding: {on_init: true}\n  routes: malformed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				beforeRead, beforeWrite = reads.Load(), writes.Load()
				reused := runInDir(t, dir, env, "--json", "init")
				status := runInDir(t, dir, env, "--json", "plumbing", "sync", "status")
				var inactive store.ExchangeStatus
				if reused.exitCode != 0 || json.Unmarshal([]byte(status.stdout), &inactive) != nil || !inactive.Paired || inactive.Active || reads.Load() != beforeRead || writes.Load() != beforeWrite {
					t.Fatal("init reactivated/retargeted inactive association", reused, status)
				}
			}
			if local := runInDir(t, dir, env, "plumbing", "matter", "create", "--title", "Still local — λ"); local.exitCode != 0 {
				t.Fatal(local)
			}
			if strings.Contains(r.stdout+r.stderr, "dummy-onboarding-token") {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestSyncInitPromptedOnboarding(t *testing.T) {
	for _, mode := range []string{"create", "lost-create-response", "registration-fails", "reset-during-create", "reset-restored-request", "join", "search"} {
		t.Run(mode, func(t *testing.T) {
			dir, home := newGitRepo(t, "prompted"), t.TempDir()
			if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://github.com/Team/Project.git").CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			var mu sync.Mutex
			var requestKey, requestPayload string
			var creates, registrations int
			var resetDuringCreate func() error
			var resetRepo string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/api/me":
					_, _ = fmt.Fprint(w, `{"id":11}`)
				case "/api/organizations":
					_, _ = fmt.Fprint(w, `{"data":[{"id":"Org","name":"Chosen team"}],"next_page_url":null}`)
				case "/api/organizations/Org/repos":
					if r.Method == "GET" {
						if mode == "join" || mode == "search" {
							_, _ = fmt.Fprint(w, `{"data":[{"id":"Repo","name":"Trial — λ","remote_url":"github.com/Team/Project"}],"next_page_url":null}`)
							return
						}
						_, _ = fmt.Fprint(w, `{"data":[],"next_page_url":null}`)
						return
					}
					var payload map[string]string
					if json.NewDecoder(r.Body).Decode(&payload) != nil {
						w.WriteHeader(422)
						return
					}
					if payload["name"] != "Trial — λ" || payload["slug"] != "office-trial" || payload["remote_url"] != "github.com/Team/Project" || payload["creation_key"] == "" {
						w.WriteHeader(422)
						return
					}
					raw, _ := json.Marshal(payload)
					if creates == 0 {
						requestKey, requestPayload = payload["creation_key"], string(raw)
					} else if requestKey != payload["creation_key"] || requestPayload != string(raw) {
						w.WriteHeader(409)
						return
					}
					creates++
					if mode == "reset-during-create" || mode == "reset-restored-request" {
						if err := resetDuringCreate(); err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
					}
					if mode == "lost-create-response" && creates == 1 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
					_, _ = fmt.Fprint(w, `{"id":"Repo","name":"Trial — λ"}`)
				case "/api/organizations/Org/repos/Repo":
					_, _ = fmt.Fprint(w, `{"id":"Repo","name":"Trial — λ"}`)
				case "/api/organizations/Org/repos/Repo/origins":
					registrations++
					if mode == "registration-fails" && registrations == 1 {
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(204)
				case "/api/organizations/Org/repos/Repo/changes":
					_, _ = fmt.Fprint(w, `{"changes":[],"cursor":0}`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			credentials := syncCredentialFile(home)
			if err := os.MkdirAll(filepath.Dir(credentials), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(credentials, []byte(fmt.Sprintf(`{"Version":1,"Connections":{"office":{"Server":%q,"Account":"11","Token":"dummy-prompt-token"}}}`, server.URL)), 0o600); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf("sync:\n  connections:\n    office: {server: %q}\n  onboarding: {on_init: true}\n  routes:\n    - {repo: 'github.com/Team/**', connection: office}\n", server.URL)
			if mode == "search" {
				config = "sync:\n  onboarding: {on_init: true}\n"
			}
			if err := os.WriteFile(filepath.Join(home, "wip", "config.yaml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(home, "native.db"))
			mu.Lock()
			resetDuringCreate = func() error {
				saved, err := os.ReadFile(credentials)
				if err != nil {
					return err
				}
				paths, err := filepath.Glob(filepath.Join(filepath.Dir(credentials), "create-*.json"))
				if err != nil {
					return err
				}
				if len(paths) != 1 {
					return fmt.Errorf("expected one pending request: %v", paths)
				}
				request, err := os.ReadFile(paths[0])
				if err != nil {
					return err
				}
				var pending struct{ LocalRepo string }
				if err := json.Unmarshal(request, &pending); err != nil {
					return err
				}
				resetRepo = pending.LocalRepo
				forgotten := runInDir(t, dir, env, "--json", "plumbing", "sync", "connection", "forget", "office", "--yes")
				if forgotten.exitCode != 0 {
					return fmt.Errorf("forget failed during creation: %s %s", forgotten.stdout, forgotten.stderr)
				}
				// Simulate a subsequent login with the exact same origin/account.
				if err := os.WriteFile(credentials, saved, 0o600); err != nil {
					return err
				}
				if mode == "reset-restored-request" {
					// Even resurrecting identical request bytes cannot borrow the
					// generation established after the old POST began.
					return os.WriteFile(paths[0], request, 0o600)
				}
				return nil
			}
			mu.Unlock()
			pty := func(steps [][2]string, wantExit int) string {
				t.Helper()
				driver, err := filepath.Abs("testdata/sync-onboarding.py")
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("python3", driver, binPath, "init")
				cmd.Dir = dir
				cmd.Env = hermeticEnv(t, env)
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
				if err := json.Unmarshal(out, &result); err != nil || result.ExitCode != wantExit || result.Remaining != 0 {
					t.Fatal(err, string(out))
				}
				if strings.Contains(result.Output, "dummy-prompt-token") {
					t.Fatal("token leaked")
				}
				return result.Output
			}
			steps := [][2]string{{"Choice [0]: ", "3"}, {"Choice [0]: ", "1"}, {"Project name [Project]: ", "Trial — λ"}, {"Project slug (lowercase letters, digits, hyphens) []: ", "office-trial"}, {"Choice [0]: ", "1"}}
			wantExit := 1
			if mode == "create" {
				steps = append(steps, [2]string{"Choice [0]: ", "0"})
				wantExit = 0
			}
			if mode == "join" || mode == "search" {
				steps = [][2]string{{"Choice [0]: ", "1"}, {"Choice [0]: ", "1"}, {"Choice [0]: ", "1"}, {"Choice [0]: ", "1"}}
				wantExit = 0
			}
			first := pty(steps, wantExit)
			if !strings.Contains(first, "account 11") {
				t.Fatal(first)
			}
			if mode == "lost-create-response" {
				// Changing routes must not bypass an uncertain creation and
				// offer a new destination through cross-connection search.
				if err := os.WriteFile(filepath.Join(home, "wip", "config.yaml"), []byte("sync:\n  onboarding: {on_init: true}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				pty([][2]string{{"Choice [0]: ", "1"}, {"Choice [0]: ", "0"}}, 0)
			}
			if mode == "registration-fails" {
				pty([][2]string{{"Choice [0]: ", "1"}}, 0)
			}
			status := runInDir(t, dir, env, "--json", "plumbing", "sync", "status")
			var paired store.ExchangeStatus
			if mode == "reset-during-create" || mode == "reset-restored-request" {
				if json.Unmarshal([]byte(status.stdout), &paired) != nil || paired.Paired || paired.Active {
					t.Fatal("old creation response revived a pairing", status)
				}
				mu.Lock()
				count, registered, repo := creates, registrations, resetRepo
				mu.Unlock()
				s, err := store.Open(filepath.Join(home, "native.db"))
				if err != nil {
					t.Fatal(err)
				}
				held, err := s.ExchangeOnboardingHeld(context.Background(), repo)
				_ = s.Close()
				if err != nil || !held || count != 1 || registered != 0 || !strings.Contains(first, "remote Repo may already exist") {
					t.Fatal("creation reset fence/uncertainty diagnostic failed", held, err, count, registered, first)
				}
				return
			}
			if json.Unmarshal([]byte(status.stdout), &paired) != nil || !paired.Active || !strings.HasSuffix(paired.Binding.Endpoint, "/api/organizations/Org/repos/Repo") {
				t.Fatal(status)
			}
			mu.Lock()
			count := creates
			mu.Unlock()
			wantCreates := 1
			if mode == "lost-create-response" {
				wantCreates = 2
			}
			if mode == "join" || mode == "search" {
				wantCreates = 0
				if !strings.Contains(first, "One sync pass completed") {
					t.Fatal("prompted join did not run the requested first sync", first)
				}
			}
			if count != wantCreates {
				t.Fatal("unexpected creation attempts", count)
			}
		})
	}
}
