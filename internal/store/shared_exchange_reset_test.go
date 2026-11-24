//go:build sharedstoreexperiment

package store_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestExchangeForgetLateResponse(t *testing.T) {
	for _, phase := range []string{"upload", "download"} {
		t.Run(phase, func(t *testing.T) {
			p, s := exchangeHost(t)
			received, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			var incoming []store.ExchangeChange
			if phase == "download" {
				foreign, sf := exchangeHost(t)
				id := followCreate(t, foreign, sf, "late-foreign-matter")
				const origin = "11111111-1111-4111-8111-111111111111"
				source, err := sf.ExperimentExport(labCtx, origin, id)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
				incoming = []store.ExchangeChange{{Version: 1, Sequence: 1, InstallationID: origin, GroupID: source.Actions[0].Event.ID, SourceRepoID: foreign.repo, Source: string(raw)}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (phase == "upload" && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/changes")) || (phase == "download" && r.Method == "GET") {
					close(received)
					<-release
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"changes": incoming, "cursor": len(incoming)})
			}))
			defer func() { released.Do(func() { close(release) }); server.Close() }()
			endpoint := server.URL + "/api/organizations/org/repos/old"
			old, err := s.PairExchangeConnection(labCtx, p.repo, endpoint, "office", "1")
			if err != nil {
				t.Fatal(err)
			}
			if phase == "upload" {
				followCreate(t, p, s, "before-reset")
			}
			before := labEvents(t, s)
			done := make(chan error, 1)
			go func() {
				_, err := s.RunExchange(labCtx, store.ExchangeClient{HTTP: http.DefaultClient}, p.repo)
				done <- err
			}()
			<-received
			preview, err := s.ForgetExchangeConnection(labCtx, "office", "", false)
			if err != nil || len(preview) != 1 {
				t.Fatal(preview, err)
			}
			if _, err := s.ForgetExchangeConnection(labCtx, "office", "reset-1", true); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PairExchangeConnection(labCtx, p.repo, endpoint, "office", "2"); err == nil {
				t.Fatal("unfinished reset did not fence pairing")
			}
			if err := s.FinishExchangeConnectionReset(labCtx, "office", "reset-1"); err != nil {
				t.Fatal(err)
			}
			fresh, err := s.PairExchangeConnection(labCtx, p.repo, endpoint, "office", "2")
			if err != nil || fresh.Binding.InstallationID == old.Binding.InstallationID || fresh.Generation == old.Generation || len(fresh.PendingUpload) != 0 {
				t.Fatal("fresh association borrowed old state", old, fresh, err)
			}
			transport := syncRowCounts(t, p.db)
			released.Do(func() { close(release) })
			if err := <-done; err == nil {
				t.Fatal("retired pass accepted a late response")
			}
			after, err := s.ExchangeStatus(labCtx, p.repo)
			if err != nil || !reflect.DeepEqual(fresh, after) || !reflect.DeepEqual(before, labEvents(t, s)) {
				t.Fatal("late response changed fresh association or native work", after, err)
			}
			if !reflect.DeepEqual(transport, syncRowCounts(t, p.db)) {
				t.Fatal("late response left a receipt, inbox or source in the new association")
			}
			if _, err := s.ForgetExchangeConnection(labCtx, "office", "reset-1", true); err != nil {
				t.Fatal(err)
			}
			after, _ = s.ExchangeStatus(labCtx, p.repo)
			if !reflect.DeepEqual(fresh, after) {
				t.Fatal("reset replay retired a new pairing")
			}
		})
	}
}

