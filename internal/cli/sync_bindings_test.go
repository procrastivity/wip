package cli_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestSyncBindingsEmpty(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(fmt.Sprint(initialized), func(t *testing.T) {
			home, dir := t.TempDir(), newGitRepo(t, "unpaired")
			path := filepath.Join(home, "custom", "store.db")
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+path)
			if initialized {
				if r := runInDir(t, dir, env, "init"); r.exitCode != 0 {
					t.Fatal(r)
				}
			}
			for _, cwd := range []string{dir, t.TempDir()} {
				r := runInDir(t, cwd, env, "plumbing", "sync", "bindings", "--json")
				if r.exitCode != 0 || strings.TrimSpace(r.stdout) != `{"bindings":[]}` || r.stderr != "" {
					t.Fatal(r)
				}
			}
			db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'exchange_%' OR name LIKE 'experiment_%'`).Scan(&n); err != nil || n != 0 {
				t.Fatal("inventory initialized sync tables", n, err)
			}
			if _, err := os.Stat(filepath.Dir(syncCredentialFile(home))); !os.IsNotExist(err) {
				t.Fatal("inventory initialized sync config", err)
			}
		})
	}
}

func TestSyncBindingsLegacySchema(t *testing.T) {
	home, dir := t.TempDir(), newGitRepo(t, "legacy-bindings")
	path := filepath.Join(home, "legacy.db")
	env := append(syncConfigEnv(home), "WIP_DB_PATH="+path)
	r := runInDir(t, dir, env, "--json", "init")
	var local struct{ Repo string }
	if r.exitCode != 0 || json.Unmarshal([]byte(r.stdout), &local) != nil {
		t.Fatal(r)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://legacy.example.invalid/api/organizations/1/repos/2"
	if _, err := s.PairExchange(context.Background(), local.Repo, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reproduce a pre-account-metadata store without upgrading it on read.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DROP TABLE exchange_accounts`); err != nil {
		t.Fatal(err)
	}
	r = runInDir(t, dir, env, "plumbing", "sync", "bindings", "--json")
	want := fmt.Sprintf(`{"bindings":[{"local_repo":%q,"active":true,"connection":"","account":"","endpoint":%q}]}`, local.Repo, endpoint)
	if r.exitCode != 0 || strings.TrimSpace(r.stdout) != want {
		t.Fatal(r)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='exchange_accounts'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("inventory created account metadata", n, err)
	}
}

func TestSyncBindingsInventory(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(home, "selected.db")
	env := append(syncConfigEnv(home), "WIP_DB_PATH="+path)
	ctx := context.Background()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	var want []map[string]any
	var active []string
	var inside string
	for _, name := range []string{"work", "personal", "paused", "retired", "unpaired", "legacy"} {
		dir := newGitRepo(t, name)
		inside = dir
		r := runInDir(t, dir, env, "--json", "init")
		var local struct{ Repo string }
		if r.exitCode != 0 || json.Unmarshal([]byte(r.stdout), &local) != nil || local.Repo == "" {
			t.Fatal(r)
		}
		if r := runInDir(t, dir, env, "plumbing", "matter", "create", "--title", "private-work-payload"); r.exitCode != 0 {
			t.Fatal(r)
		}
		if name == "unpaired" {
			continue
		}
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := server.URL + "/api/organizations/" + name + "/repos/destination"
		connection, account := name, "account-"+name
		if name == "legacy" {
			connection, account = "", ""
			_, err = s.PairExchange(ctx, local.Repo, endpoint)
		} else {
			_, err = s.PairExchangeConnection(ctx, local.Repo, endpoint, connection, account)
		}
		if err != nil {
			t.Fatal(err)
		}
		if name == "paused" {
			if err := s.UnpairExchange(ctx, local.Repo); err != nil {
				t.Fatal(err)
			}
		}
		if name == "retired" {
			if _, err := s.ForgetExchangeConnection(ctx, name, "disposable-reset", true); err != nil {
				t.Fatal(err)
			}
		} else {
			want = append(want, map[string]any{"local_repo": local.Repo, "active": name != "paused", "connection": connection, "account": account, "endpoint": endpoint})
			if name != "paused" {
				active = append(active, local.Repo)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i]["local_repo"].(string) < want[j]["local_repo"].(string) })
	sort.Strings(active)
	credentialPath := syncCredentialFile(home)
	credentials := fmt.Sprintf(`{"Version":1,"Connections":{"work":{"Server":%q,"Account":"account-work","Token":"inventory-secret-token"}}}`, server.URL)
	resetWrite(t, credentialPath, credentials)
	before := sha256.Sum256([]byte(resetRead(t, path)))
	for _, cwd := range []string{inside, outside} {
		r := runInDir(t, cwd, env, "plumbing", "sync", "bindings", "--json")
		var got map[string][]map[string]any
		if r.exitCode != 0 || r.stderr != "" || json.Unmarshal([]byte(r.stdout), &got) != nil || len(got) != 1 || !reflect.DeepEqual(got["bindings"], want) {
			t.Fatalf("inventory mismatch: %v; want %v", r, want)
		}
		if jq, err := exec.LookPath("jq"); err == nil {
			cmd := exec.Command(jq, "-r", `.bindings[] | select(.active) | .local_repo`)
			cmd.Stdin = strings.NewReader(r.stdout)
			raw, err := cmd.Output()
			if err != nil || string(raw) != strings.Join(active, "\n")+"\n" {
				t.Fatal("script selected wrong Repos", string(raw), err)
			}
		}
	}
	if _, err := exec.LookPath("jq"); err == nil {
		script, err := filepath.Abs("../../scripts/sync-mapped.sh")
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"--list", "--dry-run"} {
			cmd := exec.Command("bash", script, mode)
			cmd.Dir = inside
			cmd.Env = hermeticEnv(t, append(append([]string{}, env...), "WIP_BIN="+binPath))
			raw, err := cmd.CombinedOutput()
			completed := 0
			if mode == "--dry-run" {
				completed = 3
			}
			if err != nil || !strings.Contains(string(raw), fmt.Sprintf("active=3 skipped=1 completed=%d failed=0", completed)) {
				t.Fatal("script with real CLI", mode, string(raw), err)
			}
		}
	}
	// A broken credential registry cannot poison a metadata-only read.
	resetWrite(t, credentialPath, "invalid inventory-secret-token")
	if r := runInDir(t, outside, env, "plumbing", "sync", "bindings", "--json"); r.exitCode != 0 || strings.Contains(r.stdout+r.stderr, "inventory-secret-token") {
		t.Fatal(r)
	}
	if resetRead(t, credentialPath) != "invalid inventory-secret-token" || requests.Load() != 0 {
		t.Fatal("inventory changed credentials or contacted server", requests.Load())
	}
	if after := sha256.Sum256([]byte(resetRead(t, path))); after != before {
		t.Fatal("inventory mutated existing store")
	}
	// Changing the selected store must not return bindings from the first one.
	other := append(append([]string{}, env...), "WIP_DB_PATH="+filepath.Join(home, "other.db"))
	if r := runInDir(t, inside, other, "plumbing", "sync", "bindings", "--json"); r.exitCode != 0 || strings.TrimSpace(r.stdout) != `{"bindings":[]}` {
		t.Fatal(r)
	}
	for _, flag := range []string{"--repo", "--connection", "--server"} {
		if r := runInDir(t, outside, env, "--json", "plumbing", "sync", "bindings", flag, "unrelated"); r.exitCode == 0 || !strings.Contains(r.stderr, "does not apply") {
			t.Fatal("selection silently changed inventory scope", r)
		}
	}
}
