//go:build sharedstoreexperiment

package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// labExportAfter runs one verb on s, then exports the committed group that
// produced subject's newest event of kind under host.
func labExportAfter(t *testing.T, s *store.Store, host, subject, kind string, verb func() error) store.ExperimentSource {
	t.Helper()
	if err := verb(); err != nil {
		t.Fatal(err)
	}
	source, err := s.ExperimentExport(labCtx, host, sourceGroup(t, s, subject, kind))
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// labLifecycle runs one lifecycle verb on src, exports its committed group
// under host and imports it bound into dst, requiring the want disposition.
func labLifecycle(t *testing.T, p labPair, src, dst *store.Store, host, subject, kind, want string, verb func() error) store.ExperimentDecision {
	t.Helper()
	return labImportBound(t, dst, labExportAfter(t, src, host, subject, kind, verb), p.repo, want)
}

// labLifecycleUnbound is labLifecycle through the legacy unscoped import;
// baseline correspondence only resolves when the whole chain was imported
// unbound too, so it gets its own consistent fixture below.
func labLifecycleUnbound(t *testing.T, p labPair, src, dst *store.Store, host, subject, kind, want string, verb func() error) store.ExperimentDecision {
	t.Helper()
	return labImport(t, dst, labExportAfter(t, src, host, subject, kind, verb), want)
}

// labLifecycleState asserts dst's node lifecycle and, when payload is non-nil,
// that its newest subject event carries exactly that transition's ends and
// reason.
func labLifecycleState(t *testing.T, dst *store.Store, subject string, want store.Lifecycle, payload *store.Transition) {
	t.Helper()
	labState(t, dst, subject, want)
	if payload == nil {
		return
	}
	history, err := dst.EventsOfSubject(labCtx, subject)
	if err != nil || len(history) == 0 {
		t.Fatalf("subject history: %v", err)
	}
	var got store.Transition
	if err := json.Unmarshal(history[len(history)-1].Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.From != payload.From || got.To != payload.To || got.Reason != payload.Reason {
		t.Fatalf("last transition = %+v, want %+v", got, *payload)
	}
}

func labStageID(t *testing.T, s *store.Store, repo string) string {
	t.Helper()
	n, err := writesurface.ResolveNode(labCtx, s.View, repo, "shared/stage")
	if err != nil {
		t.Fatal(err)
	}
	return n.ID
}

func labVerbPause(s *store.Store, repo, locator string) func() error {
	return func() error { _, err := writesurface.Pause(labCtx, s, store.ActorHuman, repo, locator); return err }
}

func labVerbResume(s *store.Store, repo, locator string) func() error {
	return func() error { _, err := writesurface.Resume(labCtx, s, store.ActorHuman, repo, locator); return err }
}

func labVerbFinish(s *store.Store, repo, locator string) func() error {
	return func() error { _, err := writesurface.Finish(labCtx, s, store.ActorHuman, repo, locator); return err }
}

func labVerbCancel(s *store.Store, repo, locator, reason string) func() error {
	return func() error {
		_, err := writesurface.Cancel(labCtx, s, store.ActorHuman, repo, locator, reason)
		return err
	}
}

// Every lifecycle verb at every scale round-trips through the portable slice:
// pause/resume/finish/cancel on Step, Stage and Matter — including cancel's
// reason. The sealing Matter finish, whose group also carries the
// non-portable batch.swept, is covered end to end in
// TestSharedStoreLifecycleExchangeWire.
func TestSharedStoreLifecycleMatrix(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)
	// fluke is a native copy: every node exists Planned under the same IDs.
	// The start cascade gives every later verb its required From state.
	start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	labImportBound(t, p.fluke, start, p.repo, "integrated")
	outboxBefore, err := p.fluke.Outbox(labCtx, p.repo)
	if err != nil {
		t.Fatal(err)
	}

	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.step1, store.TypeStepPaused, "integrated",
		labVerbPause(p.hybrid, p.repo, "shared/step-01"))
	labLifecycleState(t, p.fluke, p.step1, store.Paused, &store.Transition{From: store.InProgress, To: store.Paused})
	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.step1, store.TypeStepResumed, "integrated",
		labVerbResume(p.hybrid, p.repo, "shared/step-01"))
	labLifecycleState(t, p.fluke, p.step1, store.InProgress, &store.Transition{From: store.Paused, To: store.InProgress})

	// Step-02 joins in-progress, then cancels with its reason preserved.
	step2Start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-02")
	labImportBound(t, p.fluke, step2Start, p.repo, "integrated")
	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.step2, store.TypeStepCanceled, "integrated",
		labVerbCancel(p.hybrid, p.repo, "shared/step-02", "descoped"))
	labLifecycleState(t, p.fluke, p.step2, store.Canceled, &store.Transition{From: store.InProgress, To: store.Canceled, Reason: "descoped"})

	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.step1, store.TypeStepFinished, "integrated",
		labVerbFinish(p.hybrid, p.repo, "shared/step-01"))
	labLifecycleState(t, p.fluke, p.step1, store.Done, &store.Transition{From: store.InProgress, To: store.Done})

	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", stage, store.TypeStagePaused, "integrated",
		labVerbPause(p.hybrid, p.repo, "shared/stage"))
	labLifecycleState(t, p.fluke, stage, store.Paused, &store.Transition{From: store.InProgress, To: store.Paused})
	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", stage, store.TypeStageResumed, "integrated",
		labVerbResume(p.hybrid, p.repo, "shared/stage"))
	labLifecycleState(t, p.fluke, stage, store.InProgress, &store.Transition{From: store.Paused, To: store.InProgress})
	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", stage, store.TypeStageFinished, "integrated",
		labVerbFinish(p.hybrid, p.repo, "shared/stage"))
	labLifecycleState(t, p.fluke, stage, store.Done, &store.Transition{From: store.InProgress, To: store.Done})

	// Matter finish is the last matrix row; the sealing variant — whose group
	// also carries the non-portable batch.swept — is covered end to end through
	// the real exchange path in TestSharedStoreLifecycleExchangeWire.
	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.matter, store.TypeMatterFinished, "integrated",
		labVerbFinish(p.hybrid, p.repo, "shared"))
	labLifecycleState(t, p.fluke, p.matter, store.Done, &store.Transition{From: store.InProgress, To: store.Done})

	// Integration effects queue no delivery candidates, and rebuild is stable.
	outboxAfter, err := p.fluke.Outbox(labCtx, p.repo)
	if err != nil || !reflect.DeepEqual(outboxBefore, outboxAfter) {
		t.Fatalf("lifecycle effects queued candidates: %+v -> %+v (%v)", outboxBefore, outboxAfter, err)
	}
	labRebuild(t, p.fluke)
	labRebuild(t, p.hybrid)
}

