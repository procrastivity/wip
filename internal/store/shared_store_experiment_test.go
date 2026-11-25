//go:build sharedstoreexperiment

package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

var (
	labCLI struct {
		sync.Once
		path string
		err  error
	}
	labCtx = context.Background()
)

func TestMain(m *testing.M) {
	code := m.Run()
	if labCLI.path != "" {
		_ = os.RemoveAll(filepath.Dir(labCLI.path))
	}
	os.Exit(code)
}

func cliBinary(t *testing.T) string {
	t.Helper()
	labCLI.Do(func() {
		dir, err := os.MkdirTemp("", "wip-shared-store-cli-")
		if err != nil {
			labCLI.err = err
			return
		}
		labCLI.path = filepath.Join(dir, "wip")
		cmd := exec.Command("go", "build", "-o", labCLI.path, "../../cmd/wip")
		out, err := cmd.CombinedOutput()
		if err != nil {
			labCLI.err = fmt.Errorf("build CLI: %w: %s", err, out)
		}
	})
	if labCLI.err != nil {
		t.Fatal(labCLI.err)
	}
	return labCLI.path
}

type labPair struct {
	dir, db, repo, matter, step1, step2 string
	hybrid, fluke                       *store.Store
}

func labOpen(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnableSharedStoreExperiment(labCtx); err != nil {
		t.Fatal(err)
	}
	return s
}

func labCLICommand(t *testing.T, p labPair, ok bool, args ...string) string {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "WIP_DB_PATH="+p.db, "WIP_AS_ROLE=", "WIP_EXPERIMENTAL_WIPD_PROFILE=")
	out, err := cmd.CombinedOutput()
	if (err == nil) != ok {
		t.Fatalf("CLI %v: %v: %s", args, err, out)
	}
	return string(out)
}