func TestExchangeForgetLegacyAndInitialPair(t *testing.T) {
	p, s := exchangeHost(t)
	endpoint := "http://127.0.0.1:1/api/organizations/org/repos/repo"
	legacy := exchangeBind(t, s, p, endpoint)
	// Reproduce the published pre-reset layout, retaining its binding and
	// store-wide installation but none of the new additive reset metadata.
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE exchange_associations; DROP TABLE exchange_retired; DROP TABLE exchange_reset_holds; DROP TABLE exchange_resets`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetExchangeConnection(labCtx, "default", "legacy-reset", true); err != nil {
		t.Fatal("low-level legacy binding cannot be forgotten", err)
	}
	if err := s.FinishExchangeConnectionReset(labCtx, "default", "legacy-reset"); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.PairExchangeConnection(labCtx, p.repo, endpoint, "default", "2")
	if err != nil || fresh.Binding.InstallationID == legacy.InstallationID {
		t.Fatal("legacy binding did not get a fresh origin", fresh, err)
	}
	before, err := s.ExchangeConnectionGeneration(labCtx, "credentials-only")
	if err != nil || before != "" {
		t.Fatal(before, err)
	}
	if _, err := s.ForgetExchangeConnection(labCtx, "credentials-only", "reset-empty", true); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishExchangeConnectionReset(labCtx, "credentials-only", "reset-empty"); err != nil {
		t.Fatal(err)
	}
	q, sq := exchangeHost(t)
	// A never-paired project's pending creation also leaves an onboarding hold.
	if _, err := sq.ForgetExchangeConnection(labCtx, "office", "abandoned-request", true); err != nil {
		t.Fatal(err)
	}
	if err := sq.HoldExchangeOnboarding(labCtx, q.repo, "abandoned-request"); err != nil {
		t.Fatal(err)
	}
	held, err := sq.ExchangeOnboardingHeld(labCtx, q.repo)
	if err != nil || !held {
		t.Fatal(held, err)
	}
	if err := sq.ReleaseExchangeHold(labCtx, q.repo, "reset:abandoned-request"); err != nil {
		t.Fatal(err)
	}
	if err := sq.HoldExchangeOnboarding(labCtx, q.repo, "abandoned-request"); err != nil {
		t.Fatal(err)
	}
	if held, err := sq.ExchangeOnboardingHeld(labCtx, q.repo); err != nil || held {
		t.Fatal("retry reinstated a released hold", held, err)
	}
	if err := sq.FinishExchangeConnectionReset(labCtx, "office", "abandoned-request"); err != nil {
		t.Fatal(err)
	}
	if _, err := sq.ForgetExchangeConnection(labCtx, "office", "new-reset", true); err != nil {
		t.Fatal(err)
	}
	if err := sq.HoldExchangeOnboarding(labCtx, q.repo, "new-reset"); err != nil {
		t.Fatal(err)
	}
	if err := sq.HoldExchangeOnboarding(labCtx, q.repo, "abandoned-request"); err == nil {
		t.Fatal("older reset replaced newer hold")
	}
	if err := sq.ReleaseExchangeHold(labCtx, q.repo, "reset:abandoned-request"); err == nil {
		t.Fatal("old reconnect released a newer hold")
	}
	if held, err := sq.ExchangeOnboardingHeld(labCtx, q.repo); err != nil || !held {
		t.Fatal("newer hold was lost", held, err)
	}
	if _, err := s.PairExchangeConnectionAt(labCtx, p.repo, endpoint, "credentials-only", "3", fresh.Generation, before); err == nil || !strings.Contains(err.Error(), "forgotten during pairing") {
		t.Fatal("completed credential-only reset did not fence initial pairing", err)
	}
	unchanged, err := s.ExchangeStatus(labCtx, p.repo)
	if err != nil || !reflect.DeepEqual(fresh, unchanged) {
		t.Fatal("other connection reset changed existing binding", unchanged, err)
	}
}

func TestLaravelConnectionResetCLI(t *testing.T) {
	f := startLaravelFixture(t)
	target, err := url.Parse(f.base)
	if err != nil {
		t.Fatal(err)
	}
	var lose atomic.Bool
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Request.Method == "DELETE" && resp.StatusCode == 204 && lose.Swap(false) {
			_ = resp.Body.Close()
			return fmt.Errorf("lost committed deletion response")
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	server := httptest.NewServer(proxy)
	defer server.Close()
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("fixture-password-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca, cb := syncCLI{a, t.TempDir()}, syncCLI{b, t.TempDir()}
	login := func(c syncCLI, email string) {
		c.sync(t, true, "login", "--connection", "office", "--server", server.URL, "--email", email, "--password-file", password)
	}
	login(ca, "owner@example.test")
	login(cb, "member@example.test")
	for _, c := range []syncCLI{ca, cb} {
		c.sync(t, true, "pair", "--connection", "office", "--org", f.Org, "--server-repo", f.Repo)
	}
	// A second native project/connection in the SAME SQLite store is unaffected.
	other := syncCLI{labPair{dir: t.TempDir(), db: b.db}, cb.config}
	if raw, err := exec.Command("git", "init", "--quiet", other.host.dir).CombinedOutput(); err != nil {
		t.Fatal(err, string(raw))
	}
	var initialized struct{ Repo string }
	if err := json.Unmarshal([]byte(other.command(t, true, "--json", "init")), &initialized); err != nil {
		t.Fatal(err)
	}
	other.host.repo = initialized.Repo
	var control struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fixtureHTTP(t, "POST", f.base+"/api/organizations/"+f.Org+"/repos", f.Owner, map[string]string{"name": "Control", "slug": "control", "creation_key": "11111111-1111-4111-8111-000000000092"}, 201), &control); err != nil {
		t.Fatal(err)
	}
	other.sync(t, true, "login", "--connection", "personal", "--server", server.URL, "--email", "owner@example.test", "--password-file", password)
	other.sync(t, true, "pair", "--connection", "personal", "--org", f.Org, "--server-repo", control.ID)
	other.matter(t, "Other connection remains shared")
	other.sync(t, true, "run")
	controlBefore := other.status(t)
	n := ca.matter(t, "Retained shared Matter")
	brief := filepath.Join(t.TempDir(), "brief")
	const text = "# Retained λ Brief\n"
	if err := os.WriteFile(brief, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ca.command(t, true, "plumbing", "brief", n.Locator, "--file", brief)
	ca.command(t, true, "plumbing", "start", n.Locator)
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	cloudOnly := ca.matter(t, "Cloud-only relative to second machine")
	ca.sync(t, true, "run")
	if _, err := sb.Node(labCtx, cloudOnly.ID); err == nil {
		t.Fatal("cloud-only control already downloaded")
	}
	old := cb.status(t)
	before := labEvents(t, sb)
	histories := map[string][]store.ExperimentEvaluation{}
	for _, d := range old.Decisions {
		group := d.Source.Actions[0].Event.ID
		history, err := sb.ExperimentDecisionHistory(labCtx, group)
		if err != nil {
			t.Fatal(err)
		}
		histories[group] = history
	}
	args := []string{"repo", "delete", "--connection", "office", "--org", f.Org, "--server-repo", f.Repo}
	cb.sync(t, false, append(args, "--dry-run")...)
	preview := ca.sync(t, true, append(args, "--dry-run")...)
	if !strings.Contains(preview, f.Repo) || !strings.Contains(preview, "changes") || !strings.Contains(preview, "Cloud-only work may be lost") {
		t.Fatal("missing destructive inventory", preview)
	}
	lose.Store(true)
	if out := ca.sync(t, false, append(args, "--yes")...); !strings.Contains(out, "remote_outcome_unknown") {
		t.Fatal("lost committed DELETE claimed success", out)
	}
	if out := ca.sync(t, false, append(args, "--yes")...); !strings.Contains(out, "absent OR inaccessible") {
		t.Fatal("404 claimed confirmed deletion", out)
	}
	cb.sync(t, false, "run")
	var replacement struct {
		ID string `json:"id"`
	}
	raw := fixtureHTTP(t, "POST", f.base+"/api/organizations/"+f.Org+"/repos", f.Owner, map[string]string{"name": "Replacement", "slug": "work", "creation_key": "11111111-1111-4111-8111-000000000091"}, 201)
	if err := json.Unmarshal(raw, &replacement); err != nil || replacement.ID == f.Repo || replacement.ID == "" {
		t.Fatal(string(raw), err)
	}
	cb.sync(t, false, "run") // Same slug must not redirect a stale binding.
	f.stop()
	cb.sync(t, true, "connection", "forget", "office", "--dry-run")
	cb.sync(t, true, "connection", "forget", "office", "--yes")
	if !reflect.DeepEqual(before, labEvents(t, sb)) {
		t.Fatal("offline reset changed native events")
	}
	for group, before := range histories {
		after, err := sb.ExperimentDecisionHistory(labCtx, group)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("offline reset altered immutable source/evaluation evidence", err)
		}
	}
	content, err := sb.Content(labCtx, n.ID, store.KindBrief)
	if err != nil || string(content) != text {
		t.Fatal("offline reset lost integrated Brief", string(content), err)
	}
	if _, err := sa.Node(labCtx, cloudOnly.ID); err != nil {
		t.Fatal("deletion erased source machine's local work", err)
	}
	cb.matter(t, "During offline fallback")
	f.start(t)
	other.sync(t, true, "run")
	if !reflect.DeepEqual(controlBefore, other.status(t)) {
		t.Fatal("reset altered another connection's origin, receipts or cursor")
	}
	login(cb, "member@example.test")
	cb.sync(t, true, "pair", "--connection", "office", "--org", f.Org, "--server-repo", replacement.ID)
	fresh := cb.status(t)
	if fresh.Binding.InstallationID == old.Binding.InstallationID || len(fresh.PendingUpload) != 0 || fresh.Cursor != 0 {
		t.Fatal("reconnect reused old state or exported pre-reset work", old, fresh)
	}
	cb.sync(t, true, "run")
	cb.matter(t, "New shared work")
	cb.sync(t, true, "run")
	var feed struct {
		Changes []store.ExchangeChange `json:"changes"`
	}
	if err := json.Unmarshal(fixtureHTTP(t, "GET", f.base+"/api/organizations/"+f.Org+"/repos/"+replacement.ID+"/changes", f.Owner, nil, 200), &feed); err != nil || len(feed.Changes) != 1 {
		t.Fatal("new destination received pre-reset history or integration effects", len(feed.Changes), err)
	}
	if !strings.Contains(feed.Changes[0].Source, "New shared work") || strings.Contains(feed.Changes[0].Source, "Retained shared") {
		t.Fatal("wrong future-only source", feed.Changes[0].Source)
	}
	t.Log("actual Laravel: owner-only deletion, lost reply and ambiguous retry, stale-ID rejection, offline local preservation, fresh origin and future-only reconnect passed")
}