// The unbound (target=="") path admits the same lifecycle edges: with the
// whole causal chain imported unbound, pause/resume/finish/cancel integrate
// identically — baseline correspondence is scope-consistent end to end.
func TestSharedStoreLifecycleUnbound(t *testing.T) {
	p := newLabPair(t)
	start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	labImport(t, p.fluke, start, "integrated")
	labLifecycleUnbound(t, p, p.hybrid, p.fluke, "Hybrid", p.step1, store.TypeStepPaused, "integrated",
		labVerbPause(p.hybrid, p.repo, "shared/step-01"))
	labLifecycleUnbound(t, p, p.hybrid, p.fluke, "Hybrid", p.step1, store.TypeStepResumed, "integrated",
		labVerbResume(p.hybrid, p.repo, "shared/step-01"))
	step2Start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-02")
	labImport(t, p.fluke, step2Start, "integrated")
	labLifecycleUnbound(t, p, p.hybrid, p.fluke, "Hybrid", p.step2, store.TypeStepCanceled, "integrated",
		labVerbCancel(p.hybrid, p.repo, "shared/step-02", "unbound-descoped"))
	labLifecycleUnbound(t, p, p.hybrid, p.fluke, "Hybrid", p.step1, store.TypeStepFinished, "integrated",
		labVerbFinish(p.hybrid, p.repo, "shared/step-01"))
	labLifecycleState(t, p.fluke, p.step1, store.Done, &store.Transition{From: store.InProgress, To: store.Done})
	labLifecycleState(t, p.fluke, p.step2, store.Canceled, &store.Transition{From: store.InProgress, To: store.Canceled, Reason: "unbound-descoped"})
	labRebuild(t, p.fluke)
}

// A lifecycle group that outruns its prerequisites waits as pending — never
// wedges the import — and integrates on recheck once the missing evidence
// arrives. A transition for a node that does not exist locally pends the same
// way until its birth and start land.
func TestSharedStoreLifecycleOrderingPending(t *testing.T) {
	p := newLabPair(t)
	// step-01: start then finish on hybrid, but only the finish is delivered
	// first — its baseline (the start) has not been represented locally.
	start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	finishStep := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepFinished, labVerbFinish(p.hybrid, p.repo, "shared/step-01"))
	d := labImportBound(t, p.fluke, finishStep, p.repo, "pending")
	if d.Reason != "unknown baseline dependency" {
		t.Fatalf("finish-before-start reason = %q", d.Reason)
	}
	labState(t, p.fluke, p.step1, store.Planned)
	labImportBound(t, p.fluke, start, p.repo, "integrated")
	labState(t, p.fluke, p.step1, store.InProgress)
	followRecheck(t, p.fluke, finishStep, "integrated")
	labState(t, p.fluke, p.step1, store.Done)

	// A brand-new Matter driven start->finish on hybrid: the finish alone
	// pends on a missing paired object, then integrates once birth and start
	// have been represented — in causal order, through recheck.
	late, err := writesurface.CreateMatter(labCtx, p.hybrid, store.ActorHuman, p.repo, "Late", "late")
	if err != nil {
		t.Fatal(err)
	}
	birthSource, err := p.hybrid.ExperimentExport(labCtx, "Hybrid", sourceGroup(t, p.hybrid, late.ID, store.TypeMatterCreated))
	if err != nil {
		t.Fatal(err)
	}
	lateStartEvents, err := writesurface.Start(labCtx, p.hybrid, store.ActorHuman, p.repo, "late")
	if err != nil {
		t.Fatal(err)
	}
	startSource, err := p.hybrid.ExperimentExport(labCtx, "Hybrid", lateStartEvents[0].Correlation)
	if err != nil {
		t.Fatal(err)
	}
	lateFinish := labExportAfter(t, p.hybrid, "Hybrid", late.ID, store.TypeMatterFinished, labVerbFinish(p.hybrid, p.repo, "late"))
	d = labImportBound(t, p.fluke, lateFinish, p.repo, "pending")
	if d.Reason != "missing live paired object" {
		t.Fatalf("finish-without-node reason = %q", d.Reason)
	}
	labImportBound(t, p.fluke, birthSource, p.repo, "integrated")
	labImportBound(t, p.fluke, startSource, p.repo, "integrated")
	labState(t, p.fluke, late.ID, store.InProgress)
	followRecheck(t, p.fluke, lateFinish, "integrated")
	labState(t, p.fluke, late.ID, store.Done)
	labRebuild(t, p.fluke)
}

