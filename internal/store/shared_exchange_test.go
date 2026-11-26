//go:build sharedstoreexperiment

package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
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

// labExchangeFeed is a minimal stateful exchange endpoint: it stores uploaded
// changes under contiguous server sequences and serves committed pages, so
// portable-derivative coverage exercises the real version-1 wire contract
// without a Laravel fixture. It also keeps a digest-keyed blob store with the
// same verify-then-acknowledge contract as Cloud, and records request order so
// tests can prove blobs land before the changes that name them.
type labExchangeFeed struct {
	mu       sync.Mutex
	changes  []store.ExchangeChange
	blobs    map[string][]byte
	requests []string
	// blobPutStatus/blobGetStatus, when non-zero, are the status every blob
	// PUT/GET returns — the transient-failure controls.
	blobPutStatus, blobGetStatus int
	// blobGetTruncate makes GET declare a Content-Length beyond the bytes
	// written, so the client's body read dies mid-stream like a dropped
	// connection.
	blobGetTruncate bool
	// failNextChangesPost commits the next POST /changes then answers 500 —
	// the acknowledgement-lost-after-commit control.
	failNextChangesPost bool
}

func newLabExchangeFeed(t *testing.T) (*labExchangeFeed, string) {
	t.Helper()
	feed := &labExchangeFeed{blobs: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feed.mu.Lock()
		defer feed.mu.Unlock()
		feed.requests = append(feed.requests, r.Method+" "+r.URL.Path)
		blob := strings.Contains(r.URL.Path, "/blobs/")
		if blob {
			feed.serveBlob(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/origins"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/changes"):
			var body struct {
				Changes []store.ExchangeChange `json:"changes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Changes) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// Cloud's idempotent replay: an identical re-post of a committed
			// (installation, group) is already stored; a different source under
			// the same identity conflicts and stores nothing.
			committed := append([]store.ExchangeChange(nil), feed.changes...)
			for _, change := range body.Changes {
				seen := false
				for _, existing := range committed {
					if existing.InstallationID == change.InstallationID && existing.GroupID == change.GroupID {
						if existing.Source != change.Source || existing.SourceRepoID != change.SourceRepoID {
							w.WriteHeader(http.StatusConflict)
							return
						}
						seen = true
						break
					}
				}
				if seen {
					continue
				}
				change.Sequence = int64(len(committed)) + 1
				committed = append(committed, change)
			}
			feed.changes = committed
			if feed.failNextChangesPost {
				feed.failNextChangesPost = false
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/changes"):
			after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			var page struct {
				Changes []store.ExchangeChange `json:"changes"`
				Cursor  int64                  `json:"cursor"`
			}
			for _, change := range feed.changes {
				if change.Sequence > after {
					page.Changes = append(page.Changes, change)
					page.Cursor = change.Sequence
				}
			}
			if len(page.Changes) == 0 {
				page.Cursor = after
			}
			_ = json.NewEncoder(w).Encode(page)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return feed, server.URL + "/api/organizations/org/repos/repo"
}

// blobDigest extracts the {digest} path segment of a /blobs/ request.
func (f *labExchangeFeed) blobDigest(r *http.Request) string {
	return r.URL.Path[strings.LastIndex(r.URL.Path, "/blobs/")+len("/blobs/"):]
}

// labBlobMaxBytes mirrors Cloud's whole-object cap so oversized PUTs fail
// here exactly as they would against the real service.
const labBlobMaxBytes = 32 << 20

// serveBlob implements the Cloud blob contract: PUT verifies the declared
// length and content digest before storing (201 first store, 200 exact
// retry), GET serves verified bytes under X-Blob-SHA256, and HEAD probes
// availability. Absent and unauthorized are the same indistinguishable 404.
func (f *labExchangeFeed) serveBlob(w http.ResponseWriter, r *http.Request) {
	digest := f.blobDigest(r)
	switch r.Method {
	case "PUT":
		if f.blobPutStatus != 0 {
			w.WriteHeader(f.blobPutStatus)
			return
		}
		declared, _ := strconv.ParseInt(r.URL.Query().Get("length"), 10, 64)
		if declared > labBlobMaxBytes {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		data, _ := io.ReadAll(r.Body)
		if r.URL.Query().Get("length") != strconv.Itoa(len(data)) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		_, exists := f.blobs[digest]
		f.blobs[digest] = data
		w.Header().Set("Content-Type", "application/json")
		if !exists {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"digest": digest, "byte_length": len(data), "created": !exists})
	case "GET", "HEAD":
		if f.blobGetStatus != 0 {
			w.WriteHeader(f.blobGetStatus)
			return
		}
		data, ok := f.blobs[digest]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Blob-SHA256", digest)
		if f.blobGetTruncate && r.Method == "GET" {
			// Declared length exceeds the body written: the server closes the
			// connection short and the client's ReadAll fails mid-body.
			w.Header().Set("Content-Length", strconv.Itoa(len(data)+1))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data[:len(data)/2])
			return
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == "GET" {
			_, _ = w.Write(data)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveBlobStatus arms the transient-failure controls; status 0 restores
// normal blob serving.
func (f *labExchangeFeed) serveBlobStatus(put, get int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobPutStatus, f.blobGetStatus = put, get
}

// truncateBlobGets makes the next blob GETs die mid-body until cleared.
func (f *labExchangeFeed) truncateBlobGets(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobGetTruncate = on
}

// failNextChanges arms the commit-then-500 control for the next POST /changes.
func (f *labExchangeFeed) failNextChanges() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNextChangesPost = true
}

// requestIndex returns the position of the first request matching method and
// path suffix, or -1 — the ordering oracle for upload-before-commit.
func (f *labExchangeFeed) requestIndex(method, suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.requests {
		if strings.HasPrefix(r, method+" ") && strings.HasSuffix(r, suffix) {
			return i
		}
	}
	return -1
}

// hasRequest reports whether any recorded request matches method and
// contains frag — for assertions suffix matching cannot express, like "any
// blob GET at all".
func (f *labExchangeFeed) hasRequest(method, frag string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.HasPrefix(r, method+" ") && strings.Contains(r, frag) {
			return true
		}
	}
	return false
}

// blob serves the stored bytes for digest, for corrupt-then-serve fixtures.
func (f *labExchangeFeed) blob(digest string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.blobs[digest]
	return data, ok
}

// setBlob injects or replaces stored bytes directly — the server-side
// corruption control a verifying PUT can never produce.
func (f *labExchangeFeed) setBlob(digest string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[digest] = data
}

// removeBlob drops stored bytes, simulating a blob the server never received.
func (f *labExchangeFeed) removeBlob(digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blobs, digest)
}

// changeCount reports how many changes the feed has committed.
func (f *labExchangeFeed) changeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.changes)
}

// changeOrder lists committed group IDs in feed sequence order.
func (f *labExchangeFeed) changeOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.changes))
	for i, c := range f.changes {
		out[i] = c.GroupID
	}
	return out
}

// postChangesIndices returns the request-log positions of every POST /changes,
// in arrival order — paired with changeOrder it proves each blob PUT landed
// before the changes POST that made its group receivable.
func (f *labExchangeFeed) postChangesIndices() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for i, r := range f.requests {
		if strings.HasPrefix(r, "POST ") && strings.HasSuffix(r, "/changes") {
			out = append(out, i)
		}
	}
	return out
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
		// Actual unsupported local work remains retained, never replayed as
		// sync. Gate and reference facts are portable now, so the
		// retained-evidence fixture is a config.set — a family no classifier
		// admits.
		if err := sa.SetConfig(labCtx, a.repo, "fixture.unsupported", "1"); err != nil {
			t.Fatal(err)
		}
		seeded, err := sa.MatterByLocator(labCtx, a.repo, "seeded")
		if err != nil {
			t.Fatal(err)
		}
		unsupported := sourceGroup(t, sa, a.repo, store.TypeConfigSet)
		exchangePass(t, sa, ca, ba, unsupported)
		exchangePass(t, sb, cb, bb)
		sourceDecision(t, sb, unsupported, "conflict")
		if _, err := sb.ExperimentAcknowledge(labCtx, unsupported); err != nil {
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
	// Gate and reference facts are portable now, so the unsupported fixture
	// is a config.set — a family no classifier admits.
	if err := p.fluke.SetConfig(labCtx, p.repo, "fixture.unsupported", "1"); err != nil {
		t.Fatal(err)
	}
	unsupportedGroup := sourceGroup(t, p.fluke, p.repo, store.TypeConfigSet)
	exchangePass(t, p.fluke, cb, bb, unsupportedGroup)
	exchangePass(t, p.hybrid, ca, ba)
	unsupported := sourceDecision(t, p.hybrid, unsupportedGroup, "conflict")
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
			if a.Event.ID == unsupportedGroup && a.Disposition == "conflict" && a.Origin == bb.InstallationID && reflect.DeepEqual(a.Event, unsupported.Source.Actions[0].Event) {
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

// A mixed command group crosses the real exchange as its portable unit: the
// committed feed carries the separately identified derivative, the omitted
// members stay local, and the receiver integrates exactly the unit.
func TestLaravelExchangePortableDerivative(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	if a.repo == b.repo {
		t.Fatal("independent native Repos unexpectedly identical")
	}
	if _, err := sa.PairExchange(labCtx, a.repo, f.endpoint()); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.PairExchange(labCtx, b.repo, f.endpoint()); err != nil {
		t.Fatal(err)
	}
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}, Token: f.Member}
	var mid, sid, tid string
	events := labCommitMixed(t, sa, a.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		mid, sid, tid = tx.NewID(), tx.NewID(), tx.NewID()
		brief, err := tx.ContentDraft(mid, store.KindBrief, []byte("deriv brief"))
		if err != nil {
			return nil, err
		}
		brief.Cause = 0
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: mid, Payload: store.NodeBirth{Title: "Deriv", Locator: "deriv", SortKey: 0}},
			{Type: store.TypeConfigSet, Subject: a.repo, Payload: store.ConfigSet{Key: "deriv.key", Value: "v"}, Cause: 0},
			{Type: store.TypeStageCreated, Subject: sid, Payload: store.NodeBirth{Title: "Phase", Locator: "phase", Parent: mid, SortKey: 0}, Cause: 0},
			{Type: store.TypeStepCreated, Subject: tid, Payload: store.NodeBirth{Title: "Do", Locator: "do", Parent: sid, SortKey: 0}, Cause: 1},
			brief,
		}, nil
	})
	mixed := events[0].Correlation
	if _, err := sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	// The committed feed carries the separately identified portable unit; the
	// raw mixed group itself is never the envelope.
	var page struct {
		Changes []store.ExchangeChange `json:"changes"`
	}
	if err := json.Unmarshal(fixtureHTTP(t, "GET", f.endpoint()+"/changes?after=0", f.Member, nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	var unit store.ExperimentSource
	found := false
	for _, change := range page.Changes {
		if change.GroupID == mixed {
			found = true
			if err := json.Unmarshal([]byte(change.Source), &unit); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found || unit.Derivative == nil || unit.Derivative.Parent != mixed || len(unit.Actions) != 3 || len(unit.Derivative.Omitted) != 0 {
		t.Fatalf("Laravel did not retain the portable-unit envelope: %+v", unit)
	}
	before := len(labEvents(t, sb))
	if _, err := sb.RunExchange(labCtx, cb, b.repo); err != nil {
		t.Fatal(err)
	}
	if len(labEvents(t, sb)) != before+3 {
		t.Fatal("receiver did not integrate exactly the portable unit")
	}
	m, err := sb.Node(labCtx, mid)
	if err != nil || m.Kind != store.ScaleMatter || m.Repo != b.repo || m.Locator != "deriv" {
		t.Fatalf("matter = %+v %v", m, err)
	}
	sn, err := sb.Node(labCtx, sid)
	if err != nil || sn.Kind != store.ScaleStage || sn.Parent != mid {
		t.Fatalf("stage = %+v %v", sn, err)
	}
	brief, err := sb.Content(labCtx, mid, store.KindBrief)
	if err != nil || string(brief) != "deriv brief" {
		t.Fatalf("brief = %q %v", brief, err)
	}
	if _, err := sb.Node(labCtx, tid); err == nil {
		t.Fatal("portable-typed member with a non-portable in-group cause integrated")
	}
	d := sourceDecision(t, sb, mixed, "integrated")
	if d.Source.Derivative == nil || d.Source.Derivative.Parent != mixed || len(d.Effects) != 3 {
		t.Fatalf("receiver decision lost derivative identity or effects: %+v", d)
	}
	if len(d.Source.Derivative.Omitted) != 0 {
		t.Fatal("excluded raw events reached Laravel and the receiver")
	}
	labRebuild(t, sb)
	t.Log("mixed command crossed real Laravel as its identified portable unit; excluded raw events stayed local")
}

// An incompatible source version is apply-time evidence, not a receipt
// failure: receipt still validates the envelope structure, the committed
// page and cursor land intact, the record is retained as an inspectable
// conflict, and later rows in the same feed still apply and redeliver
// inertly.
func TestSharedStoreIncompatibleSourceVersionExchange(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	if a.repo == b.repo {
		t.Fatal("independent native Repos unexpectedly identical")
	}
	client := store.ExchangeClient{HTTP: http.DefaultClient, Token: "token"}
	ba, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.PairExchange(labCtx, b.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	post := func(src store.ExperimentSource) string {
		t.Helper()
		raw, err := json.Marshal(src)
		if err != nil {
			t.Fatal(err)
		}
		change := store.ExchangeChange{Version: 1, InstallationID: src.Host, GroupID: src.Actions[0].Event.ID, SourceRepoID: a.repo, Source: string(raw)}
		fixtureHTTP(t, "POST", endpoint+"/changes", "token", map[string]any{"changes": []store.ExchangeChange{change}}, http.StatusNoContent)
		return change.GroupID
	}
	var badGroup, goodGroup, goodMatter string
	for i, title := range []string{"Incompatible first", "Compatible second"} {
		n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, title, "")
		if err != nil {
			t.Fatal(err)
		}
		group := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
		src, err := sa.ExperimentExport(labCtx, ba.Binding.InstallationID, group)
		if err != nil {
			t.Fatal(err)
		}
		if src.Version != 1 {
			t.Fatalf("exported source version = %d, want 1", src.Version)
		}
		if i == 0 {
			src.Version = 2
			badGroup = post(src)
		} else {
			goodGroup, goodMatter = post(src), n.ID
		}
	}
	if len(feed.changes) != 2 || feed.changes[0].GroupID != badGroup || feed.changes[1].GroupID != goodGroup {
		t.Fatalf("feed order = %+v", feed.changes)
	}
	before := len(labEvents(t, sb))
	if _, err := sb.RunExchange(labCtx, client, b.repo); err != nil {
		t.Fatal(err)
	}
	// Receipt kept both committed records: cursor and inbox applied together,
	// the incompatible one retained as a conflict naming its version.
	status, err := sb.ExchangeStatus(labCtx, b.repo)
	if err != nil || status.Downloaded != 2 || status.Evaluated != 2 || status.Conflicts != 1 {
		t.Fatalf("status = %+v %v", status, err)
	}
	bad := sourceDecision(t, sb, badGroup, "conflict")
	if !strings.Contains(bad.Reason, "incompatible source version 2") || len(bad.Effects) != 0 {
		t.Fatalf("incompatible decision = %+v", bad)
	}
	good := sourceDecision(t, sb, goodGroup, "integrated")
	if len(good.Effects) != 1 {
		t.Fatalf("compatible decision = %+v", good)
	}
	m, err := sb.Node(labCtx, goodMatter)
	if err != nil || m.Repo != b.repo {
		t.Fatalf("compatible matter did not integrate after the incompatible record: %+v %v", m, err)
	}
	if got := len(labEvents(t, sb)); got != before+1 {
		t.Fatalf("receiver events %d -> %d, want exactly the one compatible effect", before, got)
	}
	// Redelivery is inert: the cursor already passed the page and the stored
	// decisions answer again without new effects.
	if _, err := sb.RunExchange(labCtx, client, b.repo); err != nil {
		t.Fatal(err)
	}
	if again := sourceDecision(t, sb, badGroup, "conflict"); !reflect.DeepEqual(bad, again) {
		t.Fatal("redelivery changed the retained incompatible decision", again)
	}
	if got := len(labEvents(t, sb)); got != before+1 {
		t.Fatal("redelivery appended effects")
	}
	t.Log("incompatible source version retained as conflict at sequence 1; compatible record at sequence 2 integrated; cursor/inbox complete and redelivery inert")
}

// Stage-3 discovery scope: one ordinary pass on the actively paired Repo
// backfills every eligible original — including work that predates the
// recorded pairing boundary — exactly once, while sibling projects in the
// same store that never paired or are paused contribute nothing to the
// feed and keep their local work untouched.
func TestLaravelExchangeDiscoveryScope(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	known := map[string]bool{a.repo: true}
	// Two more native Repos share this store: one never pairs, one pairs and
	// is then paused. Both keep ordinary supported work that must stay local.
	siblingRepo := func() string {
		t.Helper()
		p := labPair{dir: t.TempDir(), db: a.db}
		if out, err := exec.Command("git", "init", "--quiet", p.dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		labCLICommand(t, p, true, "init", "--json")
		repos, err := sa.Repos(labCtx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range repos {
			if !known[r.ID] {
				known[r.ID] = true
				return r.ID
			}
		}
		t.Fatal("new Repo not visible in shared store")
		return ""
	}
	unpairedRepo := siblingRepo()
	pausedRepo := siblingRepo()

	// Supported work that predates pairing on the active Repo.
	old, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Old supported", "old-supported")
	if err != nil {
		t.Fatal(err)
	}
	brief := []byte("# Pre-pair Brief\n\nSupported bytes older than the boundary.\n")
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, old.Locator, store.KindBrief, brief); err != nil {
		t.Fatal(err)
	}
	oldBirth := sourceGroup(t, sa, old.ID, store.TypeMatterCreated)
	oldBrief := sourceGroup(t, sa, old.ID, store.TypeContentCreated)
	paired, err := sa.PairExchange(labCtx, a.repo, f.endpoint())
	if err != nil || !paired.Paired || !paired.Active {
		t.Fatal("pairing failed", paired, err)
	}
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Member}
	// The first ordinary pass discovers and acknowledges the pre-pair
	// originals: they are the retained originals later passes must not
	// upload again.
	first, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.PendingUpload) != 0 || !reflect.DeepEqual(first.Retained, []string{oldBirth, oldBrief}) {
		t.Fatalf("pre-pair originals not acknowledged once: %+v", first)
	}

	// Newer supported work on the active Repo, plus pending originals on the
	// unpaired and paused sibling projects.
	newer, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Newer work", "newer-work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, newer.Locator); err != nil {
		t.Fatal(err)
	}
	newBirth := sourceGroup(t, sa, newer.ID, store.TypeMatterCreated)
	newStart := sourceGroup(t, sa, newer.ID, store.TypeMatterStarted)
	if _, err := sa.PairExchange(labCtx, pausedRepo, f.endpoint()); err != nil {
		t.Fatal(err)
	}
	pausedMatter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, pausedRepo, "Paused work", "paused-work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, pausedRepo, pausedMatter.Locator, store.KindBrief, []byte("paused bytes")); err != nil {
		t.Fatal(err)
	}
	if err := sa.UnpairExchange(labCtx, pausedRepo); err != nil {
		t.Fatal(err)
	}
	unpairedMatter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, unpairedRepo, "Never paired", "never-paired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, unpairedRepo, unpairedMatter.Locator, store.KindBrief, []byte("unpaired bytes")); err != nil {
		t.Fatal(err)
	}
	siblingGroups := []string{
		sourceGroup(t, sa, unpairedMatter.ID, store.TypeMatterCreated),
		sourceGroup(t, sa, unpairedMatter.ID, store.TypeContentCreated),
		sourceGroup(t, sa, pausedMatter.ID, store.TypeMatterCreated),
		sourceGroup(t, sa, pausedMatter.ID, store.TypeContentCreated),
	}

	beforeUnpaired, err := sa.ExchangeStatus(labCtx, unpairedRepo)
	if err != nil || beforeUnpaired.Paired || len(beforeUnpaired.PendingUpload) != 0 || beforeUnpaired.EarlierGroups == 0 {
		t.Fatal("unpaired preview claimed outgoing work", beforeUnpaired, err)
	}
	beforePaused, err := sa.ExchangeStatus(labCtx, pausedRepo)
	if err != nil || !beforePaused.Paired || beforePaused.Active || len(beforePaused.PendingUpload) != 2 {
		t.Fatal("paused pairing lost its pending originals", beforePaused, err)
	}
	// Neither non-active project can run an ordinary pass; both refusals
	// happen before any HTTP, so the feed is untouched.
	for _, repo := range []string{unpairedRepo, pausedRepo} {
		if _, err := sa.RunExchange(labCtx, ca, repo); err == nil || !strings.Contains(err.Error(), "not actively paired") {
			t.Fatalf("non-active Repo %s exchanged: %v", repo, err)
		}
	}
	feed := func() []store.ExchangeChange {
		t.Helper()
		var page struct {
			Changes []store.ExchangeChange `json:"changes"`
			Cursor  int64                  `json:"cursor"`
		}
		if err := json.Unmarshal(fixtureHTTP(t, "GET", f.endpoint()+"/changes", f.Owner, nil, 200), &page); err != nil {
			t.Fatal(err)
		}
		return page.Changes
	}
	if got := len(feed()); got != 2 {
		t.Fatalf("feed before backfill = %d changes, want the 2 acknowledged originals", got)
	}

	// The ordinary pass backfills exactly the newer eligible originals; the
	// acknowledged originals and both sibling projects stay out of the feed.
	second, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.PendingUpload) != 0 || second.Cursor != 4 || !reflect.DeepEqual(second.Retained, []string{oldBirth, oldBrief, newBirth, newStart}) {
		t.Fatalf("backfill did not acknowledge exactly the eligible originals: %+v", second)
	}
	changes := feed()
	if len(changes) != 4 {
		t.Fatalf("feed = %d changes, want exactly the 4 eligible originals", len(changes))
	}
	want := map[string]bool{oldBirth: true, oldBrief: true, newBirth: true, newStart: true}
	for _, change := range changes {
		if !want[change.GroupID] || change.SourceRepoID != a.repo || change.InstallationID != second.Binding.InstallationID {
			t.Fatalf("feed carries work outside the active pairing: %+v", change)
		}
		delete(want, change.GroupID)
	}
	if len(want) != 0 {
		t.Fatalf("eligible originals missing from feed: %v", want)
	}
	for _, group := range siblingGroups {
		for _, change := range changes {
			if change.GroupID == group {
				t.Fatalf("sibling project group %s reached the feed", group)
			}
		}
	}

	// A repeated ordinary pass changes nothing: acknowledged originals are
	// not re-uploaded, the pairing is not re-created and the boundary stays.
	third, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.PendingUpload) != 0 || len(third.Retained) != 4 || len(feed()) != 4 || third.Cursor != second.Cursor {
		t.Fatal("repeated ordinary pass re-uploaded acknowledged originals", third)
	}
	if third.Binding != second.Binding || third.Boundary != second.Boundary {
		t.Fatal("ordinary passes re-paired or moved the boundary", third)
	}
	afterUnpaired, err := sa.ExchangeStatus(labCtx, unpairedRepo)
	if err != nil || afterUnpaired.Paired || len(afterUnpaired.PendingUpload) != 0 || afterUnpaired.EarlierGroups != beforeUnpaired.EarlierGroups {
		t.Fatal("ordinary pass touched the unpaired project", afterUnpaired, err)
	}
	afterPaused, err := sa.ExchangeStatus(labCtx, pausedRepo)
	if err != nil || !afterPaused.Paired || afterPaused.Active || !reflect.DeepEqual(afterPaused.PendingUpload, beforePaused.PendingUpload) {
		t.Fatal("ordinary pass touched the paused pairing", afterPaused, err)
	}
	if got, err := sa.Content(labCtx, unpairedMatter.ID, store.KindBrief); err != nil || string(got) != "unpaired bytes" {
		t.Fatal("unpaired project's local work changed", err)
	}
	if got, err := sa.Content(labCtx, pausedMatter.ID, store.KindBrief); err != nil || string(got) != "paused bytes" {
		t.Fatal("paused project's local work changed", err)
	}

	// The second store receives the whole backfill in one ordinary pass; the
	// sibling projects' work never leaves the sender.
	b, sb := exchangeHost(t)
	received, err := sb.PairExchange(labCtx, b.repo, f.endpoint())
	if err != nil || !received.Paired {
		t.Fatal("receiver pairing failed", received, err)
	}
	done, err := sb.RunExchange(labCtx, cb, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	if done.Cursor != 4 || done.Integrated != 4 || done.Conflicts != 0 {
		t.Fatalf("receiver did not integrate the backfilled originals once: %+v", done)
	}
	if got, err := sb.Node(labCtx, old.ID); err != nil || got.Repo != b.repo || got.Lifecycle != store.Planned {
		t.Fatalf("acknowledged original did not arrive: %+v %v", got, err)
	}
	if got, err := sb.Node(labCtx, newer.ID); err != nil || got.Repo != b.repo || got.Lifecycle != store.InProgress {
		t.Fatalf("newer work did not arrive: %+v %v", got, err)
	}
	if got, err := sb.Content(labCtx, old.ID, store.KindBrief); err != nil || string(got) != string(brief) {
		t.Fatalf("pre-pair Brief bytes changed: %q %v", got, err)
	}
	for _, absent := range []string{unpairedMatter.ID, pausedMatter.ID} {
		if _, err := sb.Node(labCtx, absent); err == nil {
			t.Fatal("non-active project work reached the receiver", absent)
		}
	}
	t.Log("pre-pair Matter/Brief acknowledged once, newer work backfilled once, unpaired and paused sibling Repos untouched; second store received all four originals")
}

// Concurrent ordinary passes on one pairing must not duplicate originals:
// the server dedups by installation+group, local acknowledgements and the
// cursor stay idempotent, and every pass converges to the same retained
// status with operation identity and payloads unchanged.
func TestLaravelExchangeConcurrentPasses(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	if _, err := sa.PairExchange(labCtx, a.repo, f.endpoint()); err != nil {
		t.Fatal(err)
	}
	n, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Raced originals", "raced-originals")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, n.Locator, store.KindBrief, []byte("concurrent pass bytes")); err != nil {
		t.Fatal(err)
	}
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	brief := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Owner}
	const passes = 4
	var wg sync.WaitGroup
	errs := make([]error, passes)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = sa.RunExchange(labCtx, ca, a.repo)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent pass %d: %v", i, err)
		}
	}
	var feed struct {
		Changes []store.ExchangeChange `json:"changes"`
	}
	if err := json.Unmarshal(fixtureHTTP(t, "GET", f.endpoint()+"/changes", f.Owner, nil, 200), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Changes) != 2 {
		t.Fatalf("concurrent passes duplicated originals: %d feed changes", len(feed.Changes))
	}
	seen := map[string]bool{}
	for _, change := range feed.Changes {
		if change.GroupID != birth && change.GroupID != brief {
			t.Fatalf("unexpected group %s on feed", change.GroupID)
		}
		if change.SourceRepoID != a.repo || seen[change.GroupID] {
			t.Fatalf("feed carries duplicated or foreign work: %+v", change)
		}
		seen[change.GroupID] = true
	}
	done, err := sa.ExchangeStatus(labCtx, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.PendingUpload) != 0 || len(done.Retained) != 2 || done.Cursor != 2 || done.Downloaded != 2 || done.Evaluated != 2 {
		t.Fatal("concurrent passes left unstable transfer state", done)
	}
	// A repeated pass after the race changes nothing: same retained
	// originals, cursor and binding, no new effects.
	again, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.PendingUpload) != 0 || len(again.Retained) != 2 || again.Cursor != done.Cursor || again.Binding != done.Binding || again.Boundary != done.Boundary {
		t.Fatal("post-race pass changed retained state", again)
	}
	if got, err := sa.Node(labCtx, n.ID); err != nil || got.Lifecycle != store.Planned {
		t.Fatal("raced originals did not project once", err)
	}
	if segments, err := sa.ContentSegments(labCtx, n.ID, store.KindBrief); err != nil || len(segments) != 1 {
		t.Fatal("Brief duplicated under concurrent passes", err)
	}
	t.Log("four concurrent ordinary passes: server kept one copy per original, local acks/cursor converged, post-race pass inert")
}
