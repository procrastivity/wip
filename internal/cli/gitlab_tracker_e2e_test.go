package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
	gitlabtracker "github.com/procrastivity/wip/internal/tracker/gitlab"
)

const (
	gitlabCLIProject = "group%2Fsub%2Fproject"
	gitlabCLIBase    = "/api/v4/projects/" + gitlabCLIProject
	gitlabCLIRef     = "https://gitlab.test/group/sub/project/-/work_items/7"
	gitlabCLILabel   = "wf::canceled"
)

type gitlabCLIRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn gitlabCLIRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

// gitlabCLIFake models just enough of the GitLab Issues REST surface for the
// CLI to drive one create-then-lifecycle round trip through the real
// adapter: one project, one issue at iid 7, and the labels endpoint the
// canceled disposition consults. postUpdatedAt/putUpdatedAt let each test
// pick its own updated_at lease progression rather than sharing one.
type gitlabCLIFake struct {
	t             *testing.T
	calls         []string
	created       bool
	state         string
	labels        []string
	updated       string
	description   string
	postUpdatedAt string
	putUpdatedAt  string
	putBodies     []map[string]any
}

func (f *gitlabCLIFake) issue() map[string]any {
	return map[string]any{
		"iid":         7,
		"web_url":     gitlabCLIRef,
		"state":       f.state,
		"labels":      f.labels,
		"updated_at":  f.updated,
		"description": f.description,
	}
}

func (f *gitlabCLIFake) respond(request *http.Request, status int, payload any) (*http.Response, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(encoded))),
		Request:    request,
	}, nil
}

func (f *gitlabCLIFake) roundTrip(request *http.Request) (*http.Response, error) {
	f.t.Helper()
	if request.Header.Get("PRIVATE-TOKEN") != "test-token" {
		f.t.Fatalf("GitLab PRIVATE-TOKEN = %q", request.Header.Get("PRIVATE-TOKEN"))
	}
	if request.URL.Host != "api.gitlab.test" {
		f.t.Fatalf("GitLab request host = %q", request.URL.Host)
	}
	path := request.URL.EscapedPath()
	f.calls = append(f.calls, request.Method+" "+path+"?"+request.URL.RawQuery)

	switch {
	case request.Method == http.MethodGet && path == gitlabCLIBase+"/issues":
		query := request.URL.Query()
		if query.Get("state") != "all" || query.Get("per_page") != "100" {
			f.t.Fatalf("GitLab issues list query = %v", query)
		}
		var body []map[string]any
		if f.created {
			body = []map[string]any{f.issue()}
		}
		return f.respond(request, http.StatusOK, body)
	case request.Method == http.MethodPost && path == gitlabCLIBase+"/issues":
		var decoded struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			f.t.Fatal(err)
		}
		if decoded.Title != "Reconcile GitLab create" || !strings.Contains(decoded.Description, "<!-- wip-idempotency:") {
			f.t.Fatalf("GitLab create body = %+v", decoded)
		}
		f.created = true
		f.state = "opened"
		f.updated = f.postUpdatedAt
		f.description = decoded.Description
		return f.respond(request, http.StatusCreated, f.issue())
	case request.Method == http.MethodGet && path == gitlabCLIBase+"/issues/7":
		return f.respond(request, http.StatusOK, f.issue())
	case request.Method == http.MethodGet && path == gitlabCLIBase+"/labels":
		query := request.URL.Query()
		if query.Get("search") != gitlabCLILabel {
			f.t.Fatalf("GitLab labels search = %q", query.Get("search"))
		}
		return f.respond(request, http.StatusOK, []map[string]any{{"name": gitlabCLILabel}})
	case request.Method == http.MethodPut && path == gitlabCLIBase+"/issues/7":
		var decoded map[string]any
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			f.t.Fatal(err)
		}
		f.putBodies = append(f.putBodies, decoded)
		switch decoded["state_event"] {
		case "close":
			f.state = "closed"
		case "reopen":
			f.state = "opened"
		default:
			f.t.Fatalf("GitLab PUT state_event = %v", decoded["state_event"])
		}
		if add, ok := decoded["add_labels"].(string); ok && add != "" {
			f.labels = append(f.labels, add)
		}
		f.updated = f.putUpdatedAt
		return f.respond(request, http.StatusOK, f.issue())
	default:
		f.t.Fatalf("unexpected GitLab request %s %s", request.Method, path)
		return nil, nil
	}
}