// done and canceled are terminal: a start that arrives after the finish
// conflicts atomically, a locally finished node refuses a later remote pause,
// and a transition whose causal predecessor conflicted pends rather than
// inventing a baseline.
func TestSharedStoreLifecycleTerminal(t *testing.T) {
	p := newLabPair(t)
	// Pristine copies of the all-Planned fixture supply independent lifecycle
	// opinions for nodes fluke has already terminated: each Start cascades
	// matter+stage+step in one group, so the ancestor members are same-group
	// subjects and only the step member meets fluke's terminal state.
	data, err := os.ReadFile(p.db)
	if err != nil {
		t.Fatal(err)
	}
	pristine := func(name string) *store.Store {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return labOpen(t, path)
	}

	start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	labImportBound(t, p.fluke, start, p.repo, "integrated")

	// Hybrid pauses then resumes step-01; those sources are held back while
	// fluke finishes the same step on its own — genuine divergence.
	pauseStep := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepPaused, labVerbPause(p.hybrid, p.repo, "shared/step-01"))
	resumeStep := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepResumed, labVerbResume(p.hybrid, p.repo, "shared/step-01"))
	if _, err := writesurface.Finish(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/step-01"); err != nil {
		t.Fatal(err)
	}
	labState(t, p.fluke, p.step1, store.Done)

	// The held pause conflicts against done — terminal state never reopens —
	// and the resume downstream of the conflicted pause pends on its missing
	// baseline rather than claiming one.
	labImportBound(t, p.fluke, pauseStep, p.repo, "conflict")
	labState(t, p.fluke, p.step1, store.Done)
	d := labImportBound(t, p.fluke, resumeStep, p.repo, "pending")
	if d.Reason != "missing baseline evidence" {
		t.Fatalf("resume-after-conflict reason = %q", d.Reason)
	}

	// step-02 runs its whole remote lifecycle here: started, then canceled —
	// canceled is terminal the same way done is.
	step2Start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-02")
	labImportBound(t, p.fluke, step2Start, p.repo, "integrated")
	labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.step2, store.TypeStepCanceled, "integrated",
		labVerbCancel(p.hybrid, p.repo, "shared/step-02", "descoped"))
	labState(t, p.fluke, p.step2, store.Canceled)

	// Each pristine copy starts one terminated step: the cascade's ancestor
	// members are idempotent echoes of fluke's own start effects, and the
	// step member conflicts against done/canceled — terminal never reopens.
	for i, subject := range []string{p.step1, p.step2} {
		third := pristine(fmt.Sprintf("third-%d.db", i))
		n, err := third.Node(labCtx, subject)
		if err != nil {
			t.Fatal(err)
		}
		events, err := writesurface.Start(labCtx, third, store.ActorHuman, p.repo, "shared/"+n.Locator)
		if err != nil {
			t.Fatal(err)
		}
		source, err := third.ExperimentExport(labCtx, "Third", events[0].Correlation)
		if err != nil {
			t.Fatal(err)
		}
		if len(source.Actions) != 3 {
			t.Fatalf("pristine start did not cascade all three scales: %+v", source.Actions)
		}
		before := len(labEvents(t, p.fluke))
		d := labImportBound(t, p.fluke, source, p.repo, "conflict")
		if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
			t.Fatalf("start on terminal node produced effects: %+v", d)
		}
	}
	labState(t, p.fluke, p.step1, store.Done)
	labState(t, p.fluke, p.step2, store.Canceled)
	labRebuild(t, p.fluke)
}

