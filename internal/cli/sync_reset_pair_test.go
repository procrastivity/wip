package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestSyncExplicitPairHold(t *testing.T) {
	for _, mode := range []string{"success", "lookup-failure", "registration-failure", "reset-during-lookup"} {
		t.Run(mode, func(t *testing.T) {
			dir, home := newGitRepo(t, "held-pair"), t.TempDir()
			db := filepath.Join(home, "local.db")
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+db)
			localResult := runInDir(t, dir, env, "--json", "init")
			var local struct{ Repo string }
			if json.Unmarshal([]byte(localResult.stdout), &local) != nil || localResult.exitCode != 0 {
				t.Fatal(localResult)
			}
			s, err := store.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			ctx := context.Background()
			if err := s.HoldExchangeOnboarding(ctx, local.Repo, "earlier-reset"); err != nil {
				t.Fatal(err)
			}
			var failed atomic.Bool
			failed.Store(mode != "success")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/me":
					_, _ = fmt.Fprint(w, `{"id":11}`)
				case "/api/organizations/Org/repos/Repo":
					if failed.Load() && mode == "lookup-failure" {
						w.WriteHeader(503)
						return
					}
					if failed.Load() && mode == "reset-during-lookup" {
						if _, err := s.ForgetExchangeConnection(ctx, "office", "concurrent-reset", true); err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
						if err := s.FinishExchangeConnectionReset(ctx, "office", "concurrent-reset"); err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
					}
					_, _ = fmt.Fprint(w, `{"id":"Repo","name":"Trial"}`)
				case "/api/organizations/Org/repos/Repo/origins":
					if failed.Load() && mode == "registration-failure" {
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(204)
				default:
					t.Error("unexpected endpoint", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			resetWrite(t, syncCredentialFile(home), fmt.Sprintf(`{"Version":1,"Connections":{"office":{"Server":%q,"Account":"11","Token":"dummy-pair-token"}}}`, server.URL))
			args := []string{"--json", "plumbing", "sync", "pair", "--connection", "office", "--org", "Org", "--server-repo", "Repo"}
			paired := runInDir(t, dir, env, args...)
			held, err := s.ExchangeOnboardingHeld(ctx, local.Repo)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				if paired.exitCode != 0 || held {
					t.Fatal(paired, held)
				}
			} else {
				if paired.exitCode == 0 || !held {
					t.Fatal("failed explicit pairing released hold", paired, held)
				}
				if mode == "reset-during-lookup" {
					status, err := s.ExchangeStatus(ctx, local.Repo)
					if err != nil || status.Paired {
						t.Fatal("old initial pairing revived after completed reset", status, err)
					}
				}
				failed.Store(false)
				paired = runInDir(t, dir, env, args...)
				held, err = s.ExchangeOnboardingHeld(ctx, local.Repo)
				if paired.exitCode != 0 || err != nil || held {
					t.Fatal("explicit retry did not clear hold", paired, err, held)
				}
			}
		})
	}
}
