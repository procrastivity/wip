package tracker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

type fakeAlignmentView struct {
	node          store.Node
	completion    store.NodeCompletion
	refs          []string
	aggregates    map[string]store.TrackerDisposition
	backend       string
	target        string
	canceledLabel string
	project       string
	configKeys    []string
}

func (f *fakeAlignmentView) Node(context.Context, string) (store.Node, error) {
	return f.node, nil
}

func (f *fakeAlignmentView) NodeCompletion(context.Context, store.Node) (store.NodeCompletion, error) {
	return f.completion, nil
}

func (f *fakeAlignmentView) TrackerReferences(context.Context, string) ([]string, error) {
	return append([]string(nil), f.refs...), nil
}

func (f *fakeAlignmentView) TrackerAggregate(_ context.Context, ref string) (store.TrackerDisposition, bool, error) {
	disposition, found := f.aggregates[ref]
	return disposition, found, nil
}

func (f *fakeAlignmentView) Config(_ context.Context, _, key string) (string, bool, error) {
	f.configKeys = append(f.configKeys, key)
	if key == store.TrackerBackendKey && f.backend != "" {
		return f.backend, true, nil
	}
	if key == store.TrackerTargetKey && f.target != "" {
		return f.target, true, nil
	}
	if key == store.TrackerCanceledLabelKey && f.canceledLabel != "" {
		return f.canceledLabel, true, nil
	}
	if key == store.TrackerProjectKey && f.project != "" {
		return f.project, true, nil
	}
	return "", false, nil
}

func (f *fakeAlignmentView) Repo(context.Context, string) (store.Repo, error) {
	return store.Repo{ID: f.node.Repo}, nil
}

type fakeAlignmentReader struct {
	states        map[string]LiveState
	errors        map[string]error
	reads         []string
	factoryInputs []FactoryInput
}

func (*fakeAlignmentReader) Deliver(context.Context, store.OutboxEntry) (Result, error) {
	return Result{}, nil
}

func (f *fakeAlignmentReader) ReadState(_ context.Context, ref string) (LiveState, error) {
	f.reads = append(f.reads, ref)
	return f.states[ref], f.errors[ref]
}

func coordinatorFixture(reader *fakeAlignmentReader) (*AlignmentCoordinator, *fakeAlignmentView) {
	registry := NewRegistry()
	registry.Register("fake", func(input FactoryInput) (Seam, error) {
		reader.factoryInputs = append(reader.factoryInputs, input)
		return reader, nil
	})
	view := &fakeAlignmentView{
		node:       store.Node{ID: "matter-1", Kind: store.ScaleMatter, Repo: "repo-1", Lifecycle: store.Done},
		completion: store.NodeCompletion{Sealed: true},
		refs:       []string{"R-2", "R-1"},
		aggregates: map[string]store.TrackerDisposition{
			"R-2": store.TrackerActive,
			"R-1": store.TrackerCompleted,
		},
		backend:       "fake",
		target:        "team-1",
		canceledLabel: "wf::canceled",
		project:       "project-1",
	}
	return NewAlignmentCoordinator(registry), view
}

func TestAlignmentCoordinatorReadsEveryReferenceOnceInStableOrderWithPushOffIrrelevant(t *testing.T) {
	reader := &fakeAlignmentReader{states: map[string]LiveState{
		"R-2": {Class: LiveNonterminal, Display: "In Review", Lease: "lease-2"},
		"R-1": {Class: LiveActive, Display: "In Progress", Lease: "lease-1"},
	}}
	coordinator, view := coordinatorFixture(reader)

	report := coordinator.Check(context.Background(), view, view.node.ID)
	if !reflect.DeepEqual(reader.reads, []string{"R-2", "R-1"}) {
		t.Fatalf("reads = %v, want each active reference once in store order", reader.reads)
	}
	if !reflect.DeepEqual(view.configKeys, []string{store.TrackerBackendKey, store.TrackerTargetKey, store.TrackerCanceledLabelKey, store.TrackerProjectKey}) {
		t.Fatalf("config reads = %v; push level must not suppress live reads", view.configKeys)
	}
	if len(reader.factoryInputs) != 1 || reader.factoryInputs[0].Repo.ID != view.node.Repo || reader.factoryInputs[0].Target != view.target ||
		reader.factoryInputs[0].CanceledLabel != view.canceledLabel || reader.factoryInputs[0].Project != view.project {
		t.Fatalf("factory inputs = %+v, want repo %q, target %q, canceled label %q and project %q",
			reader.factoryInputs, view.node.Repo, view.target, view.canceledLabel, view.project)
	}
	if len(report.Items) != 2 || report.Items[0].Classification != AlignmentAligned || report.Items[1].Classification != AlignmentBehind {
		t.Fatalf("report = %+v", report)
	}
	if report.Items[1].Offer != store.TrackerCompleted || report.Items[1].Lease != "lease-1" {
		t.Fatalf("behind item = %+v", report.Items[1])
	}
}