// The widened whitelist is what ExchangeStatus selects automatically: every
// lifecycle group is pending upload without being named, the receiver folds
// each against its own repo binding, and nothing unsupported leaks through.
func TestSharedStoreLifecycleExchangeWire(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	if _, err := sa.PairExchange(labCtx, a.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.PairExchange(labCtx, b.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	pass := func() {
		t.Helper()
		if _, err := sa.RunExchange(labCtx, ca, a.repo); err != nil {
			t.Fatal(err)
		}
		if _, err := sb.RunExchange(labCtx, cb, b.repo); err != nil {
			t.Fatal(err)
		}
	}
	// pending names the group ExchangeStatus auto-selects for the next pass.
	pending := func(subject, kind string) string {
		t.Helper()
		group := sourceGroup(t, sa, subject, kind)
		status, err := sa.ExchangeStatus(labCtx, a.repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range status.PendingUpload {
			if g == group {
				return group
			}
		}
		t.Fatalf("%s group %s missing from PendingUpload %+v", kind, group, status.PendingUpload)
		return ""
	}
	sync := func(subject, kind string, verb func() error, want store.Lifecycle, payload *store.Transition) {
		t.Helper()
		if err := verb(); err != nil {
			t.Fatal(err)
		}
		group := pending(subject, kind)
		pass()
		sourceDecision(t, sb, group, "integrated")
		labLifecycleState(t, sb, subject, want, payload)
	}

	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Wire", "wire")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, "wire", "Stage")
	if err != nil {
		t.Fatal(err)
	}
	step, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo, "wire/"+stage.Locator, "Step")
	if err != nil {
		t.Fatal(err)
	}
	pass()
	// Births arrive bound to sb's own repo under shared node identities.
	if _, err := sb.Node(labCtx, step.ID); err != nil {
		t.Fatal("births did not arrive", err)
	}

	stagePath := "wire/" + stage.Locator
	stepPath := stagePath + "/" + step.Locator
	started, err := writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, stepPath)
	if err != nil {
		t.Fatal(err)
	}
	pass()
	sourceDecision(t, sb, started[0].Correlation, "integrated")
	labState(t, sb, step.ID, store.InProgress)
	labState(t, sb, stage.ID, store.InProgress)
	labState(t, sb, matter.ID, store.InProgress)

	sync(step.ID, store.TypeStepPaused, labVerbPause(sa, a.repo, stepPath), store.Paused,
		&store.Transition{From: store.InProgress, To: store.Paused})
	sync(step.ID, store.TypeStepResumed, labVerbResume(sa, a.repo, stepPath), store.InProgress,
		&store.Transition{From: store.Paused, To: store.InProgress})
	sync(step.ID, store.TypeStepFinished, labVerbFinish(sa, a.repo, stepPath), store.Done,
		&store.Transition{From: store.InProgress, To: store.Done})
	sync(stage.ID, store.TypeStageCanceled, labVerbCancel(sa, a.repo, stagePath, "wire-descoped"), store.Canceled,
		&store.Transition{From: store.InProgress, To: store.Canceled, Reason: "wire-descoped"})

	// The sealing Matter finish commits [matter.finished, batch.swept]: only
	// the lifecycle member is portable, so the wire carries an identified
	// derivative and the receiver folds exactly one effect — never the
	// execution-scoped sweep. Batch events need the repo's full tier context.
	clones, err := sa.ClonesOfRepo(labCtx, a.repo)
	if err != nil || len(clones) == 0 {
		t.Fatalf("host repo has no clone: %+v %v", clones, err)
	}
	worktrees, err := sa.WorktreesOfClone(labCtx, clones[0].ID)
	if err != nil || len(worktrees) == 0 {
		t.Fatalf("host clone has no worktree: %+v %v", worktrees, err)
	}
	tier := store.Env{Repo: a.repo, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	if _, err := writesurface.CreateAnonymousBatch(labCtx, sa, store.ActorHuman, tier, matter.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.FinishWithEnv(labCtx, sa, store.ActorHuman, tier, "wire"); err != nil {
		t.Fatal(err)
	}
	finishGroup := pending(matter.ID, store.TypeMatterFinished)
	pass()
	d := sourceDecision(t, sb, finishGroup, "integrated")
	if d.Source.Derivative == nil || d.Source.Derivative.Parent != finishGroup || len(d.Effects) != 1 {
		t.Fatalf("sealing finish did not arrive as a one-effect derivative: %+v", d)
	}
	if len(d.Source.Derivative.Omitted) != 0 {
		t.Fatal("execution-scoped sweep evidence crossed the wire")
	}
	labLifecycleState(t, sb, matter.ID, store.Done, &store.Transition{From: store.InProgress, To: store.Done})
	for _, ev := range labEvents(t, sb) {
		if ev.Type == store.TypeBatchSwept {
			t.Fatal("execution-scoped sweep became a portable effect")
		}
	}

	status, err := sa.ExchangeStatus(labCtx, a.repo)
	if err != nil || len(status.PendingUpload) != 0 {
		t.Fatalf("lifecycle groups were not all uploaded: %+v %v", status, err)
	}
	outbox, err := sb.Outbox(labCtx, b.repo)
	if err != nil || len(outbox) != 0 {
		t.Fatalf("imported lifecycle work queued delivery candidates: %+v %v", outbox, err)
	}
}

// A portable action's baseline is the receiver's portable-plane tail — the
// last prior subject event sync can actually carry — never the literal
// history tail. reference.bound never leaves its store, so a start whose
// literal prior is a legacy tracker bind must still name the portable
// birth; otherwise a peer that never saw the bind pends "unknown baseline
// dependency" forever. fluke is a native copy made before the bind, so it
// is exactly the fresh peer this must not wedge — and its own later
// non-portable divergence must not conflict with the next portable fact.
func TestSharedStorePortableBaselineSkipsLocalFamilies(t *testing.T) {
	p := newLabPair(t)
	matter, err := writesurface.CreateMatter(labCtx, p.hybrid, store.ActorHuman, p.repo, "Bound", "bound")
	if err != nil {
		t.Fatal(err)
	}
	// The birth itself must arrive before the start can classify: fluke was
	// copied before this Matter existed.
	created := labBacklogSource(t, p.hybrid, "Hybrid", matter.ID, store.TypeMatterCreated)
	labImportBound(t, p.fluke, created, p.repo, "integrated")
	// reference.bound is the still-non-portable predecessor: it sits in
	// subject history with a Repo but no portable type, so the baseline scan
	// must step over it exactly as it did for reference.added before the
	// reference family became portable.
	labCommitMixed(t, p.hybrid, p.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeReferenceBound,
			Subject: matter.ID,
			Payload: store.ReferenceBound{Ref: "fixture:B-1"},
		}}, nil
	})
	start := labStart(t, p, p.hybrid, "Hybrid", matter.Locator)
	// The exported start must baseline on the portable birth, not on the
	// reference.bound that literally precedes it in subject history.
	var started store.ExperimentAction
	for _, a := range start.Actions {
		if a.Event.Type == store.TypeMatterStarted {
			started = a
		}
	}
	var birth string
	for _, ev := range labEvents(t, p.hybrid) {
		if ev.Subject == matter.ID && ev.Type == store.TypeMatterCreated {
			birth = ev.ID
		}
	}
	if started.Event.Type == "" {
		t.Fatal("start source has no matter.started action")
	}
	if started.Baseline != birth {
		t.Fatalf("start baseline = %q, want portable birth %q", started.Baseline, birth)
	}
	labImportBound(t, p.fluke, start, p.repo, "integrated")
	labState(t, p.fluke, matter.ID, store.InProgress)
	// fluke's own legacy bind is local-only divergence on the same subject:
	// the next portable fact must still integrate on the portable plane.
	labCommitMixed(t, p.fluke, p.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeReferenceBound,
			Subject: matter.ID,
			Payload: store.ReferenceBound{Ref: "fixture:LOCAL"},
		}}, nil
	})
	pause := labExportAfter(t, p.hybrid, "Hybrid", matter.ID, store.TypeMatterPaused,
		labVerbPause(p.hybrid, p.repo, matter.Locator))
	labImportBound(t, p.fluke, pause, p.repo, "integrated")
	labState(t, p.fluke, matter.ID, store.Paused)
	labRebuild(t, p.fluke)
}