// chdirToGitRepo mirrors linear_tracker_e2e_test.go's setup: a fresh git repo
// with a GitLab origin, the process cwd pointed at it for the duration of the
// test, and an isolated WIP_DB_PATH.
func chdirToGitRepo(t *testing.T, name string) (dir, dbPath string) {
	t.Helper()
	dir = newGitRepo(t, name)
	gitIn(t, dir, "remote", "add", "origin", "git@gitlab.test:group/sub/project.git")
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	dbPath = filepath.Join(t.TempDir(), "wip.db")
	t.Setenv("WIP_DB_PATH", dbPath)
	return dir, dbPath
}

func gitlabCLIProviders(fake *gitlabCLIFake) *tracker.Registry {
	providers := tracker.NewRegistry()
	providers.Register("gitlab", gitlabtracker.Factory(gitlabtracker.Options{
		BaseURL: "https://api.gitlab.test/api/v4",
		Token:   "test-token",
		Client:  &http.Client{Transport: gitlabCLIRoundTripFunc(fake.roundTrip)},
	}))
	return providers
}

func TestGitLabCreateThroughCLIPersistsWebURLBinding(t *testing.T) {
	_, dbPath := chdirToGitRepo(t, "gitlab-create")

	fake := &gitlabCLIFake{t: t, postUpdatedAt: "2026-08-28T10:00:00.000-05:00"}
	providers := gitlabCLIProviders(fake)

	for _, args := range [][]string{
		{"init", "--json"},
		{"outbox", "backend", "gitlab"},
		{"outbox", "canceled-label", gitlabCLILabel},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
		}
	}

	if r := runWithProviders(t, providers, "outbox", "backend"); r.exitCode != 0 || r.stdout != "gitlab\n" {
		t.Fatalf("durable backend = exit %d, stdout %q, stderr %q", r.exitCode, r.stdout, r.stderr)
	}
	if r := runWithProviders(t, providers, "outbox", "target"); r.exitCode != 0 || r.stdout != "none\n" {
		t.Fatalf("durable target = exit %d, stdout %q, stderr %q", r.exitCode, r.stdout, r.stderr)
	}
	if r := runWithProviders(t, providers, "outbox", "canceled-label"); r.exitCode != 0 || r.stdout != gitlabCLILabel+"\n" {
		t.Fatalf("durable canceled label = exit %d, stdout %q, stderr %q", r.exitCode, r.stdout, r.stderr)
	}

	added := mustJSON[struct {
		ID string `json:"id"`
	}](t, runWithProviders(t, providers, "backlog", "add", "--title", "Reconcile GitLab create", "--provenance", "intake", "--json").stdout)
	delegated := mustJSON[struct {
		Outbox string `json:"outbox"`
	}](t, runWithProviders(t, providers, "backlog", "delegate", added.ID, "--json").stdout)
	if delegated.Outbox == "" {
		t.Fatal("delegation returned no outbox identity")
	}
	if r := runWithProviders(t, providers, "outbox", "approve", delegated.Outbox); r.exitCode != 0 {
		t.Fatalf("approve: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	flushed := mustJSON[tracker.Report](t, runWithProviders(t, providers, "outbox", "flush", "--json").stdout)
	if len(flushed.Entries) != 1 || flushed.Entries[0].ID != delegated.Outbox || flushed.Entries[0].State != "flushed" {
		t.Fatalf("flush report = %+v", flushed)
	}

	// A fresh backend has no matching issue yet, so the first delivery is a
	// list-scan miss followed by a create — never a Converged dedup match.
	if len(fake.calls) != 2 {
		t.Fatalf("GitLab calls = %v, want 2", fake.calls)
	}
	if !strings.HasPrefix(fake.calls[0], "GET "+gitlabCLIBase+"/issues?") ||
		!strings.Contains(fake.calls[0], "state=all") || !strings.Contains(fake.calls[0], "per_page=100") {
		t.Fatalf("first GitLab call = %q", fake.calls[0])
	}
	if fake.calls[1] != "POST "+gitlabCLIBase+"/issues?" {
		t.Fatalf("second GitLab call = %q", fake.calls[1])
	}

	// Replay/idempotency coverage for the create leg lives in step-04's unit
	// tests: a second delegated backlog entry here would carry a different
	// idempotency key and legitimately create a second issue, so it would
	// not exercise dedup at all.

	s := openTestStore(t, dbPath)
	repos, err := s.Repos(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v (err %v)", repos, err)
	}
	if repos[0].RemoteURL != "gitlab.test/group/sub/project" {
		t.Fatalf("stored remote = %q", repos[0].RemoteURL)
	}
	backend, _, err := s.Config(context.Background(), repos[0].ID, store.TrackerBackendKey)
	if err != nil || backend != "gitlab" {
		t.Fatalf("stored backend = %q (err %v)", backend, err)
	}
	label, _, err := s.Config(context.Background(), repos[0].ID, store.TrackerCanceledLabelKey)
	if err != nil || label != gitlabCLILabel {
		t.Fatalf("stored canceled label = %q (err %v)", label, err)
	}
	entry, err := s.OutboxEntry(context.Background(), repos[0].ID, delegated.Outbox)
	if err != nil || entry.State != "flushed" || entry.Attempts != 1 {
		t.Fatalf("durable create entry = %+v (err %v)", entry, err)
	}
	events, err := s.EventsOfSubject(context.Background(), delegated.Outbox)
	if err != nil || len(events) != 2 || events[1].Type != store.TypeTrackerItemCreated {
		t.Fatalf("binding events = %+v (err %v)", events, err)
	}
	var binding store.TrackerItemCreated
	if err := json.Unmarshal(events[1].Payload, &binding); err != nil || binding.Ref != gitlabCLIRef {
		t.Fatalf("durable binding = %+v (err %v)", binding, err)
	}
	active, err := s.ActiveBacklog(context.Background(), repos[0].ID)
	if err != nil || len(active) != 0 {
		t.Fatalf("active backlog after binding = %+v (err %v)", active, err)
	}
}

func TestGitLabStartConvergesAndCancelAppliesLabelThroughCLI(t *testing.T) {
	_, dbPath := chdirToGitRepo(t, "gitlab-lifecycle")

	fake := &gitlabCLIFake{
		t:            t,
		created:      true,
		state:        "opened",
		updated:      "2026-08-28T11:00:00.000-05:00",
		description:  "<!-- wip-idempotency:seed -->",
		putUpdatedAt: "2026-08-28T11:05:00.000-05:00",
	}
	providers := gitlabCLIProviders(fake)

	for _, args := range [][]string{
		{"init", "--json"},
		{"outbox", "backend", "gitlab"},
		{"outbox", "canceled-label", gitlabCLILabel},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
		}
	}

	matter := mustJSON[nodePayload](t, runWithProviders(t, providers,
		"matter", "create", "--title", "Cancel via GitLab", "--locator", "cancel-gitlab", "--json").stdout)
	if r := runWithProviders(t, providers, "bind", "cancel-gitlab", gitlabCLIRef, "--json"); r.exitCode != 0 {
		t.Fatalf("bind: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	if entries := startTransitionOutbox(t, providers); len(entries) != 0 {
		t.Fatalf("Planned bind queued outbox entries: %+v", entries)
	}

	if r := runWithProviders(t, providers, "start", "cancel-gitlab", "--json"); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	active := wantActiveStartEntry(t, startTransitionOutbox(t, providers), matter.ID, gitlabCLIRef)
	if r := runWithProviders(t, providers, "outbox", "approve", active.ID, "--json"); r.exitCode != 0 {
		t.Fatalf("approve active: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	startFlush := mustJSON[tracker.Report](t, runWithProviders(t, providers, "outbox", "flush", "--json").stdout)
	if len(startFlush.Entries) != 1 || startFlush.Entries[0].ID != active.ID || startFlush.Entries[0].State != "converged" {
		t.Fatalf("start flush report = %+v", startFlush)
	}

	// A blank-lease active candidate against an already-opened issue is a
	// guard read, not a write: one GET, no PUT.
	if len(fake.calls) != 1 || fake.calls[0] != "GET "+gitlabCLIBase+"/issues/7?" {
		t.Fatalf("GitLab calls after start = %v, want exactly one guard read", fake.calls)
	}
	if len(fake.putBodies) != 0 {
		t.Fatalf("start flush wrote %d PUT bodies, want 0", len(fake.putBodies))
	}

	s := openTestStore(t, dbPath)
	startEvents, err := s.EventsOfSubject(context.Background(), active.ID)
	if err != nil || len(startEvents) == 0 || startEvents[len(startEvents)-1].Type != store.TypeTrackerStateObserved {
		t.Fatalf("start entry events = %+v (err %v)", startEvents, err)
	}
	var observed store.TrackerStateObserved
	if err := json.Unmarshal(startEvents[len(startEvents)-1].Payload, &observed); err != nil || observed.Lease != "2026-08-28T11:00:00.000-05:00" {
		t.Fatalf("tracker.state-observed payload = %+v (err %v)", observed, err)
	}

	if r := runWithProviders(t, providers, "cancel", "cancel-gitlab", "--json"); r.exitCode != 0 {
		t.Fatalf("cancel: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	entries := startTransitionOutbox(t, providers)
	var queued []outboxPayload
	for _, entry := range entries {
		if entry.State == "queued" {
			queued = append(queued, entry)
		}
	}
	if len(queued) != 1 {
		t.Fatalf("cancel outbox entries = %+v, want exactly one queued", entries)
	}
	canceled := queued[0]
	var payload struct {
		Disposition store.TrackerDisposition `json:"disposition"`
	}
	if err := json.Unmarshal(canceled.Payload, &payload); err != nil {
		t.Fatalf("cancel candidate payload = %s: %v", canceled.Payload, err)
	}
	if canceled.Kind != "state" || canceled.State != "queued" || canceled.Reference != gitlabCLIRef || payload.Disposition != store.TrackerCanceled {
		t.Fatalf("cancel candidate = %+v payload=%s, want queued canceled state for %s", canceled, canceled.Payload, gitlabCLIRef)
	}

	if r := runWithProviders(t, providers, "outbox", "approve", canceled.ID, "--json"); r.exitCode != 0 {
		t.Fatalf("approve canceled: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	callsBeforeCancelFlush := len(fake.calls)
	cancelFlush := mustJSON[tracker.Report](t, runWithProviders(t, providers, "outbox", "flush", "--json").stdout)
	if len(cancelFlush.Entries) != 1 || cancelFlush.Entries[0].ID != canceled.ID || cancelFlush.Entries[0].State != "flushed" {
		t.Fatalf("cancel flush report = %+v", cancelFlush)
	}

	sinceCancelFlush := fake.calls[callsBeforeCancelFlush:]
	if len(sinceCancelFlush) != 3 ||
		sinceCancelFlush[0] != "GET "+gitlabCLIBase+"/issues/7?" ||
		!strings.HasPrefix(sinceCancelFlush[1], "GET "+gitlabCLIBase+"/labels?") || !strings.Contains(sinceCancelFlush[1], "search=") ||
		sinceCancelFlush[2] != "PUT "+gitlabCLIBase+"/issues/7?" {
		t.Fatalf("GitLab calls for cancel = %v", sinceCancelFlush)
	}
	if len(fake.putBodies) != 1 || fake.putBodies[0]["state_event"] != "close" || fake.putBodies[0]["add_labels"] != gitlabCLILabel {
		t.Fatalf("cancel PUT bodies = %+v", fake.putBodies)
	}
	if fake.state != "closed" || len(fake.labels) != 1 || fake.labels[0] != gitlabCLILabel {
		t.Fatalf("fake issue after cancel: state=%q labels=%v", fake.state, fake.labels)
	}

	cancelEvents, err := s.EventsOfSubject(context.Background(), canceled.ID)
	if err != nil || len(cancelEvents) == 0 || cancelEvents[len(cancelEvents)-1].Type != store.TypeTrackerStatePushed {
		t.Fatalf("cancel entry events = %+v (err %v)", cancelEvents, err)
	}
	var pushed store.TrackerStatePushed
	if err := json.Unmarshal(cancelEvents[len(cancelEvents)-1].Payload, &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.Ref != gitlabCLIRef || pushed.Disposition != store.TrackerCanceled || pushed.Lease != "2026-08-28T11:05:00.000-05:00" {
		t.Fatalf("tracker.state-pushed payload = %+v", pushed)
	}
	record, err := s.TrackerPushRecord(context.Background(), gitlabCLIRef)
	if err != nil || record.Disposition != store.TrackerCanceled {
		t.Fatalf("tracker push record = %+v (err %v)", record, err)
	}
}

func TestGitLabIsAStockBackend(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	result := runIn(t, dir, dbEnv, "outbox", "backend", "gitlab", "--json")
	if result.exitCode != 0 || !strings.Contains(result.stdout, `"backend":"gitlab"`) {
		t.Fatalf("set stock gitlab backend: exit=%d stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
}
