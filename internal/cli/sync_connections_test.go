package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSyncRemoteRouting(t *testing.T) {
	const definitions = "sync:\n  connections:\n    personal: {server: 'https://personal.example.invalid'}\n    work: {server: 'https://work.example.invalid'}\n  routes:\n"
	const rules = "    - {repo: 'github.com/**', connection: personal}\n    - {repo: 'github.com/Employer/**', connection: work}\n    - {repo: 'github.com/Employer/Special', connection: personal}\n    - {repo: '**', connection: work}\n"
	for _, tc := range []struct {
		remote, rule, extra string
		fail                bool
	}{
		{"git@GitHub.COM:Me/Repo.git", "github.com/**", "", false},
		{"https://GitHub.COM:443/Me/Repo.git", "github.com/**", "", false},
		{"git@github.com:Employer/Repo.git", "github.com/Employer/**", "", false},
		{"git@github.com:Employer/Special.git", "github.com/Employer/Special", "", false},
		{"git@github.com:Employer-other/Repo.git", "github.com/**", "", false},
		{"git@github.com:employer/Repo.git", "github.com/**", "", false},
		{"https://github.com.evil.example/Me/Repo", "**", "", false},
		{"ssh://git@github.com:2222/Me/Repo", "**", "", false},
		{"ssh://git@github.com:2222/Me/Repo", "github.com:2222/**", "    - {repo: 'github.com:2222/**', connection: personal}\n", false},
		{"git@github.com:Me/Repo", "", "    - {repo: 'github.com/**', connection: work}\n", true},
		{"", "", "", true},
		{"git@github.com:Me/Repo", "", "    - {repo: 'github.com/*/bad', connection: work}\n", true},
		{"git@github.com:Me/Repo", "", "    - {repo: 'other.invalid/**', connection: missing}\n", true},
	} {
		name := tc.remote + tc.extra
		if name == "" {
			name = "remote-less-unadopted"
		}
		t.Run(name, func(t *testing.T) {
			dir := newGitRepo(t, "route")
			home := t.TempDir()
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(t.TempDir(), "local.db"))
			if tc.remote != "" {
				if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", tc.remote).CombinedOutput(); err != nil {
					t.Fatal(err, string(out))
				}
			}
			if r := runInDir(t, dir, env, "init"); r.exitCode != 0 {
				t.Fatal(r)
			}
			if tc.remote == "" {
				// Merely adding a remote cannot make route adopt its identity.
				if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:Me/Later.git").CombinedOutput(); err != nil {
					t.Fatal(err, string(out))
				}
			}
			before, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "wip", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(definitions+rules+tc.extra), 0o600); err != nil {
				t.Fatal(err)
			}
			r := runInDir(t, dir, env, "--json", "plumbing", "sync", "route")
			if (r.exitCode != 0) != tc.fail {
				t.Fatal(r)
			}
			if !tc.fail {
				var out struct {
					Selection struct {
						Rule, Connection  string
						CredentialPresent bool
					}
				}
				if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || out.Selection.Rule != tc.rule || out.Selection.CredentialPresent {
					t.Fatal(r, err)
				}
			}
			after, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if string(before) != string(after) {
				t.Fatal("route changed Git configuration")
			}
			if _, err := os.Stat(syncCredentialFile(home)); !os.IsNotExist(err) {
				t.Fatal("route wrote credential state", err)
			}
			if err := os.WriteFile(path, []byte(definitions+"    - {repo: 'unmatched.invalid/**', connection: work}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if r := runInDir(t, dir, env, "plumbing", "sync", "route"); r.exitCode != 1 || !strings.Contains(r.stderr, "no route") {
				t.Fatal("unmatched route guessed a destination", r)
			}
			// Bad routes cannot poison ordinary local work or explicit selection.
			if err := os.WriteFile(path, []byte(definitions+"    malformed: route\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if r := runInDir(t, dir, env, "plumbing", "matter", "create", "--title", "Still local"); r.exitCode != 0 {
				t.Fatal(r)
			}
			if r := runInDir(t, dir, env, "plumbing", "sync", "pair", "--connection", "work", "--org", "explicit", "--server-repo", "explicit", "--dry-run"); r.exitCode != 0 {
				t.Fatal(r)
			}
			if r := runInDir(t, dir, env, "plumbing", "sync", "pair", "--server", "https://unconfigured.example.invalid", "--org", "explicit", "--server-repo", "explicit", "--dry-run"); r.exitCode != 0 {
				t.Fatal(r)
			}
		})
	}
}