// labRetainLegacyDecision seeds old-version outcomes only in disposable test
// stores. Cross-version validation also produces them through 211afeb's public
// imports; permanent regressions keep just the relevant persisted state here.
func labRetainLegacyDecision(t *testing.T, s *store.Store, repo string, d store.ExperimentDecision) {
	t.Helper()
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	raw, err := json.Marshal(d.Source)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	key := repo + ":" + d.Source.Actions[0].Event.ID
	if _, err := db.Exec(`INSERT INTO experiment_sources VALUES (?,?,?,?)`, key, string(raw), string(decision), repo); err != nil {
		t.Fatal(err)
	}
	for _, a := range d.Source.Actions {
		if _, err := db.Exec(`INSERT INTO experiment_actions VALUES (?,?,NULL,?)`, repo+":"+a.Event.ID, key, a.Baseline); err != nil {
			t.Fatal(err)
		}
	}
}

// A returned original is correspondence, not a request to repeat its verb
// against today's state. Later history and tombstones cannot erase it.
func TestSharedStoreHistoricalNativeEcho(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained=%v", retained), func(t *testing.T) {
			p := newLabPair(t)
			start := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
			finish := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepFinished,
				labVerbFinish(p.hybrid, p.repo, "shared/step-01"))
			replace := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepReplaced,
				labVerbReplace(p.hybrid, p.repo, "shared/step-01", "Replacement"))
			if err := labVerbFinish(p.hybrid, p.repo, "shared/stage")(); err != nil {
				t.Fatal(err)
			}
			if err := labVerbFinish(p.hybrid, p.repo, "shared")(); err != nil {
				t.Fatal(err)
			}
			before := labEvents(t, p.hybrid)
			sources := []store.ExperimentSource{finish, replace, start}
			b := exchangeBind(t, p.hybrid, p, "http://127.0.0.1:1/api/organizations/fixture/repos/fixture")
			if retained {
				for _, source := range sources {
					d := store.ExperimentDecision{Source: source, Disposition: "pending", Reason: "missing baseline evidence"}
					switch source.Actions[0].Event.Type {
					case store.TypeMatterStarted:
						d.Disposition, d.Reason = "conflict", "lifecycle or subject history diverged; never reopen or infer from current done"
					case store.TypeStepFinished:
						d.Reason = "missing live paired object"
					}
					labRetainLegacyDecision(t, p.hybrid, p.repo, d)
				}
			}
			// Import the dependent finish/replacement before the old cascade. None
			// needs a reconstructed baseline when its exact original already exists.
			for range 2 {
				if retained {
					if err := p.hybrid.ApplyExchangeInbox(labCtx, b); err != nil {
						t.Fatal(err)
					}
				}
				for _, source := range sources {
					d := labImportBound(t, p.hybrid, source, p.repo, "integrated")
					want := make([]string, len(source.Actions))
					for i, a := range source.Actions {
						want[i] = a.Event.ID
					}
					if !reflect.DeepEqual(d.Effects, want) || !reflect.DeepEqual(d.Source, source) {
						t.Fatalf("original correspondence changed: effects %v, want %v", d.Effects, want)
					}
					d, err := p.hybrid.ExperimentRecheckBound(labCtx, p.repo, source.Actions[0].Event.ID)
					if err != nil || d.Disposition != "integrated" || !reflect.DeepEqual(d.Effects, want) {
						t.Fatalf("recheck changed original correspondence: %+v %v", d, err)
					}
				}
			}
			changed := start
			changed.Actions = append([]store.ExperimentAction(nil), start.Actions...)
			var transition store.Transition
			if err := json.Unmarshal(changed.Actions[0].Event.Payload, &transition); err != nil {
				t.Fatal(err)
			}
			transition.TrackerPushLevel = "off"
			var err error
			changed.Actions[0].Event.Payload, err = json.Marshal(transition)
			if err != nil {
				t.Fatal(err)
			}
			if d := labImportBound(t, p.hybrid, changed, p.repo, "conflict"); len(d.Effects) != 0 {
				t.Fatal("changed native bytes accepted")
			}
			if !reflect.DeepEqual(before, labEvents(t, p.hybrid)) {
				t.Fatal("historical echoes duplicated or changed original events")
			}
			if _, err := p.hybrid.Node(labCtx, p.step1); err == nil {
				t.Fatal("historical echo resurrected replaced subject")
			}
			labRebuild(t, p.hybrid)
		})
	}
	t.Run("original-baseline-correspondence", func(t *testing.T) {
		p := newLabPair(t)
		labImportBound(t, p.fluke, labStart(t, p, p.hybrid, "Hybrid", "shared/step-01"), p.repo, "integrated")
		source := labExportAfter(t, p.fluke, "Fluke", p.step1, store.TypeStepFinished, labVerbFinish(p.fluke, p.repo, "shared/step-01"))
		if source.Actions[0].NativeBaseline == "" || source.Actions[0].Ancestors[0].NativeEvent == "" {
			t.Fatal("fixture did not canonicalize integration evidence")
		}
		before := labEvents(t, p.fluke)
		forged := source
		forged.Actions = append([]store.ExperimentAction(nil), source.Actions...)
		forged.Actions[0].NativeBaseline = ""
		labImportBound(t, p.fluke, forged, p.repo, "conflict")
		labImportBound(t, p.fluke, source, p.repo, "integrated")
		if !reflect.DeepEqual(before, labEvents(t, p.fluke)) {
			t.Fatal("native echo duplicated integration evidence")
		}
	})
	for _, ancestor := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw-effect-observation/ancestor=%v", ancestor), func(t *testing.T) {
			p := newLabPair(t)
			labImportBound(t, p.fluke, labStart(t, p, p.hybrid, "Hybrid", "shared/step-01"), p.repo, "integrated")
			source := labExportAfter(t, p.fluke, "Fluke", p.step1, store.TypeStepFinished, labVerbFinish(p.fluke, p.repo, "shared/step-01"))
			var forged store.ExperimentSource
			raw, _ := json.Marshal(source)
			if err := json.Unmarshal(raw, &forged); err != nil {
				t.Fatal(err)
			}
			a := &forged.Actions[0]
			if ancestor {
				a.Ancestors[0].Event, a.Ancestors[0].NativeEvent = a.Ancestors[0].NativeEvent, ""
			} else {
				a.Baseline, a.NativeBaseline = a.NativeBaseline, ""
			}
			labImportBound(t, p.fluke, forged, p.repo, "conflict")
			labImportBound(t, p.fluke, source, p.repo, "integrated")
		})
	}
}