func newLabPair(t *testing.T) labPair {
	t.Helper()
	p := labPair{dir: t.TempDir()}
	p.db = filepath.Join(t.TempDir(), "hybrid.db")
	cmd := exec.Command("git", "init", "--quiet", p.dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	labCLICommand(t, p, true, "init", "--json")
	// Ordinary CLI work succeeds before there is any experiment/companion.
	labCLICommand(t, p, true, "plumbing", "matter", "create", "--title", "Shared", "--json")
	s, err := store.Open(p.db)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	p.repo = events[0].Repo
	n, err := s.MatterByLocator(labCtx, p.repo, "shared")
	if err != nil {
		t.Fatal(err)
	}
	p.matter = n.ID
	// Exercise real candidate derivation, but never approve/flush a provider.
	if _, err := s.SetTrackerPushLevel(labCtx, p.repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Bind(labCtx, s, store.ActorHuman, p.repo, "shared", "fixture:T-1"); err != nil {
		t.Fatal(err)
	}
	stage, err := writesurface.CreateStage(labCtx, s, store.ActorHuman, p.repo, "shared", "Stage")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		n, err := writesurface.CreateStep(labCtx, s, store.ActorHuman, p.repo, "shared/"+stage.Locator, fmt.Sprintf("Step %d", i+1))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			p.step1 = n.ID
		} else {
			p.step2 = n.ID
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Closed, checkpointed disposable copy preserves native object identities.
	data, err := os.ReadFile(p.db)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "fluke.db")
	if err := os.WriteFile(other, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p.hybrid, p.fluke = labOpen(t, p.db), labOpen(t, other)
	return p
}

func labStart(t *testing.T, p labPair, s *store.Store, host, locator string) store.ExperimentSource {
	t.Helper()
	events, err := writesurface.Start(labCtx, s, store.ActorHuman, p.repo, locator)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.ExperimentExport(labCtx, host, events[0].Correlation)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func labImport(t *testing.T, s *store.Store, source store.ExperimentSource, disposition string) store.ExperimentDecision {
	t.Helper()
	d, err := s.ExperimentImport(labCtx, source)
	if err != nil {
		t.Fatal(err)
	}
	if d.Disposition != disposition {
		t.Fatalf("got %+v, want %s", d, disposition)
	}
	return d
}

func labEvents(t *testing.T, s *store.Store) []store.Event {
	t.Helper()
	events, err := s.Events(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func labState(t *testing.T, s *store.Store, id string, want store.Lifecycle) {
	t.Helper()
	n, err := s.Node(labCtx, id)
	if err != nil || n.Lifecycle != want {
		t.Fatalf("node %s: %+v %v; want %s", id, n, err, want)
	}
}

func labRebuild(t *testing.T, s *store.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	snapshot := func() []string {
		var out []string
		for _, table := range []string{"nodes", "content", "outbox_entries"} {
			rows, err := db.Query("SELECT * FROM " + table + " ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				values := make([]any, len(cols))
				ptr := make([]any, len(cols))
				for i := range values {
					ptr[i] = &values[i]
				}
				if err := rows.Scan(ptr...); err != nil {
					t.Fatal(err)
				}
				b, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, table+string(b))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
		}
		return out
	}
	before := snapshot()
	if err := s.Rebuild(labCtx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("maintained and rebuilt nodes/outbox differ")
	}
}

func TestSharedStoreSameBaselineAndMissingEffect(t *testing.T) {
	for _, both := range []bool{true, false} {
		t.Run(fmt.Sprint("both=", both), func(t *testing.T) {
			p := newLabPair(t)
			h := labStart(t, p, p.hybrid, "Hybrid", "shared")
			var f store.ExperimentSource
			if both {
				f = labStart(t, p, p.fluke, "Fluke", "shared")
			} else {
				// Controlled late receipt: original work happened a day before
				// the receiver's integration clock, not at its native effect time.
				path := p.fluke.Path()
				_ = p.fluke.Close()
				later := time.Now().Add(24 * time.Hour)
				var err error
				p.fluke, err = store.OpenWithClock(path, func() time.Time { return later })
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = p.fluke.Close() })
			}
			before := len(labEvents(t, p.fluke))
			// Self echoes retain local source metadata too; neither is a new action.
			labImport(t, p.hybrid, h, "integrated")
			d := labImport(t, p.fluke, h, "integrated")
			if both {
				labImport(t, p.hybrid, f, "integrated")
				labImport(t, p.fluke, f, "integrated")
				if len(labEvents(t, p.fluke)) != before {
					t.Fatal("equivalent start added transition")
				}
				hybridOutbox, err := p.hybrid.Outbox(labCtx, p.repo)
				if err != nil {
					t.Fatal(err)
				}
				flukeOutbox, err := p.fluke.Outbox(labCtx, p.repo)
				if err != nil {
					t.Fatal(err)
				}
				if len(hybridOutbox) != 1 || len(flukeOutbox) != 1 || hybridOutbox[0].IdempotencyKey == flukeOutbox[0].IdempotencyKey {
					t.Fatal("fixture must demonstrate distinct cross-host tracker keys, not once-only delivery")
				}
			} else {
				events := labEvents(t, p.fluke)
				if len(events) != before+1 || d.Effects[0] == h.Actions[0].Event.ID || d.Effects[0] != events[len(events)-1].ID {
					t.Fatal("missing effect did not get fresh local identity")
				}
				if events[len(events)-1].Actor != store.SystemActor("shared-store-experiment") {
					t.Fatal("import impersonated source actor")
				}
				if _, err := p.fluke.ExperimentExport(labCtx, "Fluke", d.Effects[0]); err == nil {
					t.Fatal("integration effect re-exported")
				}
				t.Logf("source time=%s host=%s; integration time=%s; source ID != effect ID", h.Actions[0].Event.OccurredAt, h.Host, events[len(events)-1].OccurredAt)
			}
			for _, s := range []*store.Store{p.hybrid, p.fluke} {
				count := len(labEvents(t, s))
				path := s.Path()
				_ = s.Close()
				reopened := labOpen(t, path)
				labImport(t, reopened, h, "integrated")
				if both {
					labImport(t, reopened, f, "integrated")
				}
				if len(labEvents(t, reopened)) != count {
					t.Fatal("retry/reopen duplicated effect")
				}
				activities, err := reopened.ExperimentActivities(labCtx)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if both {
					want = 2
				}
				if len(activities) != want {
					t.Fatalf("lost original activity: %+v", activities)
				}
				originals := map[string]store.ExperimentSource{h.Actions[0].Event.ID: h}
				if both {
					originals[f.Actions[0].Event.ID] = f
				}
				for _, activity := range activities {
					original := originals[activity.Source.Actions[0].Event.ID]
					if !reflect.DeepEqual(activity.Source, original) || len(activity.Effects) != 1 {
						t.Fatalf("reopen lost original time/host/group or effect mapping: %+v", activity)
					}
				}
				// Only a native start queues a tracker candidate. When fluke's
				// sole start arrived as an integration effect it projects the
				// work but no delivery authority, so its outbox stays empty.
				wantCandidates := 1
				if !both && s == p.fluke {
					wantCandidates = 0
				}
				outbox, err := reopened.Outbox(labCtx, p.repo)
				if err != nil || len(outbox) != wantCandidates {
					t.Fatalf("outbox = %+v (err %v), want %d native candidates", outbox, err, wantCandidates)
				}
				if wantCandidates == 1 && !strings.Contains(outbox[0].IdempotencyKey, ":state:fixture:T-1") {
					t.Fatalf("expected the native tracker candidate, got %+v", outbox)
				}
				labState(t, reopened, p.matter, store.InProgress)
				if _, err := writesurface.Start(labCtx, reopened, store.ActorHuman, p.repo, "shared"); err == nil {
					t.Fatal("local repeated start no longer refuses")
				}
				labRebuild(t, reopened)
			}
		})
	}
}

func TestSharedStoreCascadeAndUnfriendlyControls(t *testing.T) {
	t.Run("partial overlap", func(t *testing.T) {
		p := newLabPair(t)
		h := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
		f := labStart(t, p, p.fluke, "Fluke", "shared/step-02")
		if len(h.Actions) != 3 || len(f.Actions) != 3 {
			t.Fatal("fixture lacks ancestor cascade")
		}
		for _, pair := range []struct {
			s        *store.Store
			incoming store.ExperimentSource
		}{{p.hybrid, f}, {p.fluke, h}} {
			before := len(labEvents(t, pair.s))
			d := labImport(t, pair.s, pair.incoming, "integrated")
			if len(labEvents(t, pair.s)) != before+1 || len(d.Effects) != 3 {
				t.Fatal("ancestor coalescing lost child or source group")
			}
			labState(t, pair.s, p.step1, store.InProgress)
			labState(t, pair.s, p.step2, store.InProgress)
			labRebuild(t, pair.s)
		}
	})
	for _, control := range []string{"done", "pause-resume", "missing-baseline", "missing-dependency", "policy", "content"} {
		t.Run(control, func(t *testing.T) {
			p := newLabPair(t)
			h := labStart(t, p, p.hybrid, "Hybrid", "shared")
			want := "conflict"
			switch control {
			case "done", "pause-resume", "policy":
				labStart(t, p, p.fluke, "Fluke", "shared")
				if control == "done" {
					if _, err := writesurface.Finish(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
						t.Fatal(err)
					}
				}
				if control == "pause-resume" {
					if _, err := writesurface.Pause(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
						t.Fatal(err)
					}
					if _, err := writesurface.Resume(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
						t.Fatal(err)
					}
				}
				if control == "policy" {
					var tr store.Transition
					_ = json.Unmarshal(h.Actions[0].Event.Payload, &tr)
					tr.TrackerPushLevel = "narrated"
					h.Actions[0].Event.Payload, _ = json.Marshal(tr)
				}
			case "missing-baseline":
				h.Actions[0].Baseline = ""
				want = "pending"
			case "missing-dependency":
				h.Actions[0].Baseline = p.hybrid.NewID()
				want = "pending"
			case "content":
				write := func(s *store.Store, text string) []store.Event {
					events, err := s.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: p.repo}}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
						d, err := tx.ContentDraft(p.matter, store.KindBrief, []byte(text))
						return []store.Draft{d}, err
					})
					if err != nil {
						t.Fatal(err)
					}
					return events
				}
				events := write(p.hybrid, "Hybrid's incompatible brief")
				write(p.fluke, "Fluke's accepted brief")
				var err error
				h, err = p.hybrid.ExperimentExport(labCtx, "Hybrid", events[0].ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := labEvents(t, p.fluke)
			d := labImport(t, p.fluke, h, want)
			if !reflect.DeepEqual(before, labEvents(t, p.fluke)) || !reflect.DeepEqual(d.Source, h) {
				t.Fatal("unresolved input changed local work or lost source")
			}
			if control == "content" {
				content, err := p.fluke.Content(labCtx, p.matter, store.KindBrief)
				if err != nil || string(content) != "Fluke's accepted brief" {
					t.Fatalf("accepted content overwritten: %q %v", content, err)
				}
			}
			labImport(t, p.fluke, h, want)
			labRebuild(t, p.fluke)
		})
	}
}

func TestSharedStoreAtomicRollbackAndCLIIndependence(t *testing.T) {
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	db, err := sql.Open("sqlite", p.fluke.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Abort AFTER ledger and business writes, on the last cascade member.
	_, err = db.Exec(`CREATE TRIGGER experiment_fail BEFORE INSERT ON experiment_actions WHEN NEW.id='` + h.Actions[2].Event.ID + `' BEGIN SELECT RAISE(ABORT, 'injected import failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	before := labEvents(t, p.fluke)
	if _, err := p.fluke.ExperimentImport(labCtx, h); err == nil || !strings.Contains(err.Error(), "injected import failure") {
		t.Fatalf("expected injected failure: %v", err)
	}
	activities, err := p.fluke.ExperimentActivities(labCtx)
	if err != nil || len(activities) != 0 || !reflect.DeepEqual(before, labEvents(t, p.fluke)) {
		t.Fatal("failed import escaped transaction")
	}
	for _, table := range []string{"experiment_sources", "experiment_actions", "experiment_effects", "outbox_entries"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback left %s rows: %d %v", table, count, err)
		}
	}
	labState(t, p.fluke, p.matter, store.Planned)
	// Default CLI binary, separately opened, while the companion handle stays open.
	cli := p
	cli.db = p.fluke.Path()
	labCLICommand(t, cli, true, "plumbing", "matter", "create", "--title", "After failure", "--json")
	if _, err := db.Exec(`DROP TRIGGER experiment_fail`); err != nil {
		t.Fatal(err)
	}
	path := p.fluke.Path()
	_ = p.fluke.Close()
	p.fluke = labOpen(t, path)
	labImport(t, p.fluke, h, "integrated")
	labCLICommand(t, cli, false, "plumbing", "start", "shared", "--json")
	labCLICommand(t, cli, true, "plumbing", "pause", "shared", "--json")
	labCLICommand(t, cli, true, "plumbing", "resume", "shared", "--json")
	labRebuild(t, p.fluke)
	t.Log("default CLI create succeeded with no companion; create after failed import and pause/resume with independently open companion succeeded; repeated start refused")
}

func TestSharedStoreGroupConflictAndSourceIdentity(t *testing.T) {
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	h.Actions[2].Baseline = ""
	before := labEvents(t, p.fluke)
	labImport(t, p.fluke, h, "pending")
	if !reflect.DeepEqual(before, labEvents(t, p.fluke)) {
		t.Fatal("unresolved child partially started ancestors")
	}
	labState(t, p.fluke, p.matter, store.Planned)
	h.Host = "altered origin"
	if _, err := p.fluke.ExperimentImport(labCtx, h); err == nil {
		t.Fatal("source identity accepted different immutable provenance")
	}
	// Other local work is still permitted after this group's pending decision.
	cli := p
	cli.db = p.fluke.Path()
	labCLICommand(t, cli, true, "plumbing", "matter", "create", "--title", "Unrelated", "--json")
}

func TestSharedStoreNativeIdentityCollisionBeforeEcho(t *testing.T) {
	for _, change := range []string{"actor and origin", "origin only", "other subject", "unexported origin"} {
		t.Run(change, func(t *testing.T) {
			p := newLabPair(t)
			events, err := writesurface.Start(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared")
			if err != nil {
				t.Fatal(err)
			}
			history, err := p.hybrid.EventsOfSubject(labCtx, p.matter)
			if err != nil {
				t.Fatal(err)
			}
			forged := store.ExperimentSource{Host: "foreign-origin", Actions: []store.ExperimentAction{{Event: events[0], Baseline: history[len(history)-2].ID}}}
			if change != "unexported origin" {
				if _, err := p.hybrid.ExperimentExport(labCtx, "Hybrid", events[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			if change == "actor and origin" {
				forged.Actions[0].Event.Actor = store.RoleActor("Forged")
			}
			if change == "other subject" {
				forged.Actions[0].Event.Subject = p.step1
			}
			before := labEvents(t, p.hybrid)
			for range 2 {
				d := labImport(t, p.hybrid, forged, "conflict")
				if !reflect.DeepEqual(d.Source, forged) || len(d.Effects) != 0 {
					t.Fatal("collision lost original evidence or acquired effects")
				}
			}
			preview := followPreview(t, p.hybrid, "Hybrid")
			var matching []store.ExperimentActivity
			for _, a := range preview.Activities {
				if a.Event.ID == events[0].ID {
					matching = append(matching, a)
				}
			}
			if len(matching) != 1 || matching[0].Origin != "Hybrid" || !reflect.DeepEqual(matching[0].Event, events[0]) {
				t.Fatalf("collision replaced authentic activity: %+v", matching)
			}
			path := p.hybrid.Path()
			if err := p.hybrid.Close(); err != nil {
				t.Fatal(err)
			}
			p.hybrid = labOpen(t, path)
			genuine, err := p.hybrid.ExperimentExport(labCtx, "Hybrid", events[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				labImport(t, p.hybrid, genuine, "integrated")
			}
			decisions, err := p.hybrid.ExperimentActivities(labCtx)
			if err != nil || len(decisions) != 2 {
				t.Fatalf("genuine echo and durable collision: %+v %v", decisions, err)
			}
			if !reflect.DeepEqual(before, labEvents(t, p.hybrid)) {
				t.Fatal("collision or echo changed native evidence")
			}
		})
	}
}

func TestSharedStoreProvenanceDoesNotAuthorizeRoles(t *testing.T) {
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared")
	h.Actions[0].Event.Actor = store.RoleActor("ForeignBuilder")
	d := labImport(t, p.fluke, h, "integrated")
	events := labEvents(t, p.fluke)
	if events[len(events)-1].ID != d.Effects[0] || events[len(events)-1].Actor != store.SystemActor("shared-store-experiment") {
		t.Fatal("source role impersonated a local spawn")
	}
	activities, err := p.fluke.ExperimentActivitySnapshot(labCtx, "Fluke")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range activities {
		if a.Event.ID == h.Actions[0].Event.ID {
			found = true
			if a.Event.Actor != store.RoleActor("ForeignBuilder") {
				t.Fatal("preview lost original role attribution")
			}
		}
	}
	if !found {
		t.Fatal("preview lost original role activity")
	}
	if _, err := writesurface.CreateMatter(labCtx, p.fluke, store.RoleActor("ForeignBuilder"), p.repo, "Role attempt", "role-attempt"); err == nil || !strings.Contains(err.Error(), "no open spawn") {
		t.Fatalf("provenance authorized an unspawned local role: %v", err)
	}
}

func TestSharedStoreCompanionProcess(t *testing.T) {
	if os.Getenv("WIP_LAB_CHILD") != "1" {
		return
	}
	s, err := store.Open(os.Getenv("WIP_LAB_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	data, err := os.ReadFile(os.Getenv("WIP_LAB_SOURCE"))
	if err != nil {
		t.Fatal(err)
	}
	var source store.ExperimentSource
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExperimentImport(labCtx, source); err != nil {
		t.Fatal(err)
	}
}

func TestSharedStoreConcurrentSubprocesses(t *testing.T) {
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared")
	file := filepath.Join(t.TempDir(), "source.json")
	raw, _ := json.Marshal(h)
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Several independent companion processes race the same durable identity,
	// plus ordinary CLI starts racing the same target. Either local start wins
	// or import wins; both cannot append a second native transition.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var cmd *exec.Cmd
			if i%2 == 0 {
				cmd = exec.Command(os.Args[0], "-test.run=^TestSharedStoreCompanionProcess$")
				cmd.Env = append(os.Environ(), "WIP_LAB_CHILD=1", "WIP_LAB_DB="+p.fluke.Path(), "WIP_LAB_SOURCE="+file)
			} else {
				cmd = exec.Command(cliBinary(t), "plumbing", "start", "shared", "--json")
				cmd.Dir = p.dir
				cmd.Env = append(os.Environ(), "WIP_DB_PATH="+p.fluke.Path(), "WIP_AS_ROLE=")
			}
			out, err := cmd.CombinedOutput()
			if err != nil && (i%2 == 0 || !strings.Contains(string(out), "refusal.invalid-transition")) {
				t.Errorf("process %d: %v %s", i, err, out)
			}
		}(i)
	}
	wg.Wait()
	count := 0
	for _, e := range labEvents(t, p.fluke) {
		if e.Type == store.TypeMatterStarted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent writers produced %d starts", count)
	}
	labImport(t, p.fluke, h, "integrated")
	labRebuild(t, p.fluke)
	// A stale, backwards-clock handle must advance past the persisted floor.
	old, err := store.OpenWithClock(p.fluke.Path(), func() time.Time { return time.Unix(1, 0) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if _, err := writesurface.CreateMatter(labCtx, p.fluke, store.ActorHuman, p.repo, "Newer", "newer"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.CreateMatter(labCtx, old, store.ActorHuman, p.repo, "Old clock", "old-clock"); err != nil {
		t.Fatal(err)
	}
	t.Log("three companion imports and three CLI starts: one native start; long-lived/backwards-clock writer advanced safely")
}

func TestSharedStoreEffectProjectsWorkWithoutDeliveryAuthority(t *testing.T) {
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared")
	// The source store queued its candidate natively — the payload the
	// receiver imports below carries a boundary that would queue here too.
	native, err := p.hybrid.Outbox(labCtx, p.repo)
	if err != nil || len(native) != 1 {
		t.Fatalf("native start outbox = %+v (err %v), want one candidate", native, err)
	}
	d := labImport(t, p.fluke, h, "integrated")
	if len(d.Effects) != 1 || d.Effects[0] == "" {
		t.Fatalf("expected one integrated effect, got %+v", d)
	}
	labState(t, p.fluke, p.matter, store.InProgress)

	// The effect kept the source's push-level snapshot verbatim; the fold
	// guard — not a lost payload — is what kept it from queueing.
	var transition store.Transition
	seen := false
	for _, e := range labEvents(t, p.fluke) {
		if e.ID == d.Effects[0] {
			if e.Actor != store.SystemActor("shared-store-experiment") {
				t.Fatalf("effect actor = %q, want system:shared-store-experiment", e.Actor)
			}
			if err := json.Unmarshal(e.Payload, &transition); err != nil {
				t.Fatal(err)
			}
			seen = true
		}
	}
	if !seen || transition.TrackerPushLevel != store.TrackerPushBoundary {
		t.Fatalf("effect payload = %+v (seen %v), want a boundary transition", transition, seen)
	}
	outbox, err := p.fluke.Outbox(labCtx, p.repo)
	if err != nil || len(outbox) != 0 {
		t.Fatalf("integration effect queued delivery candidates: %+v %v", outbox, err)
	}
	labRebuild(t, p.fluke)
	if outbox, err = p.fluke.Outbox(labCtx, p.repo); err != nil || len(outbox) != 0 {
		t.Fatalf("rebuild resurrected delivery candidates: %+v %v", outbox, err)
	}

	// Native control: the same transition authored on this store queues.
	if _, err := writesurface.CreateMatter(labCtx, p.fluke, store.ActorHuman, p.repo, "Control", "control"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Bind(labCtx, p.fluke, store.ActorHuman, p.repo, "control", "fixture:T-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Start(labCtx, p.fluke, store.ActorHuman, p.repo, "control"); err != nil {
		t.Fatal(err)
	}
	outbox, err = p.fluke.Outbox(labCtx, p.repo)
	if err != nil || len(outbox) != 1 || outbox[0].Kind != "state" || outbox[0].Ref != "fixture:T-2" {
		t.Fatalf("native control outbox = %+v (err %v), want one state candidate for fixture:T-2", outbox, err)
	}
}

func TestSharedStoreProjectionBumpDropsImportedCandidates(t *testing.T) {
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared")
	d := labImport(t, p.fluke, h, "integrated")
	if len(d.Effects) != 1 || d.Effects[0] == "" {
		t.Fatalf("expected one integrated effect, got %+v", d)
	}
	// Simulate a store folded under projection v11: the effect had already
	// queued the candidate the older rules derived, and the recorded version
	// is what the bump exists to invalidate on next open.
	db, err := sql.Open("sqlite", p.fluke.Path())
	if err != nil {
		t.Fatal(err)
	}
	staleID := p.fluke.NewID()
	if _, err := db.Exec(`INSERT INTO outbox_entries
		(id,repo,kind,state,subject,ref,idempotency_key,payload,birth_event,last_event)
		VALUES (?,?,'state','queued',?,?,'stale:imported-candidate','{"disposition":"active"}',?,?)`,
		staleID, p.repo, p.matter, "fixture:T-1", d.Effects[0], d.Effects[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE store_meta SET value='11' WHERE key='projection_version'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := p.fluke.Path()
	if err := p.fluke.Close(); err != nil {
		t.Fatal(err)
	}
	p.fluke = labOpen(t, path)
	outbox, err := p.fluke.Outbox(labCtx, p.repo)
	if err != nil || len(outbox) != 0 {
		t.Fatalf("stale imported candidate survived projection refold: %+v %v", outbox, err)
	}
	labState(t, p.fluke, p.matter, store.InProgress)
}

func TestSharedStoreEffectDelegationRecordsFactWithoutOutbox(t *testing.T) {
	p := newLabPair(t)
	entry, err := writesurface.BacklogAdd(labCtx, p.fluke, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Delegate me", "found mid-flight", "")
	if err != nil {
		t.Fatal(err)
	}
	// backlog.* is outside the import classifier's admitted families, so the
	// guard is exercised directly: the same actor stamp ExperimentImport
	// writes, on the one write path every event takes.
	sourceOutbox := p.fluke.NewID()
	if _, err := p.fluke.Commit(labCtx, store.Request{
		Actor: store.SystemActor("shared-store-experiment"),
		Env:   store.Env{Repo: p.repo},
	}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeBacklogDelegated,
			Subject: entry.ID,
			Payload: store.BacklogDelegated{Outbox: sourceOutbox, IdempotencyKey: "backlog:" + entry.ID},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := p.fluke.BacklogEntry(labCtx, p.repo, entry.ID)
	if err != nil || !found || got.State != "delegated" || got.Outbox != sourceOutbox {
		t.Fatalf("delegated entry = %+v (found %v, err %v), want delegated naming %s", got, found, err, sourceOutbox)
	}
	outbox, err := p.fluke.Outbox(labCtx, p.repo)
	if err != nil || len(outbox) != 0 {
		t.Fatalf("effect delegation queued delivery: %+v %v", outbox, err)
	}
	active, err := p.fluke.ActiveBacklog(labCtx, p.repo)
	if err != nil || len(active) != 0 {
		t.Fatalf("dangling delegation stayed active: %+v %v", active, err)
	}
	labRebuild(t, p.fluke)
	got, found, err = p.fluke.BacklogEntry(labCtx, p.repo, entry.ID)
	if err != nil || !found || got.State != "delegated" || got.Outbox != sourceOutbox {
		t.Fatalf("rebuild lost the delegation fact: %+v (found %v, err %v)", got, found, err)
	}
	if outbox, err = p.fluke.Outbox(labCtx, p.repo); err != nil || len(outbox) != 0 {
		t.Fatalf("rebuild resurrected delivery: %+v %v", outbox, err)
	}

	// Native control: a human delegation on this store queues the create.
	native, err := writesurface.BacklogAdd(labCtx, p.fluke, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Native delegate", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.BacklogDelegate(labCtx, p.fluke, store.ActorHuman, p.repo, native.ID); err != nil {
		t.Fatal(err)
	}
	outbox, err = p.fluke.Outbox(labCtx, p.repo)
	if err != nil || len(outbox) != 1 || outbox[0].Kind != "create" {
		t.Fatalf("native delegation outbox = %+v (err %v), want one create", outbox, err)
	}
}

// labGroup commits drafts as one chain on s and exports the resulting source
// group. Each draft causally follows its predecessor so the export is a real
// ordered group rather than unrelated origins sharing a correlation.
func labGroup(t *testing.T, s *store.Store, host, repo string, decide func(*store.Tx) ([]store.Draft, error)) store.ExperimentSource {
	t.Helper()
	events, err := s.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		drafts, err := decide(tx)
		if err != nil {
			return nil, err
		}
		for i := range drafts {
			if i > 0 {
				drafts[i].Cause = i - 1
			}
		}
		return drafts, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.ExperimentExport(labCtx, host, events[0].Correlation)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func labImportBound(t *testing.T, s *store.Store, source store.ExperimentSource, repo, disposition string) store.ExperimentDecision {
	t.Helper()
	d, err := s.ExperimentImportBound(labCtx, source, repo)
	if err != nil {
		t.Fatal(err)
	}
	if d.Disposition != disposition {
		t.Fatalf("got %+v, want %s", d, disposition)
	}
	return d
}

// A Stage and Step may be non-origin members of one group: their NodeBirth
// payloads name parents by shared identity, and classification sees the Matter
// and Stage effects earlier members just folded into candidate state.
func TestSharedStorePortableBirthGroup(t *testing.T) {
	p := newLabPair(t)
	var mid, sid, tid string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		mid, sid, tid = tx.NewID(), tx.NewID(), tx.NewID()
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: mid, Payload: store.NodeBirth{Title: "Portable", Locator: "portable", SortKey: 0}},
			{Type: store.TypeStageCreated, Subject: sid, Payload: store.NodeBirth{Title: "Phase", Locator: "phase", Parent: mid, SortKey: 0}},
			{Type: store.TypeStepCreated, Subject: tid, Payload: store.NodeBirth{Title: "Do", Locator: "do-01", Parent: sid, SortKey: 0}},
		}, nil
	})
	before := len(labEvents(t, p.fluke))
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 3 {
		t.Fatalf("effects=%v", d.Effects)
	}
	m, err := p.fluke.Node(labCtx, mid)
	if err != nil || m.Kind != store.ScaleMatter || m.Locator != "portable" || m.Parent != "" || m.Matter != mid || m.Lifecycle != store.Planned {
		t.Fatalf("matter = %+v %v", m, err)
	}
	sn, err := p.fluke.Node(labCtx, sid)
	if err != nil || sn.Kind != store.ScaleStage || sn.Parent != mid || sn.Matter != mid || sn.Lifecycle != store.Planned {
		t.Fatalf("stage = %+v %v", sn, err)
	}
	tn, err := p.fluke.Node(labCtx, tid)
	if err != nil || tn.Kind != store.ScaleStep || tn.Parent != sid || tn.Matter != mid || tn.Lifecycle != store.Planned {
		t.Fatalf("step = %+v %v", tn, err)
	}
	events := labEvents(t, p.fluke)
	if len(events) != before+3 {
		t.Fatalf("native count %d -> %d", before, len(events))
	}
	for i, effect := range d.Effects {
		ev := events[before+i]
		if ev.ID != effect || ev.Type != source.Actions[i].Event.Type || ev.Correlation != d.Effects[0] || (i > 0 && ev.Causation != d.Effects[i-1]) {
			t.Fatalf("effect %d = %+v, want aligned chained fold of %s", i, ev, source.Actions[i].Event.Type)
		}
	}
	labRebuild(t, p.fluke)
	// An exact retry returns the retained decision without new effects.
	again := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if !reflect.DeepEqual(d, again) || len(labEvents(t, p.fluke)) != before+3 {
		t.Fatal("exact retry was not inert", again)
	}
	// The verified self-echo on the origin store maps back to native ids.
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, source, p.repo, "integrated")
	if len(echo.Effects) != 3 || len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("verified echo was not inert", echo)
	}
	for i, effect := range echo.Effects {
		if effect != source.Actions[i].Event.ID {
			t.Fatalf("echo effect %d = %s, want native %s", i, effect, source.Actions[i].Event.ID)
		}
	}
}

// Dependent members see same-group candidate state end to end: the Brief's
// baseline is the Matter birth and the start's baseline is the Brief, both
// resolved through the ledger rows written earlier in the same pass.
func TestSharedStorePortableDependentGroup(t *testing.T) {
	p := newLabPair(t)
	var mid string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		mid = tx.NewID()
		brief, err := tx.ContentDraft(mid, store.KindBrief, []byte("portable brief"))
		if err != nil {
			return nil, err
		}
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: mid, Payload: store.NodeBirth{Title: "Chained", Locator: "chained", SortKey: 0}},
			brief,
			{Type: store.TypeMatterStarted, Subject: mid, Payload: store.Transition{From: store.Planned, To: store.InProgress}},
		}, nil
	})
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 3 {
		t.Fatalf("effects=%v", d.Effects)
	}
	labState(t, p.fluke, mid, store.InProgress)
	content, err := p.fluke.Content(labCtx, mid, store.KindBrief)
	if err != nil || string(content) != "portable brief" {
		t.Fatalf("brief = %q %v", content, err)
	}
	history, err := p.fluke.EventsOfSubject(labCtx, mid)
	if err != nil || len(history) != 3 {
		t.Fatalf("subject history=%v %v", history, err)
	}
	for i, effect := range d.Effects {
		if history[i].ID != effect || history[i].Type != source.Actions[i].Event.Type {
			t.Fatalf("history[%d]=%+v, want folded effect %s", i, history[i], effect)
		}
	}
	labRebuild(t, p.fluke)
}

// A birth whose parent has not arrived waits with the whole group; once the
// parent group integrates, explicit recheck folds the children against the
// now-live parent and the same-group stage.
func TestSharedStorePortablePendingParent(t *testing.T) {
	p := newLabPair(t)
	var mid string
	ancestor := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		mid = tx.NewID()
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: mid, Payload: store.NodeBirth{Title: "Root", Locator: "root", SortKey: 0}},
		}, nil
	})
	var sid, tid string
	child := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		sid, tid = tx.NewID(), tx.NewID()
		return []store.Draft{
			{Type: store.TypeStageCreated, Subject: sid, Payload: store.NodeBirth{Title: "Phase", Locator: "phase", Parent: mid, SortKey: 0}},
			{Type: store.TypeStepCreated, Subject: tid, Payload: store.NodeBirth{Title: "Do", Locator: "do-01", Parent: sid, SortKey: 0}},
		}, nil
	})
	before := len(labEvents(t, p.fluke))
	pending := labImportBound(t, p.fluke, child, p.repo, "pending")
	if len(pending.Effects) != 0 || len(labEvents(t, p.fluke)) != before || !strings.Contains(pending.Reason, "awaiting parent") {
		t.Fatalf("pending group wrote effects or lost its reason: %+v", pending)
	}
	if _, err := p.fluke.Node(labCtx, sid); err == nil {
		t.Fatal("pending parent produced a node")
	}
	// Redelivery never reconsiders; only explicit recheck does.
	labImportBound(t, p.fluke, child, p.repo, "pending")
	if len(labEvents(t, p.fluke)) != before {
		t.Fatal("redelivery applied pending work")
	}
	labImportBound(t, p.fluke, ancestor, p.repo, "integrated")
	integrated := followRecheck(t, p.fluke, child, "integrated")
	if len(integrated.Effects) != 2 {
		t.Fatalf("recheck effects=%v", integrated.Effects)
	}
	sn, err := p.fluke.Node(labCtx, sid)
	if err != nil || sn.Parent != mid || sn.Matter != mid {
		t.Fatalf("stage after recheck = %+v %v", sn, err)
	}
	tn, err := p.fluke.Node(labCtx, tid)
	if err != nil || tn.Parent != sid || tn.Matter != mid {
		t.Fatalf("step after recheck = %+v %v", tn, err)
	}
	followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
	labRebuild(t, p.fluke)
}

// One conflicting member condemns the whole group: earlier members' effects
// and ledger rows roll back to the savepoint, every action stays retained with
// no effect, and the decision is an inspectable conflict — never a merge. The
// target already occupies the second Stage's address with its own node, so the
// source group is valid on its origin yet incompatible here.
func TestSharedStorePortableConflictAtomic(t *testing.T) {
	p := newLabPair(t)
	if _, err := writesurface.CreateStage(labCtx, p.fluke, store.ActorHuman, p.repo, "shared", "Taken"); err != nil {
		t.Fatal(err)
	}
	var s1, s2 string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		s1, s2 = tx.NewID(), tx.NewID()
		return []store.Draft{
			{Type: store.TypeStageCreated, Subject: s1, Payload: store.NodeBirth{Title: "Free", Locator: "free", Parent: p.matter, SortKey: 0}},
			{Type: store.TypeStageCreated, Subject: s2, Payload: store.NodeBirth{Title: "Taken", Locator: "taken", Parent: p.matter, SortKey: 0}},
		}, nil
	})
	before := len(labEvents(t, p.fluke))
	d := labImportBound(t, p.fluke, source, p.repo, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatalf("conflicting member left partial effects: %+v", d)
	}
	for _, id := range []string{s1, s2} {
		if _, err := p.fluke.Node(labCtx, id); err == nil {
			t.Fatalf("rolled-back birth %s still projected", id)
		}
	}
	db, err := sql.Open("sqlite", p.fluke.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var actions, effected int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(a.effect IS NOT NULL),0) FROM experiment_actions a JOIN experiment_sources s ON s.id=a.source_group WHERE json_extract(s.source,'$.Actions[0].Event.ID')=?`, source.Actions[0].Event.ID).Scan(&actions, &effected); err != nil {
		t.Fatal(err)
	}
	if actions != 2 || effected != 0 {
		t.Fatalf("ledger retained %d actions, %d effected; want 2/0", actions, effected)
	}
	followRecheck(t, p.fluke, source, "conflict")
	followHistory(t, p.fluke, source.Actions[0].Event.ID, 1)
}

// A Matter's locator is unique per Repo, not per Matter — the (matter,
// locator) index cannot see across Matters, so the source may legally carry
// two same-slug Matters. The bounded import still refuses the second as an
// occupied repo address and keeps the whole group non-integrated.
func TestSharedStorePortableMatterLocatorScope(t *testing.T) {
	p := newLabPair(t)
	var m1, m2 string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		m1, m2 = tx.NewID(), tx.NewID()
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: m1, Payload: store.NodeBirth{Title: "One", Locator: "dup", SortKey: 0}},
			{Type: store.TypeMatterCreated, Subject: m2, Payload: store.NodeBirth{Title: "Two", Locator: "dup", SortKey: 0}},
		}, nil
	})
	before := len(labEvents(t, p.fluke))
	d := labImportBound(t, p.fluke, source, p.repo, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatalf("same-slug Matters merged or partially applied: %+v", d)
	}
	for _, id := range []string{m1, m2} {
		if _, err := p.fluke.Node(labCtx, id); err == nil {
			t.Fatalf("rolled-back matter %s still projected", id)
		}
	}
}

// Births live only in the bounded slice: the unscoped lab import keeps the
// historical conflict rather than guessing a target.
func TestSharedStorePortableBirthRequiresBoundTarget(t *testing.T) {
	p := newLabPair(t)
	var mid, sid string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		mid, sid = tx.NewID(), tx.NewID()
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: mid, Payload: store.NodeBirth{Title: "Scoped", Locator: "scoped", SortKey: 0}},
			{Type: store.TypeStageCreated, Subject: sid, Payload: store.NodeBirth{Title: "Phase", Locator: "phase", Parent: mid, SortKey: 0}},
		}, nil
	})
	before := len(labEvents(t, p.fluke))
	d := labImport(t, p.fluke, source, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatal("unbound import applied bounded births", d)
	}
}
