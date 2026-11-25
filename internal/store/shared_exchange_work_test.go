//go:build sharedstoreexperiment

package store_test

import (
	"context"
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
	var omitted []string
	for _, a := range d.Source.Derivative.Omitted {
		omitted = append(omitted, a.Event.Type)
	}
	if !reflect.DeepEqual(omitted, []string{store.TypeBatchSwept}) {
		t.Fatalf("derivative omitted = %v, want [batch.swept]", omitted)
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