func TestAlignmentCoordinatorReturnsBeforeProviderIOUnlessMatterIsSealed(t *testing.T) {
	reader := &fakeAlignmentReader{states: map[string]LiveState{}}
	coordinator, view := coordinatorFixture(reader)
	view.completion.Sealed = false

	report := coordinator.Check(context.Background(), view, view.node.ID)
	if len(report.Items) != 0 || len(reader.reads) != 0 || len(view.configKeys) != 0 {
		t.Fatalf("unsealed check = %+v, reads=%v config=%v", report, reader.reads, view.configKeys)
	}
}

func TestAlignmentCoordinatorReturnsSilentlyForUnboundMatter(t *testing.T) {
	reader := &fakeAlignmentReader{states: map[string]LiveState{}}
	coordinator, view := coordinatorFixture(reader)
	view.refs = nil

	report := coordinator.Check(context.Background(), view, view.node.ID)
	if len(report.Items) != 0 || len(reader.reads) != 0 || len(view.configKeys) != 0 {
		t.Fatalf("unbound check = %+v, reads=%v config=%v", report, reader.reads, view.configKeys)
	}
}

func TestAlignmentCoordinatorTreatsDismissedMatterAsCompletedWithoutProviderEvents(t *testing.T) {
	f := newFixture(t)
	if err := f.s.DeclareGate(f.ctx, f.repo, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	var matter string
	commit := func(decide func(context.Context, *store.Tx) ([]store.Draft, error)) {
		f.t.Helper()
		if _, err := f.s.Commit(f.ctx, store.Request{Actor: f.actor, Env: store.Env{Repo: f.repo}}, decide); err != nil {
			f.t.Fatal(err)
		}
	}
	commit(func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		matter = tx.NewID()
		return []store.Draft{{
			Type: store.TypeMatterCreated, Subject: matter,
			Payload: store.NodeBirth{Locator: "dismissed", Title: "Dismissed matter", SortKey: 1000},
		}}, nil
	})
	commit(func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeMatterStarted, Subject: matter,
			Payload: store.Transition{From: store.Planned, To: store.InProgress},
		}}, nil
	})
	commit(func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeMatterFinished, Subject: matter,
			Payload: store.Transition{From: store.InProgress, To: store.Done},
		}}, nil
	})
	commit(func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeReferenceAdded, Subject: matter,
			Payload: store.ReferenceAdded{Ref: "R-dismissed"},
		}}, nil
	})
	if _, err := writesurface.DismissGate(f.ctx, f.s, f.actor, f.repo, "reviewed-local", "dismissed", "the verifier is unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetConfig(f.ctx, f.repo, store.TrackerBackendKey, "fake"); err != nil {
		t.Fatal(err)
	}

	reader := &fakeAlignmentReader{states: map[string]LiveState{
		"R-dismissed": {Class: LiveCompleted, Display: "Done", Lease: "lease-dismissed"},
	}}
	providers := NewRegistry()
	providers.Register("fake", func(FactoryInput) (Seam, error) { return reader, nil })
	report := NewAlignmentCoordinator(providers).Check(f.ctx, f.s.View, matter)
	if len(report.Items) != 1 || report.Items[0].Classification != AlignmentAligned ||
		report.Items[0].Expected != store.TrackerCompleted {
		t.Fatalf("dismissed alignment report = %+v, want completed aggregate and aligned provider state", report)
	}
	if len(reader.reads) != 1 || reader.reads[0] != "R-dismissed" {
		t.Fatalf("provider reads = %v, want one read after canonical seal", reader.reads)
	}
	outbox, err := f.s.Outbox(f.ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 0 {
		t.Fatalf("dismissal created provider outbox entries: %+v", outbox)
	}
	events, err := f.s.Events(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.HasPrefix(event.Type, "tracker.") {
			t.Fatalf("dismissal emitted provider-specific event %s", event.Type)
		}
	}
}

