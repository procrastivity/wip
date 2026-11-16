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
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

type syncCLI struct {
	host   labPair
	config string
}

func TestLaravelNamedConnections(t *testing.T) {
	personal, work := startLaravelFixture(t), startLaravelFixture(t)
	a, s := exchangeHost(t)
	home := t.TempDir()
	ca := syncCLI{a, home}
	if out, err := exec.Command("git", "-C", a.dir, "remote", "add", "origin", "git@github.com:Me/Personal.git").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	ca.matter(t, "Before pairing explicitly adopts selected remote")
	// Distinct native Repos in one client store, not isolated client databases.
	b := labPair{dir: t.TempDir(), db: a.db}
	if out, err := exec.Command("git", "init", "--quiet", b.dir).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("git", "-C", b.dir, "remote", "add", "origin", "https://work.example.test/Employer/Work.git").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	cb := syncCLI{b, home}
	cb.command(t, true, "init")
	var initial store.ExchangeStatus
	if err := json.Unmarshal([]byte(cb.sync(t, true, "status")), &initial); err != nil {
		t.Fatal(err)
	}
	b.repo = initial.Binding.LocalRepo
	cb.host = b
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("fixture-password-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, login := range []struct{ name, server, email string }{
		{"personal", personal.base, "owner@example.test"},
		{"work", work.base, "member@example.test"},
		{"colleague", personal.base, "member@example.test"},
	} {
		ca.sync(t, true, "login", "--connection", login.name, "--server", login.server, "--email", login.email, "--password-file", password)
	}
	ca.sync(t, false, "pair", "--org", personal.Org, "--server-repo", personal.Repo)
	configPath := filepath.Join(home, "wip", "config.yaml")
	config := fmt.Sprintf("sync:\n  connections:\n    personal: {server: %q}\n    work: {server: %q}\n  routes:\n    - {repo: 'github.com/**', connection: personal}\n    - {repo: '**', connection: work}\n", personal.base, work.base)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		cli  syncCLI
		name string
		f    *laravelFixture
	}{{ca, "personal", personal}, {cb, "work", work}} {
		preview := pair.cli.sync(t, true, "pair", "--org", pair.f.Org, "--server-repo", pair.f.Repo, "--dry-run")
		if !strings.Contains(preview, `"Connection":"`+pair.name+`"`) {
			t.Fatal("route chose wrong connection", preview)
		}
		pair.cli.sync(t, true, "pair", "--org", pair.f.Org, "--server-repo", pair.f.Repo)
		if got := pair.cli.status(t); got.Binding.Connection != pair.name || got.Binding.Account == "" {
			t.Fatal("missing durable connection/account pin", got)
		}
	}
	if ca.status(t).Binding.InstallationID != cb.status(t).Binding.InstallationID || a.repo == b.repo {
		t.Fatal("installation/native Repo identity changed")
	}
	// Same server, different account is not interchangeable with the binding.
	ca.sync(t, false, "run", "--connection", "colleague")
	ca.sync(t, false, "repos", "--connection", "colleague")
	ca.sync(t, true, "repos", "--connection", "personal")
	ca.sync(t, false, "run", "--server", work.base)
	c := labPair{dir: t.TempDir(), db: a.db}
	if out, err := exec.Command("git", "init", "--quiet", c.dir).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	cc := syncCLI{c, home}
	cc.command(t, true, "init")
	var other struct {
		ID string `json:"id"`
	}
	raw := fixtureHTTP(t, "POST", personal.base+"/api/organizations/"+personal.Org+"/repos", personal.Owner, map[string]string{"name": "Colleague", "slug": "colleague", "creation_key": "55555555-5555-4555-8555-555555555555"}, 201)
	if err := json.Unmarshal(raw, &other); err != nil || other.ID == "" {
		t.Fatal("invalid second destination", err)
	}
	cc.sync(t, true, "pair", "--connection", "colleague", "--org", personal.Org, "--server-repo", other.ID)
	cc.matter(t, "Same origin, different account")
	cc.sync(t, true, "run")
	var feed struct {
		Changes []store.ExchangeChange `json:"changes"`
	}
	if err := json.Unmarshal(fixtureHTTP(t, "GET", personal.base+"/api/organizations/"+personal.Org+"/repos/"+other.ID+"/changes", personal.Owner, nil, 200), &feed); err != nil || len(feed.Changes) != 1 {
		t.Fatal("second-account upload", err)
	}
	if account := cc.status(t).Binding.Account; account == ca.status(t).Binding.Account || account != fmt.Sprint(feed.Changes[0].UploaderID) {
		t.Fatal("same-origin accounts conflated")
	}
	beforeGit := map[string]string{}
	for _, p := range []labPair{a, b} {
		raw, err := os.ReadFile(filepath.Join(p.dir, ".git", "config"))
		if err != nil {
			t.Fatal(err)
		}
		beforeGit[p.dir] = string(raw)
	}
	// Rules/config edits are only suggestions. Retained destinations win even
	// when routing is malformed; explicit run does not parse that configuration.
	if err := os.WriteFile(configPath, []byte("sync:\n  routes: malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []syncCLI{ca, cb} {
		c.sync(t, true, "route")
		c.sync(t, true, "run", "--dry-run")
	}
	na, nb := ca.matter(t, "Only personal"), cb.matter(t, "Only work")
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	if ca.status(t).Cursor != 1 || cb.status(t).Cursor != 1 {
		t.Fatal("cross-server upload or missing own echo")
	}
	if got, err := s.Node(labCtx, na.ID); err != nil || got.Repo != a.repo {
		t.Fatal(err)
	}
	if got, err := s.Node(labCtx, nb.ID); err != nil || got.Repo != b.repo {
		t.Fatal(err)
	}
	// Actual Go originals with deliberately repeated foreign action/group and
	// object IDs arrive through BOTH independent Laravel/PostgreSQL feeds.
	foreign, sf := exchangeHost(t)
	n, err := writesurface.CreateMatter(labCtx, sf, store.ActorHuman, foreign.repo, "Foreign shared ID", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sf, store.ActorHuman, foreign.repo, n.Locator, store.KindBrief, []byte("private personal foreign Brief")); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Start(labCtx, sf, store.ActorHuman, foreign.repo, n.Locator); err != nil {
		t.Fatal(err)
	}
	const origin = "11111111-1111-4111-8111-111111111111"
	var originals []store.ExperimentSource
	for _, kind := range []string{store.TypeMatterCreated, store.TypeContentCreated, store.TypeMatterStarted} {
		src, err := sf.ExperimentExport(labCtx, origin, sourceGroup(t, sf, n.ID, kind))
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, src)
	}
	for _, f := range []*laravelFixture{personal, work} {
		fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Owner, map[string]string{"installation_id": origin}, 200)
		var changes []store.ExchangeChange
		for _, src := range originals {
			if f == work {
				src.Actions[0].Event.Actor = store.Actor("role:WorkForeign")
			}
			raw, err := json.Marshal(src)
			if err != nil {
				t.Fatal(err)
			}
			changes = append(changes, store.ExchangeChange{Version: 1, InstallationID: origin, GroupID: src.Actions[0].Event.ID, SourceRepoID: foreign.repo, Source: string(raw)})
		}
		fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Owner, map[string]any{"changes": changes}, 200)
	}
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	pa, pb := ca.status(t), cb.status(t)
	if pa.Integrated != 4 || pb.Conflicts != 3 || pb.Cursor != 4 {
		t.Fatal("foreign mappings crossed destinations", pa, pb)
	}
	content, err := s.Content(labCtx, n.ID, store.KindBrief)
	if err != nil || string(content) != "private personal foreign Brief" {
		t.Fatal("foreign object overwritten", err)
	}
	group := originals[0].Actions[0].Event.ID
	cb.sync(t, true, "ack", group)
	if len(ca.status(t).Acknowledged) != 0 || len(cb.status(t).Acknowledged) != 1 {
		t.Fatal("ack crossed destination")
	}
	if strings.Contains(ca.sync(t, true, "activity"), "role:WorkForeign") || !strings.Contains(cb.sync(t, true, "activity"), "role:WorkForeign") {
		t.Fatal("activity correspondence crossed destinations")
	}
	beforeEvents := labEvents(t, s)
	pa, pb = ca.status(t), cb.status(t)
	personal.stop()
	personal.start(t)
	work.stop()
	work.start(t)
	for _, c := range []syncCLI{ca, cb} {
		c.sync(t, true, "run")
		c.sync(t, true, "recheck")
		c.sync(t, true, "run")
	}
	if !reflect.DeepEqual(pa, ca.status(t)) || !reflect.DeepEqual(pb, cb.status(t)) || !reflect.DeepEqual(beforeEvents, labEvents(t, s)) {
		t.Fatal("retry/reopen changed history, identity or cursors")
	}
	// One server outage and one token's logout/revocation do not disable the
	// other connection or offline work. Network failures do not advance cursors.
	work.stop()
	cb.sync(t, false, "run")
	ca.matter(t, "Personal during work outage")
	ca.sync(t, true, "run")
	cb.matter(t, "Work remains local during outage")
	cb.sync(t, true, "status")
	cb.sync(t, true, "activity")
	work.start(t)
	cb.sync(t, true, "logout", "--connection", "work")
	cb.sync(t, false, "run")
	ca.sync(t, true, "run")
	cb.sync(t, true, "login", "--connection", "work", "--server", work.base, "--email", "member@example.test", "--password-file", password)
	cb.sync(t, true, "run")
	fixtureHTTP(t, "DELETE", fmt.Sprintf("%s/api/organizations/%s/members/%d", work.base, work.Org, work.MemberID), work.Owner, nil, 204)
	cb.sync(t, false, "run")
	ca.sync(t, true, "run")
	for _, p := range []labPair{a, b} {
		raw, err := os.ReadFile(filepath.Join(p.dir, ".git", "config"))
		if err != nil || string(raw) != beforeGit[p.dir] {
			t.Fatal("Git configuration changed", err)
		}
		if _, err := s.Repo(labCtx, p.repo); err != nil {
			t.Fatal("native identity changed", err)
		}
	}
	t.Log("two actual Laravel/PostgreSQL servers; shared native store; named and same-origin accounts; repeated foreign IDs; retry/restart; independent logout, membership revocation and outage; unchanged Git/native identities")
}

