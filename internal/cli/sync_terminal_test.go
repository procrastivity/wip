package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSyncTerminalLogin(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("terminal acceptance requires Python 3's standard-library POSIX PTY support:", err)
	}
	driver, err := filepath.Abs("testdata/sync-terminal.py")
	if err != nil {
		t.Fatal(err)
	}
	const password, token = "dummy-terminal-secret-λ", "dummy-readiness-bearer"
	for _, mode := range []string{"success", "outside-alias", "inside-alias-to-outside", "bad-password", "server-failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := newGitRepo(t, "terminal-login")
			home := t.TempDir()
			physical := home
			if strings.Contains(mode, "alias") {
				home = filepath.Join(t.TempDir(), "alias")
				if mode == "inside-alias-to-outside" {
					home = filepath.Join(dir, "alias")
				}
				if err := os.Symlink(physical, home); err != nil {
					t.Fatal(err)
				}
			}
			var requests, revoked atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "server-failure" {
					w.WriteHeader(503)
					return
				}
				switch r.URL.Path {
				case "/sanctum/csrf-cookie", "/logout":
					w.WriteHeader(204)
				case "/login", "/user/confirm-password":
					var input struct{ Password string }
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.Password != password {
						w.WriteHeader(422)
					}
				case "/tokens":
					_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
				case "/api/me":
					_, _ = w.Write([]byte(`{"id":17}`))
				case "/api/token":
					if r.Method != "DELETE" || r.Header.Get("Authorization") != "Bearer "+token {
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
			args := []string{"--json", "plumbing", "sync", "login", "--server", server.URL, "--email", "terminal@example.test"}
			// A pipe must never silently substitute visible password input.
			headless := runInDir(t, dir, syncConfigEnv(home), args...)
			if headless.exitCode != 1 || !strings.Contains(headless.stderr, "use a terminal") || requests.Load() != 0 {
				t.Fatal("headless password entry was not refused", headless)
			}
			var input any = password
			if mode == "bad-password" {
				input = "incorrect-" + password
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(python, append([]string{driver, binPath}, args...)...)
			cmd.Dir = dir
			cmd.Env = hermeticEnv(t, syncConfigEnv(home))
			cmd.Stdin = bytes.NewReader(raw)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("PTY driver: %v: %s", err, out)
			}
			var terminal struct {
				Output      string
				ExitCode    int  `json:"exit_code"`
				HiddenInput bool `json:"hidden_input"`
				Restored    bool
			}
			if err := json.Unmarshal(out, &terminal); err != nil {
				t.Fatal(err, string(out))
			}
			if !terminal.HiddenInput || !terminal.Restored || strings.Contains(terminal.Output, password) || strings.Contains(terminal.Output, token) {
				t.Fatal("terminal secrecy/restoration failed", terminal)
			}
			credential := syncCredentialFile(physical)
			if mode == "bad-password" || mode == "server-failure" {
				if terminal.ExitCode != 1 {
					t.Fatal("failed input accepted", terminal)
				}
				if _, err := os.Stat(credential); !os.IsNotExist(err) {
					t.Fatal("failed login saved credentials", err)
				}
				return
			}
			if terminal.ExitCode != 0 || !strings.Contains(terminal.Output, `"authenticated":true`) {
				t.Fatal("terminal login failed", terminal)
			}
			data, err := os.ReadFile(credential)
			if err != nil || !strings.Contains(string(data), token) || strings.Contains(string(data), password) {
				t.Fatal("token-only persistence failed", err)
			}
			for path, want := range map[string]os.FileMode{credential: 0o600, filepath.Dir(credential): 0o700} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != want {
					t.Fatal("private credential permissions", path, err)
				}
			}
			// Directory aliases may be safe; a credential-file symlink is not.
			if err := os.Rename(credential, credential+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(credential+".original", credential); err != nil {
				t.Fatal(err)
			}
			before := requests.Load()
			refused := runInDir(t, dir, syncConfigEnv(home), "--json", "plumbing", "sync", "repos")
			if refused.exitCode != 1 || requests.Load() != before || strings.Contains(refused.stdout+refused.stderr, token) {
				t.Fatal("credential-file symlink was not refused before HTTP", refused)
			}
			if err := os.Remove(credential); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(credential+".original", credential); err != nil {
				t.Fatal(err)
			}
			logout := runInDir(t, dir, syncConfigEnv(home), "--json", "plumbing", "sync", "logout")
			if logout.exitCode != 0 || revoked.Load() != 1 || strings.Contains(logout.stdout+logout.stderr, token) {
				t.Fatal("logout did not revoke without disclosing token", logout)
			}
			data, err = os.ReadFile(credential)
			if err != nil || strings.Contains(string(data), token) || !strings.Contains(string(data), `"Account":"17"`) {
				t.Fatal("logout must remove token but retain account pin", err)
			}
		})
	}
}