func TestAlignmentCoordinatorDegradesBackendAndProviderFailures(t *testing.T) {
	t.Run("backend none", func(t *testing.T) {
		reader := &fakeAlignmentReader{states: map[string]LiveState{}}
		coordinator, view := coordinatorFixture(reader)
		view.backend = "none"
		report := coordinator.Check(context.Background(), view, view.node.ID)
		if len(report.Items) != 2 || report.Items[0].Classification != AlignmentUnavailable || len(reader.reads) != 0 {
			t.Fatalf("report = %+v, reads=%v", report, reader.reads)
		}
	})

	t.Run("provider error continues", func(t *testing.T) {
		reader := &fakeAlignmentReader{
			states: map[string]LiveState{"R-1": {Class: LiveCompleted, Display: "Done", Lease: "lease-1"}},
			errors: map[string]error{"R-2": errors.New("temporary API failure")},
		}
		coordinator, view := coordinatorFixture(reader)
		report := coordinator.Check(context.Background(), view, view.node.ID)
		if !reflect.DeepEqual(reader.reads, []string{"R-2", "R-1"}) {
			t.Fatalf("reads = %v", reader.reads)
		}
		if report.Items[0].Classification != AlignmentUnavailable || report.Items[1].Classification != AlignmentAligned {
			t.Fatalf("report = %+v", report)
		}
	})

	t.Run("provider construction failure", func(t *testing.T) {
		reader := &fakeAlignmentReader{states: map[string]LiveState{}}
		_, view := coordinatorFixture(reader)
		registry := NewRegistry()
		registry.Register("fake", func(FactoryInput) (Seam, error) { return nil, errors.New("missing credentials") })
		coordinator := NewAlignmentCoordinator(registry)
		report := coordinator.Check(context.Background(), view, view.node.ID)
		if len(report.Items) != 2 || report.Items[0].Classification != AlignmentUnavailable || !strings.Contains(report.Items[0].Reason, "missing credentials") || len(reader.reads) != 0 {
			t.Fatalf("report = %+v, reads=%v", report, reader.reads)
		}
	})

	t.Run("malformed provider state", func(t *testing.T) {
		reader := &fakeAlignmentReader{states: map[string]LiveState{
			"R-2": {Class: LiveClass("mystery"), Display: "Mystery", Lease: "lease"},
			"R-1": {Class: LiveCompleted, Display: "Done"},
		}}
		coordinator, view := coordinatorFixture(reader)
		report := coordinator.Check(context.Background(), view, view.node.ID)
		if len(report.Items) != 2 || report.Items[0].Classification != AlignmentUnavailable || report.Items[1].Classification != AlignmentUnavailable {
			t.Fatalf("report = %+v", report)
		}
	})
}

func TestAlignmentClassificationMatrixAndSilentAlignedOutput(t *testing.T) {
	tests := []struct {
		name     string
		expected store.TrackerDisposition
		observed LiveClass
		want     AlignmentClass
		offer    store.TrackerDisposition
	}{
		{name: "active exact", expected: store.TrackerActive, observed: LiveActive, want: AlignmentAligned},
		{name: "active compatible", expected: store.TrackerActive, observed: LiveNonterminal, want: AlignmentAligned},
		{name: "active behind", expected: store.TrackerActive, observed: LiveBacklog, want: AlignmentBehind, offer: store.TrackerActive},
		{name: "completed ahead", expected: store.TrackerActive, observed: LiveCompleted, want: AlignmentAhead},
		{name: "canceled conflicts with active", expected: store.TrackerActive, observed: LiveCanceled, want: AlignmentConflict},
		{name: "completed behind", expected: store.TrackerCompleted, observed: LiveNonterminal, want: AlignmentBehind, offer: store.TrackerCompleted},
		{name: "completed exact", expected: store.TrackerCompleted, observed: LiveCompleted, want: AlignmentAligned},
		{name: "terminal ambiguity", expected: store.TrackerCompleted, observed: LiveTerminal, want: AlignmentConflict},
		{name: "terminal competition", expected: store.TrackerCompleted, observed: LiveCanceled, want: AlignmentConflict},
		{name: "canceled exact", expected: store.TrackerCanceled, observed: LiveCanceled, want: AlignmentAligned},
		{name: "canceled behind", expected: store.TrackerCanceled, observed: LiveActive, want: AlignmentBehind, offer: store.TrackerCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := classifyAlignment("R", test.expected, LiveState{Class: test.observed, Display: string(test.observed), Lease: "lease"})
			if item.Classification != test.want || item.Offer != test.offer {
				t.Fatalf("item = %+v, want classification %s and offer %s", item, test.want, test.offer)
			}
		})
	}

	aligned := AlignmentReport{Items: []Alignment{{Reference: "R", Classification: AlignmentAligned}}}
	if aligned.Visible() || len(aligned.Lines()) != 0 {
		t.Fatalf("aligned report was visible: %+v %v", aligned, aligned.Lines())
	}
	behind := AlignmentReport{Items: []Alignment{{
		Reference: "R", Classification: AlignmentBehind, Expected: store.TrackerCompleted,
		Observed: "In Progress", Offer: store.TrackerCompleted,
	}}}
	if lines := behind.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "offer: move to completed") {
		t.Fatalf("behind lines = %v", lines)
	}
}
