//go:build sharedstoreexperiment

package store_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

type laravelFixture struct {
	path, base string
	env        []string
	server     *exec.Cmd
	log        *os.File
	Org        string `json:"org"`
	Repo       string `json:"repo"`
	OtherOrg   string `json:"other_org"`
	MemberID   int64  `json:"member_id"`
	Owner      string `json:"owner"`
	Member     string `json:"member"`
	Outsider   string `json:"outsider"`
}

func startLaravelFixture(t *testing.T) *laravelFixture {
	t.Helper()
	path := os.Getenv("WIP_CLOUD_PATH")
	if path == "" {
		if os.Getenv("WIP_EXCHANGE_REQUIRED") == "1" {
			t.Fatal("WIP_CLOUD_PATH is required for integration acceptance; Laravel tests must not skip")
		}
		t.Skip("set WIP_CLOUD_PATH to the Laravel integration checkout to run real PostgreSQL/HTTP tests")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	db := fmt.Sprintf("wip_exchange_%d_%d", os.Getpid(), time.Now().UnixNano())
	cmd := exec.Command("createdb", "-h", "/var/run/postgresql", db)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create disposable PostgreSQL database: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("dropdb", "-h", "/var/run/postgresql", "--force", db).Run() })
	cacheDir := t.TempDir()
	f := &laravelFixture{path: path, env: append(os.Environ(), "APP_ENV=testing", "APP_DEBUG=false",
		"APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "APP_CONFIG_CACHE="+filepath.Join(cacheDir, "config.php"), "APP_ROUTES_CACHE="+filepath.Join(cacheDir, "routes.php"),
		"DB_URL=", "DB_CONNECTION=pgsql", "DB_HOST=/var/run/postgresql", "DB_DATABASE="+db, "DB_USERNAME="+os.Getenv("USER"), "DB_PASSWORD=", "CACHE_STORE=array", "SESSION_DRIVER=database", "SESSION_SECURE_COOKIE=false", "QUEUE_CONNECTION=sync", "MAIL_MAILER=array")}
	f.php(t, "artisan", "migrate", "--force", "--no-interaction")
	if err := json.Unmarshal(f.php(t, "tests/Support/exchange-fixture.php", "seed"), f); err != nil {
		t.Fatal("fixture seed JSON:", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.base = "http://" + listener.Addr().String()
	_ = listener.Close()
	f.log, err = os.Create(filepath.Join(t.TempDir(), "laravel.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.stop(); _ = f.log.Close() })
	f.start(t)
	return f
}

func (f *laravelFixture) php(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("php", args...)
	cmd.Dir = f.path
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PHP fixture operation failed: %v: %s", err, out)
	}
	return out
}

func (f *laravelFixture) start(t *testing.T) {
	t.Helper()
	addr := strings.TrimPrefix(f.base, "http://")
	f.server = exec.Command("php", "-S", addr, "-t", "public", "public/index.php")
	f.server.Dir = f.path
	f.server.Env = f.env
	f.server.Stdout = f.log
	f.server.Stderr = f.log
	if err := f.server.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(f.base + "/up")
		if err == nil {
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Laravel HTTP server failed readiness")
}

func (f *laravelFixture) stop() {
	if f.server != nil {
		_ = f.server.Process.Kill()
		_ = f.server.Wait()
		f.server = nil
	}
}

func (f *laravelFixture) endpoint() string {
	return f.base + "/api/organizations/" + f.Org + "/repos/" + f.Repo
}

func fixtureHTTP(t *testing.T, method, url, token string, body any, want int) []byte {
	t.Helper()
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, input)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s HTTP %d want %d: %s", method, resp.StatusCode, want, raw)
	}
	return raw
}

func exchangeHost(t *testing.T) (labPair, *store.Store) {
	t.Helper()
	p := labPair{dir: t.TempDir(), db: filepath.Join(t.TempDir(), "local.db")}
	if out, err := exec.Command("git", "init", "--quiet", p.dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %v %s", err, out)
	}
	labCLICommand(t, p, true, "init", "--json")
	s := labOpen(t, p.db)
	events := labEvents(t, s)
	p.repo = events[0].Repo
	return p, s
}

func exchangeBind(t *testing.T, s *store.Store, p labPair, endpoint string) store.ExchangeBinding {
	t.Helper()
	b, err := s.BindExchange(labCtx, p.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func exchangePass(t *testing.T, s *store.Store, c store.ExchangeClient, b store.ExchangeBinding, groups ...string) {
	t.Helper()
	if err := s.ExchangePass(labCtx, c, b, groups); err != nil {
		t.Fatal(err)
	}
}

func sourceGroup(t *testing.T, s *store.Store, subject, kind string) string {
	t.Helper()
	history, err := s.EventsOfSubject(labCtx, subject)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type == kind {
			return history[i].Correlation
		}
	}
	t.Fatal("source group not found", subject, kind)
	return ""
}

func sourceDecision(t *testing.T, s *store.Store, group, want string) store.ExperimentDecision {
	t.Helper()
	all, err := s.ExperimentActivities(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if d.Source.Actions[0].Event.ID == group {
			if d.Disposition != want {
				t.Fatalf("decision %s wanted %s: %+v", group, want, d)
			}
			return d
		}
	}
	t.Fatal("missing decision", group)
	return store.ExperimentDecision{}
}

// The response is lost only AFTER the real Laravel request commits. The same
// wrapper permits an ordinary CLI process to write while HTTP is in progress.
type lostExchangeResponse struct {
	t    *testing.T
	p    labPair
	lost bool
}

func (l *lostExchangeResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/changes") && !l.lost {
		labCLICommand(l.t, l.p, true, "plumbing", "matter", "create", "--title", "During HTTP", "--json")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		l.lost = true
		return nil, fmt.Errorf("injected loss after committed upload")
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestLaravelExchangeRoundTrip(t *testing.T) {
	parent := t
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	if a.repo == b.repo {
		t.Fatal("independent native Repos unexpectedly identical")
	}
	gitA, _ := os.ReadFile(filepath.Join(a.dir, ".git", "config"))
	gitB, _ := os.ReadFile(filepath.Join(b.dir, ".git", "config"))
	repoA, _ := sa.Repo(labCtx, a.repo)
	repoB, _ := sb.Repo(labCtx, b.repo)
	clonesA, err := sa.ClonesOfRepo(labCtx, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	clonesB, err := sb.ClonesOfRepo(labCtx, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	ba := exchangeBind(t, sa, a, f.endpoint())
	bb := exchangeBind(t, sb, b, f.endpoint())
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Member}
	if ba.InstallationID == bb.InstallationID {
		t.Fatal("installations must differ")
	}

	t.Run("seeded supported overlapping starts through Laravel", func(t *testing.T) {
		// Explicit common business seed; never copy Repo/Clone/path lookup rows.
		n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Seeded", "")
		if err != nil {
			t.Fatal(err)
		}
		birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
		seed, err := sa.ExperimentExport(labCtx, ba.InstallationID, birth)
		if err != nil {
			t.Fatal(err)
		}
		if d, err := sb.ExperimentImportBound(labCtx, seed, b.repo); err != nil || d.Disposition != "integrated" {
			t.Fatalf("seed: %+v %v", d, err)
		}
		at := followTime("2026-12-01T09:00:00Z")
		bt := followTime("2026-12-01T09:04:00Z")
		sa = followClock(parent, sa, &at)
		sb = followClock(parent, sb, &bt)
		as := labStart(t, a, sa, ba.InstallationID, "seeded")
		bs := labStart(t, b, sb, bb.InstallationID, "seeded")
		if as.Actions[0].Baseline != bs.Actions[0].Baseline || bs.Actions[0].NativeBaseline == "" {
			t.Fatal("portable baseline must retain the differing observed native baseline")
		}
		nativeA, nativeB := len(labEvents(t, sa)), len(labEvents(t, sb))
		exchangePass(t, sa, ca, ba, as.Actions[0].Event.ID)
		exchangePass(t, sb, cb, bb, bs.Actions[0].Event.ID)
		exchangePass(t, sa, ca, ba)
		if len(labEvents(t, sa)) != nativeA || len(labEvents(t, sb)) != nativeB {
			t.Fatal("equivalent starts appended duplicate effects")
		}
		for _, s := range []*store.Store{sa, sb} {
			for _, src := range []store.ExperimentSource{as, bs} {
				d := sourceDecision(t, s, src.Actions[0].Event.ID, "integrated")
				if !reflect.DeepEqual(d.Source, src) {
					t.Fatal("original identity/time/actor/baseline changed")
				}
			}
		}
		t.Log("two independently initialized Repos: two source origins/times, one native lifecycle start each; originals unchanged")
	})
	if t.Failed() {
		return
	}

	t.Run("useful offline Matter Brief retry restart and conflicts", func(t *testing.T) {
		n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Useful Offline", "")
		if err != nil {
			t.Fatal(err)
		}
		birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
		brief := []byte("# Useful offline work\n\nOriginal bytes: λ, not a PHP fixture.\n")
		if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, n.Locator, store.KindBrief, brief); err != nil {
			t.Fatal(err)
		}
		content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
		loss := &lostExchangeResponse{t: t, p: a}
		lost := ca
		lost.HTTP = &http.Client{Transport: loss, Timeout: 5 * time.Second}
		if err := sa.ExchangePass(labCtx, lost, ba, []string{content}); err == nil || !loss.lost {
			t.Fatal("lost-response control not exercised", err)
		}
		f.stop()
		f.start(t)
		// Duplicate content upload is inert; arrival before Matter stays pending.
		exchangePass(t, sa, ca, ba, content)
		exchangePass(t, sb, cb, bb)
		sourceDecision(t, sb, content, "pending")
		exchangePass(t, sa, ca, ba, birth)
		exchangePass(t, sb, cb, bb)
		sourceDecision(t, sb, birth, "integrated")
		sourceDecision(t, sb, content, "integrated")
		followHistory(t, sb, content, 2)
		if err := sb.Close(); err != nil {
			t.Fatal(err)
		}
		sb = labOpen(parent, b.db)
		if again := exchangeBind(t, sb, b, f.endpoint()); again != bb {
			t.Fatal("binding/origin changed on restart")
		}
		before := len(labEvents(t, sb))
		exchangePass(t, sb, cb, bb)
		exchangePass(t, sa, ca, ba, birth, content)
		exchangePass(t, sb, cb, bb)
		if len(labEvents(t, sb)) != before {
			t.Fatal("retry duplicated local effects")
		}
		f.stop()
		got, err := sb.Content(labCtx, n.ID, store.KindBrief)
		if err != nil || !bytes.Equal(got, brief) {
			t.Fatalf("offline Brief %q %v", got, err)
		}
		labCLICommand(t, b, true, "plumbing", "matter", "create", "--title", "Offline independent", "--json")
		if err := sb.ExchangePass(labCtx, cb, bb, nil); err == nil {
			t.Fatal("unreachable server must report failed sync")
		}
		labCLICommand(t, b, true, "plumbing", "matter", "create", "--title", "After server failure", "--json")
		if err := sb.Rebuild(labCtx); err != nil {
			t.Fatal(err)
		}
		got, err = sb.Content(labCtx, n.ID, store.KindBrief)
		if err != nil || !bytes.Equal(got, brief) {
			t.Fatal("rebuild lost offline content", err)
		}
		f.start(t)
		// New independent Matter goes the other way, not just one copied seed.
		other, err := sb.MatterByLocator(labCtx, b.repo, "offline-independent")
		if err != nil {
			t.Fatal(err)
		}
		otherBirth := sourceGroup(t, sb, other.ID, store.TypeMatterCreated)
		exchangePass(t, sb, cb, bb, otherBirth)
		exchangePass(t, sa, ca, ba)
		if node, err := sa.Node(labCtx, other.ID); err != nil || node.Repo != a.repo {
			t.Fatal("independent Matter did not arrive", err)
		}
		// Actual unsupported local pause remains retained, never replayed as start.
		labCLICommand(t, a, true, "plumbing", "pause", "seeded", "--json")
		seeded, err := sa.MatterByLocator(labCtx, a.repo, "seeded")
		if err != nil {
			t.Fatal(err)
		}
		pause := sourceGroup(t, sa, seeded.ID, store.TypeMatterPaused)
		exchangePass(t, sa, ca, ba, pause)
		exchangePass(t, sb, cb, bb)
		sourceDecision(t, sb, pause, "conflict")
		if _, err := sb.ExperimentAcknowledge(labCtx, pause); err != nil {
			t.Fatal(err)
		}
		if node, err := sb.Node(labCtx, seeded.ID); err != nil || node.Lifecycle != store.InProgress {
			t.Fatal("hard conflict changed lifecycle", err)
		}
		// A generated integration event may not be retransmitted as original work.
		d := sourceDecision(t, sb, birth, "integrated")
		if err := sb.ExchangePass(labCtx, cb, bb, []string{d.Effects[0]}); err == nil {
			t.Fatal("generated effect was exported")
		}
		t.Log("actual Go Matter/inline Brief survived lost HTTP response, PHP/store restart, dependency recheck, offline read and rebuild; conflict retained")
	})
	if t.Failed() {
		return
	}

	t.Run("tenancy revocation and identity preservation", func(t *testing.T) {
		fixtureHTTP(t, "PATCH", f.endpoint(), f.Owner, map[string]string{"remote_url": "https://github.com/example/disposable.git"}, 200)
		exchangePass(t, sa, ca, ba)
		exchangePass(t, sb, cb, bb)
		fixtureHTTP(t, "GET", f.endpoint()+"/changes", f.Outsider, nil, 404)
		fixtureHTTP(t, "GET", strings.Replace(f.endpoint(), f.Org, f.OtherOrg, 1)+"/changes", f.Owner, nil, 404)
		fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Member, map[string]string{"installation_id": ba.InstallationID}, 403)
		fixtureHTTP(t, "DELETE", f.endpoint()+"/origins/"+bb.InstallationID, f.Owner, nil, 200)
		if err := sb.ExchangePass(labCtx, cb, bb, nil); err == nil {
			t.Fatal("revoked origin could synchronize")
		}
		fixtureHTTP(t, "DELETE", f.base+"/api/organizations/"+f.Org+"/members/"+fmt.Sprint(f.MemberID), f.Owner, nil, 204)
		fixtureHTTP(t, "GET", f.endpoint()+"/changes", f.Member, nil, 404)
		fixtureHTTP(t, "DELETE", f.base+"/api/token", f.Owner, nil, 204)
		fixtureHTTP(t, "GET", f.endpoint()+"/changes", f.Owner, nil, 401)
		gotA, _ := sa.Repo(labCtx, a.repo)
		gotB, _ := sb.Repo(labCtx, b.repo)
		if !reflect.DeepEqual(repoA, gotA) || !reflect.DeepEqual(repoB, gotB) {
			t.Fatal("native Repo identity/metadata changed")
		}
		endClonesA, err := sa.ClonesOfRepo(labCtx, a.repo)
		if err != nil {
			t.Fatal(err)
		}
		endClonesB, err := sb.ClonesOfRepo(labCtx, b.repo)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(clonesA, endClonesA) || !reflect.DeepEqual(clonesB, endClonesB) {
			t.Fatal("native Clone identity/checkout path changed")
		}
		endA, _ := os.ReadFile(filepath.Join(a.dir, ".git", "config"))
		endB, _ := os.ReadFile(filepath.Join(b.dir, ".git", "config"))
		if !bytes.Equal(gitA, endA) || !bytes.Equal(gitB, endB) {
			t.Fatal("Git configuration changed")
		}
		if _, err := sa.BindExchange(labCtx, a.repo, f.endpoint()+"-different"); err == nil {
			t.Fatal("binding silently switched")
		}
		stats := f.php(t, "tests/Support/exchange-fixture.php", "stats")
		t.Logf("durable server state %s; native IDs and Git preserved", stats)
	})
}

func TestLaravelExchangeRecoveryProcess(t *testing.T) {
	path := os.Getenv("WIP_EXCHANGE_RECOVERY_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var binding store.ExchangeBinding
	if err := json.Unmarshal([]byte(os.Getenv("WIP_EXCHANGE_RECOVERY_BINDING")), &binding); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyExchangeInbox(labCtx, binding); err != nil {
		t.Fatal(err)
	}
	got, err := s.Content(labCtx, os.Getenv("WIP_EXCHANGE_RECOVERY_MATTER"), store.KindBrief)
	if err != nil || string(got) != "durable cross-language inbox bytes" {
		t.Fatalf("offline recovered Brief %q %v", got, err)
	}
}

func TestLaravelExchangeDurableReceiptAndConflictingContent(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ba := exchangeBind(t, sa, a, f.endpoint())
	bb := exchangeBind(t, sb, b, f.endpoint())
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Member}
	n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Durable", "")
	if err != nil {
		t.Fatal(err)
	}
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, n.Locator, store.KindBrief, []byte("durable cross-language inbox bytes")); err != nil {
		t.Fatal(err)
	}
	brief := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	exchangePass(t, sa, ca, ba, birth, brief)
	db, err := sql.Open("sqlite", b.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	execSQL := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	state := func(wantCursor, wantInbox, wantProcessed int) {
		t.Helper()
		var cursor, inbox, processed int
		if err := db.QueryRow(`SELECT cursor FROM exchange_bindings WHERE repo=?`, b.repo).Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(applied),0) FROM exchange_inbox`).Scan(&inbox, &processed); err != nil {
			t.Fatal(err)
		}
		if cursor != wantCursor || inbox != wantInbox || processed != wantProcessed {
			t.Fatalf("cursor/inbox/processed=%d/%d/%d want %d/%d/%d", cursor, inbox, processed, wantCursor, wantInbox, wantProcessed)
		}
	}
	execSQL(`CREATE TRIGGER reject_receipt BEFORE INSERT ON exchange_inbox WHEN NEW.sequence=2 BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`)
	if err := sb.ExchangePass(labCtx, cb, bb, nil); err == nil {
		t.Fatal("receipt fault did not fire")
	}
	state(0, 0, 0)
	execSQL(`DROP TRIGGER reject_receipt`)
	execSQL(`CREATE TRIGGER reject_effect BEFORE INSERT ON events WHEN NEW.actor='system:shared-store-experiment' BEGIN SELECT RAISE(ABORT,'injected integration failure'); END`)
	if err := sb.ExchangePass(labCtx, cb, bb, nil); err == nil {
		t.Fatal("application fault did not fire")
	}
	state(2, 2, 0)
	if _, err := sb.Node(labCtx, n.ID); err == nil {
		t.Fatal("failed application exposed business effect")
	}
	execSQL(`DROP TRIGGER reject_effect`)
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
	f.stop()
	raw, err := json.Marshal(bb)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestLaravelExchangeRecoveryProcess$", "-test.v")
	child.Env = append(os.Environ(), "WIP_EXCHANGE_RECOVERY_DB="+b.db, "WIP_EXCHANGE_RECOVERY_BINDING="+string(raw), "WIP_EXCHANGE_RECOVERY_MATTER="+n.ID)
	execSQL(`CREATE TRIGGER reject_processed BEFORE UPDATE OF applied ON exchange_inbox WHEN NEW.sequence=1 BEGIN SELECT RAISE(ABORT,'injected loss after effect commit'); END`)
	if _, err := child.CombinedOutput(); err == nil {
		t.Fatal("post-effect process failure did not fire")
	}
	state(2, 2, 0)
	var effects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE actor='system:shared-store-experiment'`).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("committed effect before process failure: %d %v", effects, err)
	}
	execSQL(`DROP TRIGGER reject_processed`)
	child = exec.Command(os.Args[0], "-test.run=^TestLaravelExchangeRecoveryProcess$", "-test.v")
	child.Env = append(os.Environ(), "WIP_EXCHANGE_RECOVERY_DB="+b.db, "WIP_EXCHANGE_RECOVERY_BINDING="+string(raw), "WIP_EXCHANGE_RECOVERY_MATTER="+n.ID)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("offline fresh-process recovery: %v %s", err, out)
	}
	state(2, 2, 2)
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE actor='system:shared-store-experiment'`).Scan(&effects); err != nil || effects != 2 {
		t.Fatalf("recovered effects must not duplicate: %d %v", effects, err)
	}
	sb = labOpen(t, b.db)
	before := len(labEvents(t, sb))
	if err := sb.ApplyExchangeInbox(labCtx, bb); err != nil {
		t.Fatal(err)
	}
	if len(labEvents(t, sb)) != before {
		t.Fatal("offline receipt replay duplicated effects")
	}
	f.start(t)
	// Two hosts author different accepted Briefs after the same shared birth.
	c, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Competing", "")
	if err != nil {
		t.Fatal(err)
	}
	sharedBirth := sourceGroup(t, sa, c.ID, store.TypeMatterCreated)
	exchangePass(t, sa, ca, ba, sharedBirth)
	exchangePass(t, sb, cb, bb)
	for _, host := range []struct {
		s          *store.Store
		repo, text string
	}{{sa, a.repo, "accepted A"}, {sb, b.repo, "accepted B"}} {
		if _, err := writesurface.WriteOnce(labCtx, host.s, store.ActorHuman, host.repo, c.Locator, store.KindBrief, []byte(host.text)); err != nil {
			t.Fatal(err)
		}
	}
	ga := sourceGroup(t, sa, c.ID, store.TypeContentCreated)
	gb := sourceGroup(t, sb, c.ID, store.TypeContentCreated)
	exchangePass(t, sa, ca, ba, ga)
	exchangePass(t, sb, cb, bb, gb)
	exchangePass(t, sa, ca, ba)
	sourceDecision(t, sa, gb, "conflict")
	sourceDecision(t, sb, ga, "conflict")
	for _, host := range []struct {
		s    *store.Store
		text string
	}{{sa, "accepted A"}, {sb, "accepted B"}} {
		got, err := host.s.Content(labCtx, c.ID, store.KindBrief)
		if err != nil || string(got) != host.text {
			t.Fatal("competing Brief overwritten", err)
		}
	}
	t.Log("receipt failure: cursor/inbox 0/0; application failure: 2/2 with no effects; offline new process: 2/2 processed once; competing Briefs retained without overwrite")
}

func TestLaravelExchangeIdentityCollisions(t *testing.T) {
	for _, actorChanged := range []bool{true, false} {
		t.Run(fmt.Sprint("native collision actorChanged=", actorChanged), func(t *testing.T) {
			f := startLaravelFixture(t)
			a, sa := exchangeHost(t)
			b, sb := exchangeHost(t)
			ba := exchangeBind(t, sa, a, f.endpoint())
			bb := exchangeBind(t, sb, b, f.endpoint())
			ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Owner}
			if _, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Native start", ""); err != nil {
				t.Fatal(err)
			}
			genuine := labStart(t, a, sa, ba.InstallationID, "native-start")
			forged, err := sa.ExperimentExport(labCtx, ba.InstallationID, genuine.Actions[0].Event.ID)
			if err != nil {
				t.Fatal(err)
			}
			forged.Host = bb.InstallationID
			if actorChanged {
				forged.Actions[0].Event.Actor = store.RoleActor("Forged")
			}
			fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Member, map[string]string{"installation_id": bb.InstallationID}, 200)
			raw, err := json.Marshal(forged)
			if err != nil {
				t.Fatal(err)
			}
			change := store.ExchangeChange{Version: 1, InstallationID: bb.InstallationID, GroupID: forged.Actions[0].Event.ID, SourceRepoID: a.repo, Source: string(raw)}
			for range 2 {
				fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Member, map[string]any{"changes": []store.ExchangeChange{change}}, 200)
			}
			exchangePass(t, sa, ca, ba)
			sourceDecision(t, sa, change.GroupID, "conflict")
			for _, activity := range followPreview(t, sa, ba.InstallationID).Activities {
				if activity.Event.ID == change.GroupID && (activity.Origin != ba.InstallationID || !reflect.DeepEqual(activity.Event, genuine.Actions[0].Event)) {
					t.Fatal("foreign transport attribution replaced native original")
				}
			}
			before := len(labEvents(t, sa))
			f.stop()
			f.start(t)
			if err := sa.Close(); err != nil {
				t.Fatal(err)
			}
			sa = labOpen(t, a.db)
			later, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "After collision", "")
			if err != nil {
				t.Fatal(err)
			}
			laterGroup := sourceGroup(t, sa, later.ID, store.TypeMatterCreated)
			exchangePass(t, sa, ca, ba, change.GroupID, laterGroup)
			exchangePass(t, sa, ca, ba, change.GroupID, laterGroup)
			all, err := sa.ExperimentActivities(labCtx)
			if err != nil || len(all) != 3 {
				t.Fatalf("conflict, genuine echo and subsequent work: %+v %v", all, err)
			}
			var integrated int
			for _, d := range all {
				if d.Disposition == "integrated" {
					integrated++
				}
			}
			if integrated != 2 || len(labEvents(t, sa)) != before+1 {
				t.Fatal("collision poisoned later inbox progress or duplicated native work")
			}
			t.Log("foreign collision retained once; authentic activity preserved; genuine echo and later work processed after PHP/store restart and duplicate transfer")
		})
	}
	for _, tombstoned := range []bool{false, true} {
		t.Run(fmt.Sprint("global Brief identity tombstoned=", tombstoned), func(t *testing.T) {
			f := startLaravelFixture(t)
			a, sa := exchangeHost(t)
			b, sb := exchangeHost(t)
			ba := exchangeBind(t, sa, a, f.endpoint())
			bb := exchangeBind(t, sb, b, f.endpoint())
			ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Owner}
			cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Member}
			var matters []store.Node
			var briefs []store.ExperimentSource
			var births []string
			for _, title := range []string{"Accepted A", "Colliding B", "Later C"} {
				n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, title, "")
				if err != nil {
					t.Fatal(err)
				}
				matters = append(matters, n)
				births = append(births, sourceGroup(t, sa, n.ID, store.TypeMatterCreated))
				if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, n.Locator, store.KindBrief, []byte(title+" original bytes")); err != nil {
					t.Fatal(err)
				}
				brief, err := sa.ExperimentExport(labCtx, ba.InstallationID, sourceGroup(t, sa, n.ID, store.TypeContentCreated))
				if err != nil {
					t.Fatal(err)
				}
				briefs = append(briefs, brief)
			}
			exchangePass(t, sa, ca, ba, append(births, briefs[0].Actions[0].Event.ID)...)
			exchangePass(t, sb, cb, bb)
			var accepted, collision store.ContentWritten
			if err := json.Unmarshal(briefs[0].Actions[0].Event.Payload, &accepted); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(briefs[1].Actions[0].Event.Payload, &collision); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", b.db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if tombstoned {
				// No ordinary verb tombstones content today; seed that schema state
				// as content_test.go does, without inventing a sync removal family.
				marker, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Tombstone marker", "")
				if err != nil {
					t.Fatal(err)
				}
				newer := sourceGroup(t, sb, marker.ID, store.TypeMatterCreated)
				if _, err := db.Exec(`UPDATE content SET tombstone_event=?,last_event=? WHERE id=?`, newer, newer, accepted.Content); err != nil {
					t.Fatal(err)
				}
			}
			collision.Content = accepted.Content
			briefs[1].Actions[0].Event.Payload, err = json.Marshal(collision)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(briefs[1])
			if err != nil {
				t.Fatal(err)
			}
			change := store.ExchangeChange{Version: 1, InstallationID: ba.InstallationID, GroupID: briefs[1].Actions[0].Event.ID, SourceRepoID: a.repo, Source: string(raw)}
			fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Owner, map[string]any{"changes": []store.ExchangeChange{change}}, 200)
			exchangePass(t, sa, ca, ba, briefs[2].Actions[0].Event.ID)
			exchangePass(t, sb, cb, bb)
			sourceDecision(t, sb, change.GroupID, "conflict")
			sourceDecision(t, sb, briefs[2].Actions[0].Event.ID, "integrated")
			before := len(labEvents(t, sb))
			if err := sb.Close(); err != nil {
				t.Fatal(err)
			}
			sb = labOpen(t, b.db)
			f.stop()
			if err := sb.ApplyExchangeInbox(labCtx, bb); err != nil {
				t.Fatal(err)
			}
			sourceDecision(t, sb, change.GroupID, "conflict")
			got, err := sb.Content(labCtx, matters[2].ID, store.KindBrief)
			if err != nil || string(got) != "Later C original bytes" {
				t.Fatalf("later unrelated offline Brief %q %v", got, err)
			}
			var original []byte
			if err := db.QueryRow(`SELECT bytes FROM content WHERE id=?`, accepted.Content).Scan(&original); err != nil || string(original) != "Accepted A original bytes" {
				t.Fatal("accepted content overwritten", err)
			}
			segments, err := sb.ContentSegments(labCtx, matters[1].ID, store.KindBrief)
			if err != nil || len(segments) != 0 || len(labEvents(t, sb)) != before {
				t.Fatal("collision produced content or retry produced effects", err)
			}
			t.Log("global content-ID reuse retained as durable conflict; accepted bytes unchanged; later valid Brief readable offline after store restart")
		})
	}
}

func TestLaravelExchangeMixedCollisionPreservesCanonicalActivity(t *testing.T) {
	f := startLaravelFixture(t)
	p := newLabPair(t)
	ba := exchangeBind(t, p.hybrid, p, f.endpoint())
	bb := exchangeBind(t, p.fluke, p, f.endpoint())
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Member}
	// Interleave real stamped IDs: foreign root < native root < foreign child.
	// Canonical foreign activity must sort before the separately retained claim.
	at := followTime("2026-12-01T09:00:00Z")
	if err := p.fluke.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	p.fluke, err = store.OpenWithClock(p.fluke.Path(), func() time.Time {
		now := at
		at = at.Add(2 * time.Second)
		return now
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.fluke.Close() })
	foreign := labStart(t, p, p.fluke, bb.InstallationID, "shared/step-01")
	nativeTime := followTime("2026-12-01T09:00:01Z")
	p.hybrid = followClock(t, p.hybrid, &nativeTime)
	native := labStart(t, p, p.hybrid, ba.InstallationID, "shared/step-02")
	if len(foreign.Actions) != 3 || len(native.Actions) != 3 || foreign.Actions[0].Event.ID >= native.Actions[0].Event.ID || native.Actions[0].Event.ID >= foreign.Actions[1].Event.ID {
		t.Fatal("fixture must interleave increasing native/foreign cascade IDs")
	}
	exchangePass(t, p.hybrid, ca, ba, native.Actions[0].Event.ID)
	exchangePass(t, p.fluke, cb, bb, foreign.Actions[0].Event.ID)
	exchangePass(t, p.hybrid, ca, ba)
	if d := sourceDecision(t, p.hybrid, foreign.Actions[0].Event.ID, "integrated"); !reflect.DeepEqual(d.Source, foreign) {
		t.Fatal("authentic foreign source changed during integration")
	}
	// An ordinary unsupported source remains canonical conflict activity. The
	// correction must exclude collision groups, not all conflicted decisions.
	cli := p
	cli.db = p.fluke.Path()
	labCLICommand(t, cli, true, "plumbing", "pause", "shared/step-01", "--json")
	pauseGroup := sourceGroup(t, p.fluke, p.step1, store.TypeStepPaused)
	exchangePass(t, p.fluke, cb, bb, pauseGroup)
	exchangePass(t, p.hybrid, ca, ba)
	pause := sourceDecision(t, p.hybrid, pauseGroup, "conflict")
	before := followPreview(t, p.hybrid, ba.InstallationID)
	nativeLog := labEvents(t, p.hybrid)
	claim := store.ExperimentSource{Host: "33333333-3333-4333-8333-333333333333", Actions: []store.ExperimentAction{native.Actions[0], foreign.Actions[1]}}
	claim.Actions[1].Event.Actor = store.RoleActor("ForgedForeign")
	claim.Actions[1].Event.Causation = claim.Actions[0].Event.ID
	claim.Actions[1].Event.Correlation = claim.Actions[0].Event.ID
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	change := store.ExchangeChange{Version: 1, InstallationID: claim.Host, GroupID: claim.Actions[0].Event.ID, SourceRepoID: p.repo, Source: string(raw)}
	fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Owner, map[string]string{"installation_id": claim.Host}, 200)
	check := func() {
		t.Helper()
		after := followPreview(t, p.hybrid, ba.InstallationID)
		for _, want := range foreign.Actions {
			var found int
			for _, a := range after.Activities {
				if a.Event.ID == want.Event.ID {
					found++
					if !reflect.DeepEqual(a.Event, want.Event) || a.Origin != foreign.Host || a.Disposition != "integrated" {
						t.Fatalf("mixed collision replaced canonical foreign activity: %+v", a)
					}
				}
			}
			if found != 1 {
				t.Fatal("foreign original missing or duplicated")
			}
		}
		var conflictVisible bool
		for _, a := range after.Activities {
			if a.Event.ID == pauseGroup && a.Disposition == "conflict" && a.Origin == bb.InstallationID && reflect.DeepEqual(a.Event, pause.Source.Actions[0].Event) {
				conflictVisible = true
			}
		}
		if !conflictVisible {
			t.Fatal("ordinary canonical conflict activity was hidden")
		}
		if !reflect.DeepEqual(before.Periods, after.Periods) || !reflect.DeepEqual(before, after) {
			t.Fatal("collision changed canonical source-time periods or activity evidence")
		}
		all, err := p.hybrid.ExperimentActivities(labCtx)
		if err != nil || len(all) != 4 {
			t.Fatalf("canonical decisions plus one inspectable collision: %+v %v", all, err)
		}
		var retained int
		for _, d := range all {
			if reflect.DeepEqual(d.Source, claim) && d.Disposition == "conflict" && len(d.Effects) == 0 {
				retained++
			}
		}
		if retained != 1 || !reflect.DeepEqual(nativeLog, labEvents(t, p.hybrid)) {
			t.Fatal("collision lost/doubled retention or changed native evidence")
		}
		if d := sourceDecision(t, p.hybrid, foreign.Actions[0].Event.ID, "integrated"); !reflect.DeepEqual(d.Source, foreign) {
			t.Fatal("canonical foreign source storage changed")
		}
	}
	for range 2 {
		fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Owner, map[string]any{"changes": []store.ExchangeChange{change}}, 200)
		exchangePass(t, p.hybrid, ca, ba)
		check()
	}
	if err := p.hybrid.Close(); err != nil {
		t.Fatal(err)
	}
	p.hybrid = labOpen(t, p.db)
	f.stop()
	if err := p.hybrid.ApplyExchangeInbox(labCtx, ba); err != nil {
		t.Fatal(err)
	}
	check()
	t.Log("Laravel accepted increasing-ID mixed claim; retry/reopen/offline apply retained it once without changing canonical foreign attribution, periods, ordinary conflict visibility or native evidence")
}

func TestLaravelExchangeCommittedPages(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ba := exchangeBind(t, sa, a, f.endpoint())
	bb := exchangeBind(t, sb, b, f.endpoint())
	fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Owner, map[string]string{"installation_id": ba.InstallationID}, 200)
	var changes []store.ExchangeChange
	var subjects []string
	// Cross the server's 100-record page boundary; this is test scope, not a quota.
	for i := 0; i < 103; i++ {
		n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, fmt.Sprintf("Paged work %03d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		group := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
		source, err := sa.ExperimentExport(labCtx, ba.InstallationID, group)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, store.ExchangeChange{Version: 1, InstallationID: ba.InstallationID, GroupID: group, SourceRepoID: a.repo, Source: string(raw)})
		subjects = append(subjects, n.ID)
	}
	fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Owner, map[string]any{"changes": changes}, 200)
	var page struct {
		Changes []store.ExchangeChange `json:"changes"`
		Cursor  int64                  `json:"cursor"`
	}
	if err := json.Unmarshal(fixtureHTTP(t, "GET", f.endpoint()+"/changes?after=0", f.Member, nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 100 || page.Cursor != 100 {
		t.Fatalf("first committed page count/cursor=%d/%d", len(page.Changes), page.Cursor)
	}
	for i, change := range page.Changes {
		if change.Source != changes[i].Source || change.UploaderID == 0 || change.ReceivedAt == "" || change.SourceRepoID != a.repo {
			t.Fatal("server changed original bytes or omitted uploader/receipt distinction")
		}
	}
	client := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Member}
	exchangePass(t, sb, client, bb)
	for i, id := range subjects {
		node, err := sb.Node(labCtx, id)
		if err != nil || node.Repo != b.repo || node.Title != fmt.Sprintf("Paged work %03d", i) {
			t.Fatal("committed page lost or remapped work", i, err)
		}
	}
	before := len(labEvents(t, sb))
	exchangePass(t, sb, client, bb)
	if len(labEvents(t, sb)) != before {
		t.Fatal("page replay duplicated effects")
	}
	t.Log("103 actual Go-created Matters crossed 100+3 committed feed pages, exact source bytes retained, mapped once into the other native Repo")
}