func TestSyncNamedCredentialConcurrency(t *testing.T) {
	dir, home := newGitRepo(t, "named-auth"), t.TempDir()
	env := append(syncConfigEnv(home), "WIP_DB_PATH="+filepath.Join(t.TempDir(), "native.db"))
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("dummy-named-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	var tokens sync.Map
	var serial, issued, revoked atomic.Int32
	barrier := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sanctum/csrf-cookie", "/user/confirm-password", "/logout":
			w.WriteHeader(204)
		case "/login":
			var input struct{ Email string }
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(422)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "account", Value: input.Email})
		case "/tokens":
			cookie, err := r.Cookie("account")
			if err != nil {
				w.WriteHeader(401)
				return
			}
			id := "11"
			if cookie.Value == "two@example.test" {
				id = "22"
			}
			token := fmt.Sprintf("dummy-token-%d", serial.Add(1))
			tokens.Store(token, id)
			if issued.Add(1) == 2 {
				close(barrier)
			}
			<-barrier
			_, _ = fmt.Fprintf(w, `{"token":%q}`, token)
		case "/api/me":
			id, ok := tokens.Load(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if !ok {
				w.WriteHeader(401)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%s}`, id)
		case "/api/token":
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if _, ok := tokens.LoadAndDelete(token); !ok {
				w.WriteHeader(401)
				return
			}
			revoked.Add(1)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	login := func(name, email string) result {
		return runInDir(t, dir, env, "--json", "plumbing", "sync", "login", "--connection", name, "--server", server.URL, "--email", email, "--password-file", password)
	}
	results := make(chan result, 2)
	for name, email := range map[string]string{"personal": "one@example.test", "work": "two@example.test"} {
		go func() { results <- login(name, email) }()
	}
	for range 2 {
		if r := <-results; r.exitCode != 0 {
			t.Fatal(r)
		}
	}
	read := func() map[string]struct{ Server, Account, Token string } {
		raw, err := os.ReadFile(syncCredentialFile(home))
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Version     int
			Connections map[string]struct{ Server, Account, Token string }
		}
		if json.Unmarshal(raw, &r) != nil || r.Version != 1 {
			t.Fatal("invalid registry")
		}
		return r.Connections
	}
	initial := read()
	if len(initial) != 2 || initial["personal"].Account != "11" || initial["work"].Account != "22" || initial["personal"].Token == initial["work"].Token {
		t.Fatal("lost entry or account conflation")
	}
	if r := runInDir(t, dir, env, "--json", "plumbing", "sync", "connections"); r.exitCode != 0 || strings.Contains(r.stdout, "dummy-token-") {
		t.Fatal(r)
	}
	if r := runInDir(t, dir, env, "plumbing", "sync", "logout", "--connection", "work"); r.exitCode != 0 {
		t.Fatal(r)
	}
	if after := read(); after["work"].Token != "" || after["work"].Account != "22" || !reflect.DeepEqual(after["personal"], initial["personal"]) {
		t.Fatal("logout crossed connection boundary")
	}
	if r := login("work", "one@example.test"); r.exitCode != 1 || !strings.Contains(r.stderr, "account is immutable") {
		t.Fatal(r)
	}
	if revoked.Load() != 2 || read()["work"].Token != "" {
		t.Fatal("failed identity replacement did not revoke its newly issued token")
	}
	if r := login("work", "two@example.test"); r.exitCode != 0 {
		t.Fatal(r)
	}
	// A concurrent login cannot overwrite a newer winner for the same name.
	if r := runInDir(t, dir, env, "plumbing", "sync", "logout", "--connection", "work"); r.exitCode != 0 {
		t.Fatal(r)
	}
	for range 2 {
		go func() { results <- login("work", "two@example.test") }()
	}
	winners := 0
	for range 2 {
		r := <-results
		if r.exitCode == 0 {
			winners++
		}
	}
	if winners != 1 || read()["work"].Token == "" || !reflect.DeepEqual(read()["personal"], initial["personal"]) {
		t.Fatal("concurrent same-name login did not preserve one winner and other entries", winners)
	}
}