func TestLaravelLegacyConnectionMigration(t *testing.T) {
	f := startLaravelFixture(t)
	p, s := exchangeHost(t)
	c := syncCLI{p, t.TempDir()}
	old, err := s.PairExchange(labCtx, p.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	c.matter(t, "Retained legacy work")
	if _, err := s.RunExchange(labCtx, store.ExchangeClient{HTTP: http.DefaultClient, Token: f.Owner}, p.repo); err != nil {
		t.Fatal(err)
	}
	old, err = s.ExchangeStatus(labCtx, p.repo)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents := labEvents(t, s)
	// Reproduce the older correspondence schema's unprefixed storage keys.
	// Original source bytes and native events are never rewritten.
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{
		`DROP TRIGGER experiment_source_immutable`,
		`UPDATE experiment_sources SET id=substr(id,instr(id,':')+1)`,
		`UPDATE experiment_actions SET id=substr(id,instr(id,':')+1),source_group=substr(source_group,instr(source_group,':')+1)`,
		`UPDATE experiment_evaluations SET source_group=substr(source_group,instr(source_group,':')+1)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnableSharedStoreExperiment(labCtx); err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]string{"Server": f.base, "Token": f.Owner})
	path := filepath.Join(c.config, "wip", "sync", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"connections"}, {"status"}, {"route"}, {"activity"}, {"run", "--dry-run"}, {"pair", "--org", f.Org, "--server-repo", f.Repo, "--dry-run"}} {
		c.sync(t, true, args...)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(legacy) || !reflect.DeepEqual(old, c.status(t)) {
		t.Fatal("offline read migrated legacy state", err)
	}
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("fixture-password-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.sync(t, true, "login", "--connection", "second", "--server", f.base, "--email", "member@example.test", "--password-file", password)
	var registry struct {
		Version     int
		Connections map[string]struct{ Server, Account, Token string }
	}
	raw, err = os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &registry) != nil || registry.Version != 1 || registry.Connections["default"].Token != f.Owner || registry.Connections["default"].Account != "" {
		t.Fatal("credential mutation changed legacy identity/token", err)
	}
	// The retained legacy endpoint, not the newer login, selects default.
	c.sync(t, true, "run")
	got := c.status(t)
	if got.Binding.Connection != "default" || got.Binding.Account == "" {
		t.Fatal("legacy binding was not attached", got)
	}
	old.Binding = got.Binding
	if !reflect.DeepEqual(old, got) || !reflect.DeepEqual(beforeEvents, labEvents(t, s)) {
		t.Fatal("attachment reset history/boundary/cursor or duplicated native work")
	}
	c.sync(t, true, "run")
	c.sync(t, true, "recheck")
	if !reflect.DeepEqual(got, c.status(t)) || !reflect.DeepEqual(beforeEvents, labEvents(t, s)) {
		t.Fatal("legacy retry changed history")
	}
	t.Log("legacy read-only bytes preserved; explicit mutation copied token without rotation; online account attachment retained raw correspondence keys, original source/effect history, boundary, installation and cursor")
}

func TestLaravelLegacyActionReservation(t *testing.T) {
	f := startLaravelFixture(t)
	a, s := exchangeHost(t)
	b := labPair{dir: t.TempDir(), db: a.db}
	if out, err := exec.Command("git", "init", "--quiet", b.dir).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	cb := syncCLI{b, t.TempDir()}
	cb.command(t, true, "init")
	b.repo = cb.status(t).Binding.LocalRepo
	ca := syncCLI{a, t.TempDir()}
	na, nb := ca.matter(t, "Reserved identity"), cb.matter(t, "Independent target")
	ba := exchangeBind(t, s, a, f.endpoint())
	var other struct {
		ID string `json:"id"`
	}
	raw := fixtureHTTP(t, "POST", f.base+"/api/organizations/"+f.Org+"/repos", f.Owner,
		map[string]string{"name": "Independent", "slug": "independent", "creation_key": "55555555-5555-4555-8555-555555555555"}, 201)
	if err := json.Unmarshal(raw, &other); err != nil || other.ID == "" {
		t.Fatal(err)
	}
	endpointB := f.base + "/api/organizations/" + f.Org + "/repos/" + other.ID
	bb := exchangeBind(t, s, b, endpointB)
	const origin = "11111111-1111-4111-8111-111111111111"
	const group = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const action = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	first := store.ExperimentSource{Host: origin, Actions: []store.ExperimentAction{
		{Event: store.Event{ID: group, Correlation: group, Causation: group, Repo: a.repo, Subject: na.ID, Type: "review.unsupported", Actor: store.ActorHuman, OccurredAt: time.Unix(100, 0).UTC(), Payload: json.RawMessage(`{}`)}},
		{Event: store.Event{ID: action, Correlation: group, Causation: group, Repo: a.repo, Subject: na.ID, Type: "review.unsupported", Actor: store.ActorHuman, OccurredAt: time.Unix(101, 0).UTC(), Payload: json.RawMessage(`{}`)}},
	}}
	post := func(endpoint string, source store.ExperimentSource) {
		t.Helper()
		fixtureHTTP(t, "POST", endpoint+"/origins", f.Owner, map[string]string{"installation_id": origin}, 200)
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		change := store.ExchangeChange{Version: 1, InstallationID: origin, GroupID: source.Actions[0].Event.ID, SourceRepoID: a.repo, Source: string(raw)}
		fixtureHTTP(t, "POST", endpoint+"/changes", f.Owner, map[string]any{"changes": []store.ExchangeChange{change}}, 200)
	}
	client := store.ExchangeClient{HTTP: http.DefaultClient, Token: f.Owner}
	post(f.endpoint(), first)
	exchangePass(t, s, client, ba)
	// Reproduce the previous raw-key format without changing original evidence.
	db, err := sql.Open("sqlite", a.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{
		`DROP TRIGGER experiment_source_immutable`,
		`UPDATE experiment_sources SET id=substr(id,instr(id,':')+1)`,
		`UPDATE experiment_actions SET id=substr(id,instr(id,':')+1),source_group=substr(source_group,instr(source_group,':')+1)`,
		`UPDATE experiment_evaluations SET source_group=substr(source_group,instr(source_group,':')+1)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnableSharedStoreExperiment(labCtx); err != nil {
		t.Fatal(err)
	}
	changed := first.Actions[1]
	changed.Event.Correlation, changed.Event.Causation = action, action
	changed.Event.Type = store.TypeMatterStarted
	changed.Baseline = sourceGroup(t, s, na.ID, store.TypeMatterCreated)
	changed.Event.Payload, err = json.Marshal(store.Transition{From: store.Planned, To: store.InProgress})
	if err != nil {
		t.Fatal(err)
	}
	claim := store.ExperimentSource{Host: origin, Actions: []store.ExperimentAction{changed}}
	before := labEvents(t, s)
	post(f.endpoint(), claim)
	for range 2 {
		err := s.ExchangePass(labCtx, client, ba, nil)
		if err == nil {
			t.Error("changed legacy action identity was accepted through Laravel")
		}
		if n, err := s.Node(labCtx, na.ID); err != nil || n.Lifecycle != store.Planned || !reflect.DeepEqual(before, labEvents(t, s)) {
			t.Error("reused action created native effects", n.Lifecycle, err)
		}
	}
	if d, err := s.ExperimentImportBound(labCtx, first, a.repo); err != nil || d.Disposition != "conflict" {
		t.Fatal("genuine legacy retry changed", d, err)
	}
	// The same raw action ID owned by A must not reserve B's namespace.
	claim.Actions[0].Event.Subject = nb.ID
	claim.Actions[0].Baseline = sourceGroup(t, s, nb.ID, store.TypeMatterCreated)
	post(endpointB, claim)
	exchangePass(t, s, client, bb)
	if n, err := s.Node(labCtx, nb.ID); err != nil || n.Lifecycle != store.InProgress {
		t.Fatal("another target's raw key blocked independent work", err)
	}
	before = labEvents(t, s)
	f.stop()
	f.start(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = labOpen(t, a.db)
	exchangePass(t, s, client, bb)
	if !reflect.DeepEqual(before, labEvents(t, s)) {
		t.Fatal("independent retry duplicated effects after restart")
	}
	if err := s.ExchangePass(labCtx, client, ba, nil); err == nil {
		t.Fatal("reopen forgot legacy action reservation")
	}
	t.Log("actual Laravel accepts the cross-group claim; Go refuses same-target legacy action reuse without effects, preserves genuine retry, and independently integrates another target through retry/reopen")
}

func (c syncCLI) command(t *testing.T, ok bool, args ...string) string {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Dir = c.host.dir
	cmd.Env = append(os.Environ(), "WIP_DB_PATH="+c.host.db, "XDG_CONFIG_HOME="+c.config, "WIP_AS_ROLE=", "WIP_EXPERIMENTAL_WIPD_PROFILE=")
	out, err := cmd.CombinedOutput()
	if (err == nil) != ok {
		t.Fatalf("CLI %v: %v: %s", args, err, out)
	}
	if strings.Contains(string(out), "fixture-password-123") || strings.Contains(string(out), `"Token"`) {
		t.Fatal("credential leak in command output")
	}
	return string(out)
}

func (c syncCLI) sync(t *testing.T, ok bool, args ...string) string {
	t.Helper()
	return c.command(t, ok, append([]string{"--json", "plumbing", "sync"}, args...)...)
}

func (c syncCLI) status(t *testing.T) store.ExchangeStatus {
	t.Helper()
	var s store.ExchangeStatus
	if err := json.Unmarshal([]byte(c.sync(t, true, "status")), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (c syncCLI) matter(t *testing.T, title string) store.Node {
	t.Helper()
	var n store.Node
	if err := json.Unmarshal([]byte(c.command(t, true, "--json", "plumbing", "matter", "create", "--title", title)), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLaravelExplicitSyncCLI(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := syncCLI{a, t.TempDir()}
	cb := syncCLI{b, t.TempDir()}
	if a.repo == b.repo {
		t.Fatal("independent native IDs required")
	}
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("fixture-password-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, c := range []syncCLI{ca, cb} {
		email := []string{"owner@example.test", "member@example.test"}[i]
		c.sync(t, true, "login", "--server", f.base, "--email", email, "--password-file", password)
		if i == 0 {
			// Cross both foundation pagination boundaries through its ordinary
			// API. These are discovery setup, not a replacement for CLI login.
			for j := 0; j < 16; j++ {
				fixtureHTTP(t, "POST", f.base+"/api/organizations", f.Owner, map[string]string{"name": fmt.Sprintf("Pagination org %02d", j), "slug": fmt.Sprintf("pagination-org-%02d", j)}, 201)
			}
			for j := 0; j < 17; j++ {
				fixtureHTTP(t, "POST", f.base+"/api/organizations/"+f.Org+"/repos", f.Owner, map[string]string{"name": fmt.Sprintf("Pagination Repo %02d", j), "slug": fmt.Sprintf("pagination-repo-%02d", j), "creation_key": fmt.Sprintf("55555555-5555-4555-8555-%012d", j)}, 201)
			}
		}
		info, err := os.Stat(filepath.Join(c.config, "wip", "sync", "credentials.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("credentials are not private", err)
		}
		discovery := c.sync(t, true, "repos")
		if !strings.Contains(discovery, f.Repo) || strings.Contains(discovery, f.OtherOrg) {
			t.Fatal("discovery tenant mismatch", discovery)
		}
		if i == 0 {
			for j := 0; j < 16; j++ {
				if !strings.Contains(discovery, fmt.Sprintf("pagination-org-%02d", j)) {
					t.Fatal("organization discovery stopped at first page")
				}
			}
			for j := 0; j < 17; j++ {
				if !strings.Contains(discovery, fmt.Sprintf("pagination-repo-%02d", j)) {
					t.Fatal("Repo discovery stopped at first page")
				}
			}
		}
	}
	before := ca.matter(t, "Before pairing stays private")
	ca.sync(t, false, "pair", "--org", f.OtherOrg, "--server-repo", f.Repo)
	if ca.status(t).Paired {
		t.Fatal("failed destination selection paired locally")
	}
	for _, c := range []syncCLI{ca, cb} {
		old := c.status(t)
		local := syncRowCounts(t, c.host.db)
		stats := string(f.php(t, "tests/Support/exchange-fixture.php", "stats"))
		c.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo, "--dry-run")
		if !reflect.DeepEqual(old, c.status(t)) || !reflect.DeepEqual(local, syncRowCounts(t, c.host.db)) || stats != string(f.php(t, "tests/Support/exchange-fixture.php", "stats")) {
			t.Fatal("pair preview mutated durable state")
		}
		c.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo)
		once := c.status(t)
		c.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo)
		if !reflect.DeepEqual(once, c.status(t)) {
			t.Fatal("pair retry changed identity/boundary")
		}
	}
	n := ca.matter(t, "Useful source A")
	brief := filepath.Join(t.TempDir(), "brief.txt")
	const text = "# Offline useful work\n\nExact λ bytes from the real CLI.\n"
	if err := os.WriteFile(brief, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ca.command(t, true, "--json", "plumbing", "brief", n.Locator, "--file", brief)
	cb.matter(t, "Independent source B")
	pre := ca.status(t)
	local := syncRowCounts(t, a.db)
	if len(pre.PendingUpload) != 2 {
		t.Fatal("complete original selection", pre)
	}
	ca.sync(t, true, "run", "--dry-run")
	if !reflect.DeepEqual(pre, ca.status(t)) || !reflect.DeepEqual(local, syncRowCounts(t, a.db)) {
		t.Fatal("run preview changed state")
	}
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	ca.sync(t, true, "run")
	if _, err := sb.Node(labCtx, before.ID); err == nil {
		t.Fatal("pre-pair history silently adopted")
	}
	segments, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segments) != 1 {
		t.Fatal("CLI Brief did not arrive", err)
	}
	bytes, err := sb.SegmentBytes(labCtx, segments[0].ID)
	if err != nil || string(bytes) != text {
		t.Fatal("offline Brief mismatch", err)
	}
	for _, c := range []syncCLI{ca, cb} {
		s := c.status(t)
		if len(s.PendingUpload) != 0 || len(s.Retained) == 0 || s.Cursor != 3 || s.Evaluated != 3 || s.Integrated != 3 || s.Conflicts != 0 {
			t.Fatal("transport/application outcomes", s)
		}
	}
	// Independent offline starts of the same imported Matter coalesce one
	// lifecycle effect without losing either activity or original identity.
	ca.command(t, true, "plumbing", "start", n.Locator)
	cb.command(t, true, "plumbing", "start", n.Locator)
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	ca.sync(t, true, "run")
	activity := cb.sync(t, true, "activity")
	var preview struct {
		Canonical  []store.ExperimentActivity `json:"canonical"`
		Periods    json.RawMessage            `json:"periods"`
		Collisions []store.ExperimentDecision `json:"collision_claims"`
	}
	if err := json.Unmarshal([]byte(activity), &preview); err != nil {
		t.Fatal(err)
	}
	var starts []store.ExperimentActivity
	for _, a := range preview.Canonical {
		if a.Event.Type == store.TypeMatterStarted && a.Event.Subject == n.ID {
			starts = append(starts, a)
		}
	}
	if len(starts) != 2 || starts[0].Origin == starts[1].Origin || starts[0].Event.ID == starts[1].Event.ID || starts[0].Disposition != "integrated" || starts[1].Disposition != "integrated" {
		t.Fatal("independent canonical originals missing", starts)
	}
	// Two disconnected create-once Briefs retain a hard canonical conflict;
	// handling/recheck must never overwrite either host's accepted bytes.
	competing := ca.matter(t, "Competing Briefs")
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	ca.command(t, true, "plumbing", "brief", competing.Locator, "--file", brief)
	other := filepath.Join(t.TempDir(), "other-brief")
	if err := os.WriteFile(other, []byte("B accepted different content"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb.command(t, true, "plumbing", "brief", competing.Locator, "--file", other)
	ca.sync(t, true, "run")
	cb.sync(t, true, "run")
	ca.sync(t, true, "run")
	conflicted := cb.status(t)
	if conflicted.Conflicts != 1 || conflicted.Evaluated <= int64(conflicted.Integrated) {
		t.Fatal("transport confused with reconciliation", conflicted)
	}
	var conflictID string
	for _, d := range conflicted.Decisions {
		if d.Disposition == "conflict" {
			conflictID = d.Source.Actions[0].Event.ID
		}
	}
	if conflictID == "" {
		t.Fatal("canonical conflict missing")
	}
	cb.sync(t, true, "ack", conflictID)
	cb.sync(t, true, "recheck", conflictID)
	cb.sync(t, true, "recheck")
	if now := cb.status(t); now.Conflicts != 1 || len(now.Acknowledged) != 1 {
		t.Fatal("ack/recheck falsely resolved conflict", now)
	}
	if got, err := sb.Content(labCtx, competing.ID, store.KindBrief); err != nil || string(got) != "B accepted different content" {
		t.Fatal("accepted content overwritten", err)
	}
	if err := json.Unmarshal([]byte(cb.sync(t, true, "activity")), &preview); err != nil {
		t.Fatal(err)
	}
	var visible bool
	for _, a := range preview.Canonical {
		if a.Event.ID == conflictID && a.Disposition == "conflict" && a.Acknowledgement != nil {
			visible = true
		}
	}
	if !visible {
		t.Fatal("ordinary canonical conflict hidden")
	}
	// A malicious transport claim combines an authentic native ID with an
	// already-retained foreign ID and forged attribution/group links. Keep its
	// whole collision group inspectable without changing canonical periods.
	if err := json.Unmarshal([]byte(ca.sync(t, true, "activity")), &preview); err != nil {
		t.Fatal(err)
	}
	authentic := preview
	var native, foreign store.ExperimentAction
	for _, d := range ca.status(t).Decisions {
		for _, a := range d.Source.Actions {
			if a.Event.ID == starts[0].Event.ID {
				native = a
			}
			if a.Event.ID == starts[1].Event.ID {
				foreign = a
			}
		}
	}
	if native.Event.ID == "" || foreign.Event.ID == "" || native.Event.ID >= foreign.Event.ID {
		t.Fatal("increasing native/foreign collision fixture required")
	}
	native.Event.Repo = foreign.Event.Repo
	foreign.Event.Actor = store.RoleActor("ForgedForeign")
	foreign.Event.Correlation = native.Event.ID
	foreign.Event.Causation = native.Event.ID
	claim := store.ExperimentSource{Host: "44444444-4444-4444-8444-444444444444", Actions: []store.ExperimentAction{native, foreign}}
	encoded, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	change := store.ExchangeChange{Version: 1, InstallationID: claim.Host, GroupID: native.Event.ID, SourceRepoID: foreign.Event.Repo, Source: string(encoded)}
	fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Owner, map[string]string{"installation_id": claim.Host}, 200)
	for range 2 {
		fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Owner, map[string]any{"changes": []store.ExchangeChange{change}}, 200)
		ca.sync(t, true, "run")
		// Do not let json.Unmarshal reuse the saved baseline's backing slices.
		preview.Canonical = nil
		preview.Periods = nil
		preview.Collisions = nil
		if err := json.Unmarshal([]byte(ca.sync(t, true, "activity")), &preview); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(authentic.Canonical, preview.Canonical) || string(authentic.Periods) != string(preview.Periods) || len(preview.Collisions) != 1 || preview.Collisions[0].Disposition != "conflict" {
			t.Fatal("CLI mixed collision changed canonical work/periods or lost inspection")
		}
	}
	// Unsupported ordinary local work is not falsely declared synchronized.
	ca.command(t, true, "plumbing", "finding", "add", n.Locator, "local finding remains usable")
	if len(ca.status(t).Unsupported) == 0 {
		t.Fatal("unsupported work hidden")
	}
	old := cb.status(t)
	cb.sync(t, true, "unpair")
	cb.sync(t, false, "run")
	cb.matter(t, "While disconnected")
	cb.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo)
	resumed := cb.status(t)
	if resumed.Binding != old.Binding || resumed.Cursor != old.Cursor || resumed.Boundary != old.Boundary || len(resumed.PendingUpload) != 1 {
		t.Fatal("resume reset scope/cursor/identity", resumed)
	}
	f.stop()
	f.start(t)
	cb.sync(t, true, "run")
	ca.sync(t, true, "run")
	// Every command is a new process. Restarting stores/server and then going
	// offline retains readable work and usable local CLI, never startup HTTP.
	if err := sa.Rebuild(labCtx); err != nil {
		t.Fatal(err)
	}
	f.stop()
	cb.sync(t, true, "status")
	cb.sync(t, true, "activity")
	cb.sync(t, true, "run", "--dry-run")
	cb.sync(t, false, "run")
	cb.command(t, true, "plumbing", "refresh")
	generated, err := os.ReadFile(filepath.Join(b.dir, ".wip", "generated", n.Locator, "brief.md"))
	if err != nil || string(generated) != text {
		t.Fatal("ordinary offline generated Brief", err, string(generated))
	}
	cb.matter(t, "Server unavailable ordinary CLI")
	f.start(t)
	cb.sync(t, true, "logout")
	cb.sync(t, false, "repos")
	cb.sync(t, true, "status")
	loggedOut, err := os.ReadFile(filepath.Join(cb.config, "wip", "sync", "credentials.json"))
	if err != nil || !strings.Contains(string(loggedOut), `"Token":""`) {
		t.Fatal("logout must retain only connection metadata", err)
	}
	if got, err := sa.Repo(labCtx, a.repo); err != nil || got.ID != a.repo {
		t.Fatal("native identity changed", err)
	}
	t.Log("real built CLI → Laravel session/CSRF/password confirmation → token → discovery/pair → automatic originals → PostgreSQL feed → inbox/import; offline Brief, overlap, restart, unpair/resume, unsupported and logout controls passed")
}

func syncRowCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'exchange_%' OR name LIKE 'experiment_%' OR name='events') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, table := range tables {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func TestLaravelExplicitSyncCLILostResponsesAndRevocation(t *testing.T) {
	f := startLaravelFixture(t)
	p, s := exchangeHost(t)
	c := syncCLI{p, t.TempDir()}
	var lose atomic.Int32
	var concurrentWrite atomic.Bool
	writeResult := make(chan error, 1)
	target, err := url.Parse(f.base)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		kind := int32(0)
		if strings.HasSuffix(resp.Request.URL.Path, "/origins") {
			kind = 1
		}
		if strings.HasSuffix(resp.Request.URL.Path, "/changes") {
			kind = 2
		}
		if resp.Request.Method == "POST" && resp.StatusCode >= 200 && resp.StatusCode < 300 && kind != 0 && lose.CompareAndSwap(kind, 0) {
			_ = resp.Body.Close()
			if concurrentWrite.Swap(false) {
				cmd := exec.Command(cliBinary(t), "plumbing", "matter", "create", "--title", "During HTTP no writer lock")
				cmd.Dir = p.dir
				cmd.Env = append(os.Environ(), "WIP_DB_PATH="+p.db, "WIP_AS_ROLE=")
				out, err := cmd.CombinedOutput()
				if err != nil {
					writeResult <- fmt.Errorf("concurrent ordinary CLI: %v %s", err, out)
				} else {
					writeResult <- nil
				}
			}
			return fmt.Errorf("injected lost committed response")
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
	if err := os.Chmod(password, 0o644); err != nil {
		t.Fatal(err)
	}
	c.sync(t, false, "login", "--server", server.URL, "--email", "member@example.test", "--password-file", password)
	if err := os.Chmod(password, 0o600); err != nil {
		t.Fatal(err)
	}
	c.sync(t, true, "login", "--server", server.URL, "--email", "member@example.test", "--password-file", password)
	// Real server has committed the origin, but CLI does not see its response.
	// Local setup identity/boundary must already be durable before that HTTP.
	lose.Store(1)
	c.sync(t, false, "pair", "--org", f.Org, "--server-repo", f.Repo)
	if lose.Load() != 0 {
		t.Fatal("origin loss not exercised")
	}
	first := c.status(t)
	if !first.Paired {
		t.Fatal("setup not durable before HTTP")
	}
	c.matter(t, "After lost setup")
	f.stop()
	f.start(t)
	c.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo)
	retry := c.status(t)
	if retry.Binding != first.Binding || retry.Boundary != first.Boundary || len(retry.PendingUpload) != 1 {
		t.Fatal("lost setup retry moved boundary", retry)
	}
	// Exact envelope captured before HTTP; another CLI writes while transport
	// is in flight. Neither that work nor the lost upload may be skipped.
	lose.Store(2)
	concurrentWrite.Store(true)
	c.sync(t, false, "run")
	if lose.Load() != 0 {
		t.Fatal("upload loss not exercised")
	}
	if err := <-writeResult; err != nil {
		t.Fatal(err)
	}
	var stats struct{ Count, Origins int }
	if err := json.Unmarshal(f.php(t, "tests/Support/exchange-fixture.php", "stats"), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Count != 1 || stats.Origins != 1 || len(c.status(t).PendingUpload) != 2 {
		t.Fatal("lost response checkpoint/ack", stats)
	}
	f.stop()
	f.start(t)
	c.sync(t, true, "run")
	after := c.status(t)
	if after.Cursor != 2 || after.Integrated != 2 || len(after.PendingUpload) != 0 || len(after.Retained) != 2 {
		t.Fatal("retry lost/duplicated work", after)
	}
	if err := json.Unmarshal(f.php(t, "tests/Support/exchange-fixture.php", "stats"), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Count != 2 || stats.Origins != 1 {
		t.Fatal("retry duplicate receipt", stats)
	}
	// Origin revocation and tenant membership are enforced on the actual pass,
	// never treated as local setup failures or permission to replay elsewhere.
	fixtureHTTP(t, "DELETE", f.endpoint()+"/origins/"+after.Binding.InstallationID, f.Owner, nil, 200)
	c.sync(t, false, "run")
	c.sync(t, true, "status")
	c.matter(t, "Revoked origin ordinary local work")
	c.sync(t, true, "logout")
	c.sync(t, true, "login", "--server", server.URL, "--email", "member@example.test", "--password-file", password)
	raw, err := os.ReadFile(filepath.Join(c.config, "wip", "sync", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var credentials struct {
		Connections map[string]struct{ Server, Token string }
	}
	if err := json.Unmarshal(raw, &credentials); err != nil {
		t.Fatal(err)
	}
	fixtureHTTP(t, "DELETE", f.base+"/api/token", credentials.Connections["default"].Token, nil, 204)
	if !strings.Contains(c.sync(t, false, "repos"), "401") {
		t.Fatal("revoked token not denied")
	}
	c.sync(t, true, "logout") // an already revoked token is safe to forget
	c.sync(t, true, "login", "--server", server.URL, "--email", "member@example.test", "--password-file", password)
	fixtureHTTP(t, "DELETE", fmt.Sprintf("%s/api/organizations/%s/members/%d", f.base, f.Org, f.MemberID), f.Owner, nil, 204)
	c.sync(t, false, "pair", "--org", f.Org, "--server-repo", f.Repo)
	c.sync(t, false, "run")
	c.sync(t, true, "status")
	if got, err := s.Repo(labCtx, p.repo); err != nil || got.ID != p.repo {
		t.Fatal("revocation changed native identity", err)
	}
	// A caller cannot redirect a saved credential to a different origin.
	if out := c.sync(t, false, "repos", "--server", "https://elsewhere.example"); !strings.Contains(out, "another server") {
		t.Fatal("credential origin mismatch not refused")
	}
	t.Log("real Laravel committed-origin/upload response loss, concurrent ordinary CLI writer, retry after server restart, exact dedup and origin/token/membership revocation passed")
}

func TestLaravelExplicitSyncCLIOfflineInboxRecovery(t *testing.T) {
	f := startLaravelFixture(t)
	a, _ := exchangeHost(t)
	b, _ := exchangeHost(t)
	ca, cb := syncCLI{a, t.TempDir()}, syncCLI{b, t.TempDir()}
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("fixture-password-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, c := range []syncCLI{ca, cb} {
		c.sync(t, true, "login", "--server", f.base, "--email", []string{"owner@example.test", "member@example.test"}[i], "--password-file", password)
		c.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo)
	}
	n := ca.matter(t, "Durable inbox offline recovery")
	ca.sync(t, true, "run")
	// Inject only an importer failure, not source work or cursor data. The
	// receiving CLI must persist the real committed HTTP page before failing.
	db, err := sql.Open("sqlite", b.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TRIGGER fail_import BEFORE INSERT ON experiment_sources BEGIN SELECT RAISE(ABORT,'injected importer failure'); END`); err != nil {
		t.Fatal(err)
	}
	cb.sync(t, false, "run")
	before := cb.status(t)
	if before.Cursor != 1 || before.Downloaded != 1 || before.Evaluated != 0 {
		t.Fatal("receipt was not durable independently of import", before)
	}
	f.stop()
	if _, err := db.Exec(`DROP TRIGGER fail_import`); err != nil {
		t.Fatal(err)
	}
	cb.sync(t, true, "recheck")
	after := cb.status(t)
	if after.Cursor != before.Cursor || after.Downloaded != 1 || after.Evaluated != 1 || after.Integrated != 1 {
		t.Fatal("offline inbox recovery skipped/duplicated receipt", after)
	}
	cb.command(t, true, "plumbing", "refresh")
	if _, err := os.Stat(filepath.Join(b.dir, ".wip", "generated", n.Locator)); err != nil {
		t.Fatal("recovered Matter not readable offline", err)
	}
	// Refresh itself records ordinary render.performed activity, outside this
	// bounded family. Compare recovery against that new local baseline.
	after = cb.status(t)
	cb.sync(t, true, "recheck")
	if !reflect.DeepEqual(after, cb.status(t)) {
		t.Fatal("offline recovery retry added effects/evaluations")
	}
	t.Log("CLI failure after real committed-feed receipt preserved inbox/cursor; fresh offline CLI recheck applied once without server or credential access")
}

func TestLaravelExplicitSyncCLIScopedRecheck(t *testing.T) {
	for _, command := range []string{"recheck", "run"} {
		t.Run(command, func(t *testing.T) {
			f := startLaravelFixture(t)
			p := newLabPair(t)
			ancestor, child := followDependency(t, p)
			if _, err := p.fluke.PairExchange(labCtx, p.repo, "https://not-contacted.example/api/organizations/old/repos/old"); err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct {
				source store.ExperimentSource
				want   string
			}{{child, "pending"}, {ancestor, "integrated"}} {
				decision, err := p.fluke.ExperimentImportBound(labCtx, item.source, p.repo)
				if err != nil || decision.Disposition != item.want {
					t.Fatalf("dependency setup: %+v %v", decision, err)
				}
			}
			if err := p.fluke.UnpairExchange(labCtx, p.repo); err != nil {
				t.Fatal(err)
			}
			before, err := p.fluke.ExchangeStatus(labCtx, p.repo)
			if err != nil || before.Waiting != 1 || before.Active {
				t.Fatal("disconnected Repo needs one waiting child", before, err)
			}
			priorEvents, err := p.fluke.EventsOfSubject(labCtx, p.step1)
			if err != nil {
				t.Fatal(err)
			}
			other := labPair{dir: t.TempDir(), db: p.fluke.Path()}
			if out, err := exec.Command("git", "init", "--quiet", other.dir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			labCLICommand(t, other, true, "init", "--json")
			// An outside symlink remains a valid configuration location. This
			// exercises real token saving, not a blanket symlink prohibition.
			config := filepath.Join(t.TempDir(), "config-alias")
			if err := os.Symlink(t.TempDir(), config); err != nil {
				t.Fatal(err)
			}
			c := syncCLI{other, config}
			password := filepath.Join(t.TempDir(), "password")
			if err := os.WriteFile(password, []byte("fixture-password-123"), 0o600); err != nil {
				t.Fatal(err)
			}
			c.sync(t, true, "login", "--server", f.base, "--email", "owner@example.test", "--password-file", password)
			c.sync(t, true, "pair", "--org", f.Org, "--server-repo", f.Repo)
			if c.status(t).Binding.LocalRepo == p.repo {
				t.Fatal("fixture must use independent Repos in one database")
			}
			if command == "run" {
				c.matter(t, "Only the selected Repo should sync")
			} else {
				f.stop()
			}
			c.sync(t, true, command)
			after, err := p.fluke.ExchangeStatus(labCtx, p.repo)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("selected Repo command changed disconnected Repo decisions", after, err)
			}
			afterEvents, err := p.fluke.EventsOfSubject(labCtx, p.step1)
			if err != nil || !reflect.DeepEqual(priorEvents, afterEvents) {
				t.Fatal("selected Repo command applied another Repo's business effects", err)
			}
			if command == "run" {
				if status := c.status(t); status.Downloaded != 1 || status.Integrated != 1 || len(status.Retained) != 1 {
					t.Fatal("selected Repo did not actually exchange through Laravel", status)
				}
				f.stop()
			}
			selected := c.status(t)
			c.sync(t, true, "recheck", "--repo", p.repo)
			rechecked, err := p.fluke.ExchangeStatus(labCtx, p.repo)
			if err != nil || rechecked.Waiting != 0 || rechecked.Integrated != before.Integrated+1 {
				t.Fatal("explicitly selected Repo did not recover offline", rechecked, err)
			}
			labState(t, p.fluke, p.step1, store.InProgress)
			c.sync(t, true, "recheck", "--repo", p.repo)
			retried, err := p.fluke.ExchangeStatus(labCtx, p.repo)
			if err != nil || !reflect.DeepEqual(rechecked, retried) || !reflect.DeepEqual(selected, c.status(t)) {
				t.Fatal("offline retry changed effects or unrelated Repo", err)
			}
		})
	}
}