// Native event equality must not authenticate altered source observations.
func TestSharedStoreNativeEchoClaims(t *testing.T) {
	changes := map[string]func(*store.ExperimentAction){
		"parent":           func(a *store.ExperimentAction) { a.Parent = a.Event.Subject },
		"baseline":         func(a *store.ExperimentAction) { a.Baseline = a.Event.ID },
		"native-baseline":  func(a *store.ExperimentAction) { a.NativeBaseline = a.Event.ID },
		"ancestor-subject": func(a *store.ExperimentAction) { a.Ancestors[0].Subject = a.Event.Subject },
		"ancestor-event":   func(a *store.ExperimentAction) { a.Ancestors[0].Event = a.Event.ID },
		"ancestor-native":  func(a *store.ExperimentAction) { a.Ancestors[0].NativeEvent = a.Event.ID },
		"missing-ancestor": func(a *store.ExperimentAction) { a.Ancestors = a.Ancestors[:1] },
	}
	for name, change := range changes {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained=%v", name, retained), func(t *testing.T) {
				p := newLabPair(t)
				source := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
				var forged store.ExperimentSource
				raw, _ := json.Marshal(source)
				if err := json.Unmarshal(raw, &forged); err != nil {
					t.Fatal(err)
				}
				change(&forged.Actions[len(forged.Actions)-1])
				if err := labVerbFinish(p.hybrid, p.repo, "shared/step-01")(); err != nil {
					t.Fatal(err)
				}
				before := labEvents(t, p.hybrid)
				if retained {
					labRetainLegacyDecision(t, p.hybrid, p.repo, store.ExperimentDecision{Source: forged, Disposition: "pending", Reason: "missing baseline evidence"})
					d, err := p.hybrid.ExperimentRecheckBound(labCtx, p.repo, forged.Actions[0].Event.ID)
					if err != nil || d.Disposition != "conflict" || len(d.Effects) != 0 {
						t.Fatalf("altered retained claims accepted: %s %v", d.Disposition, err)
					}
				} else {
					labImportBound(t, p.hybrid, forged, p.repo, "conflict")
					// Invalid native claims belong to collision evidence, not the
					// canonical source reservation needed by the genuine echo.
					labImportBound(t, p.hybrid, source, p.repo, "integrated")
				}
				if !reflect.DeepEqual(before, labEvents(t, p.hybrid)) {
					t.Fatal("claim validation changed native events")
				}
			})
		}
	}
}

func TestSharedStoreInboxRecoveryScope(t *testing.T) {
	p := newLabPair(t)
	b := exchangeBind(t, p.hybrid, p, "http://127.0.0.1:1/api/organizations/fixture/repos/fixture")
	other := p.hybrid.NewID()
	if _, err := p.hybrid.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: other}}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRepoAttached, Subject: other, Payload: store.RepoAttached{Label: "other"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	own := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	foreign := labStart(t, p, p.fluke, "Fluke", "shared/step-02")
	labRetainLegacyDecision(t, p.hybrid, p.repo, store.ExperimentDecision{Source: own, Disposition: "pending", Reason: "missing baseline evidence"})
	labRetainLegacyDecision(t, p.hybrid, other, store.ExperimentDecision{Source: own, Disposition: "conflict", Reason: "incompatible ancestor lifecycle"})
	labRetainLegacyDecision(t, p.hybrid, p.repo, store.ExperimentDecision{Source: foreign, Disposition: "conflict", Reason: "cycle"})
	conflicts := func() []store.ExperimentDecision {
		t.Helper()
		all, err := p.hybrid.ExperimentActivities(labCtx)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.ExperimentDecision
		for _, d := range all {
			if d.Disposition == "conflict" {
				out = append(out, d)
			}
		}
		return out
	}
	before, events := conflicts(), labEvents(t, p.hybrid)
	if len(before) != 2 {
		t.Fatal("missing negative controls")
	}
	for range 2 {
		if err := p.hybrid.ApplyExchangeInbox(labCtx, b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, conflicts()) || !reflect.DeepEqual(events, labEvents(t, p.hybrid)) {
			t.Fatal("recovery changed another Repo or settled conflict")
		}
	}
}

func TestSharedStoreCachedNativeEchoClaims(t *testing.T) {
	p := newLabPair(t)
	source := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	feed, endpoint := newLabExchangeFeed(t)
	b := exchangeBind(t, p.hybrid, p, endpoint)
	feed.failNextChangesPost = true
	if err := p.hybrid.ExchangePass(labCtx, store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}, b, []string{source.Actions[0].Event.ID}); err == nil {
		t.Fatal("expected disposable upload failure after capture")
	}
	db, err := sql.Open("sqlite", p.hybrid.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var before, after string
	if err := db.QueryRow(`SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, p.repo, source.Actions[0].Event.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := labVerbReplace(p.hybrid, p.repo, "shared/step-01", "Replacement")(); err != nil {
		t.Fatal(err)
	}
	events := labEvents(t, p.hybrid)
	labRetainLegacyDecision(t, p.hybrid, p.repo, store.ExperimentDecision{Source: source, Disposition: "pending", Reason: "missing baseline evidence"})
	forged := source
	forged.Actions = append([]store.ExperimentAction(nil), source.Actions...)
	forged.Actions[len(forged.Actions)-1].Baseline = source.Actions[0].Event.ID
	labImportBound(t, p.hybrid, forged, p.repo, "conflict")
	if err := p.hybrid.ApplyExchangeInbox(labCtx, b); err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.hybrid, source, p.repo, "integrated")
	if !reflect.DeepEqual(events, labEvents(t, p.hybrid)) {
		t.Fatal("cached echo recovery changed original events")
	}
	if err := db.QueryRow(`SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, p.repo, source.Actions[0].Event.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("native validation rewrote captured bytes")
	}
}

func TestSharedStoreNativeEchoEarlierAlias(t *testing.T) {
	p := newLabPair(t)
	path := p.fluke.Path()
	if err := p.fluke.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "third.db")
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	p.fluke = labOpen(t, path)
	third := labOpen(t, copyPath)
	older := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	later := labStart(t, p, p.fluke, "Fluke", "shared/step-01")
	if older.Actions[0].Event.ID >= later.Actions[0].Event.ID {
		t.Fatal("fixture alias ordering")
	}
	d := labImportBound(t, third, later, p.repo, "integrated")
	source := labStart(t, p, third, "Third", "shared/step-02")
	observed := source.Actions[0].Ancestors[0]
	if observed.Event != later.Actions[1].Event.ID || observed.NativeEvent != d.Effects[1] {
		t.Fatal("fixture did not export original ancestor correspondence")
	}
	labImportBound(t, third, older, p.repo, "integrated")
	feed, endpoint := newLabExchangeFeed(t)
	b := exchangeBind(t, third, p, endpoint)
	feed.failNextChangesPost = true
	if err := third.ExchangePass(labCtx, store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}, b, []string{source.Actions[0].Event.ID}); err == nil {
		t.Fatal("expected disposable capture failure")
	}
	db, err := sql.Open("sqlite", third.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var before, after string
	if err := db.QueryRow(`SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, p.repo, source.Actions[0].Event.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var change store.ExchangeChange
	var captured store.ExperimentSource
	if err := json.Unmarshal([]byte(before), &change); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(change.Source), &captured); err != nil {
		t.Fatal(err)
	}
	got := captured.Actions[0].Ancestors[0]
	if got.Event != older.Actions[1].Event.ID || got.NativeEvent != observed.NativeEvent {
		t.Fatal("fixture did not capture the earlier-sorting alias of the same effect")
	}
	events := labEvents(t, third)
	for range 2 {
		labImportBound(t, third, source, p.repo, "integrated")
		followRecheck(t, third, source, "integrated")
	}
	if !reflect.DeepEqual(events, labEvents(t, third)) {
		t.Fatal("earlier alias duplicated effects")
	}
	if err := db.QueryRow(`SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, p.repo, source.Actions[0].Event.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("earlier alias rewrote capture")
	}
}

// Start skips non-planned ancestors natively. Import must accept exactly
// that observed history, not require all ancestors to remain in-progress.
func TestSharedStoreStartUnderNonPlannedAncestors(t *testing.T) {
	for _, lifecycle := range []string{"done", "canceled", "paused", "divergent", "retained-done", "retained-finding"} {
		t.Run(lifecycle, func(t *testing.T) {
			retained := lifecycle == "retained-done" || lifecycle == "retained-finding"
			if lifecycle == "retained-done" {
				lifecycle = "done"
			}
			p := newLabPair(t)
			b := exchangeBind(t, p.fluke, p, "http://127.0.0.1:1/api/organizations/fixture/repos/fixture")
			stage := labStageID(t, p.hybrid, p.repo)
			start := labStart(t, p, p.hybrid, "Hybrid", "shared/stage")
			labImportBound(t, p.fluke, start, p.repo, "integrated")
			kind, verb := store.TypeStageFinished, labVerbFinish(p.hybrid, p.repo, "shared/stage")
			switch lifecycle {
			case "canceled":
				kind, verb = store.TypeStageCanceled, labVerbCancel(p.hybrid, p.repo, "shared/stage", "closed")
			case "paused":
				kind, verb = store.TypeStagePaused, labVerbPause(p.hybrid, p.repo, "shared/stage")
			}
			if lifecycle == "retained-finding" {
				labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", stage, store.TypeContentAppended, "integrated", func() error {
					_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared/stage", []byte("agreed evidence"))
					return err
				})
			} else {
				labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", stage, kind, "integrated", verb)
			}
			if lifecycle == "done" || lifecycle == "divergent" {
				labLifecycle(t, p, p.hybrid, p.fluke, "Hybrid", p.matter, store.TypeMatterFinished, "integrated",
					labVerbFinish(p.hybrid, p.repo, "shared"))
			}
			if lifecycle == "divergent" {
				if _, err := writesurface.AppendFinding(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/stage", []byte("local later evidence")); err != nil {
					t.Fatal(err)
				}
			}
			before := len(labEvents(t, p.fluke))
			for _, locator := range []string{"shared/step-01", "shared/step-02"} {
				source := labStart(t, p, p.hybrid, "Hybrid", locator)
				if len(source.Actions) != 1 {
					t.Fatal("native start unexpectedly moved a non-planned ancestor")
				}
				want := "integrated"
				if lifecycle == "divergent" {
					want = "conflict"
				}
				if !retained {
					labImportBound(t, p.fluke, source, p.repo, want)
				}
				subject := source.Actions[0].Event.Subject
				finish := labExportAfter(t, p.hybrid, "Hybrid", subject, store.TypeStepFinished,
					labVerbFinish(p.hybrid, p.repo, locator))
				if retained {
					reason := "incompatible ancestor lifecycle"
					if lifecycle == "retained-finding" {
						reason = "ancestor history diverged from its start"
					}
					labRetainLegacyDecision(t, p.fluke, p.repo, store.ExperimentDecision{Source: source, Disposition: "conflict", Reason: reason})
					labRetainLegacyDecision(t, p.fluke, p.repo, store.ExperimentDecision{Source: finish, Disposition: "pending", Reason: "missing baseline evidence"})
					for range 2 {
						if err := p.fluke.ApplyExchangeInbox(labCtx, b); err != nil {
							t.Fatal(err)
						}
					}
				}
				if lifecycle == "divergent" {
					labImportBound(t, p.fluke, finish, p.repo, "pending")
					labState(t, p.fluke, subject, store.Planned)
				} else {
					labImportBound(t, p.fluke, finish, p.repo, "integrated")
					labState(t, p.fluke, subject, store.Done)
				}
				// Retries and explicit reconsideration must not bypass either
				// the accepted correspondence or a genuine history conflict.
				labImportBound(t, p.fluke, source, p.repo, want)
				followRecheck(t, p.fluke, source, want)
			}
			added := 4
			if lifecycle == "divergent" {
				added = 0
			}
			if got := len(labEvents(t, p.fluke)); got != before+added {
				t.Fatalf("got %d events, want %d", got, before+added)
			}
			labRebuild(t, p.fluke)
		})
	}
}
