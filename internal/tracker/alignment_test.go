package tracker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

type fakeAlignmentView struct {
	node       store.Node
	gates      []store.GateDeclaration
	satisfied  map[string]bool
	refs       []string
	aggregates map[string]store.TrackerDisposition
	backend    string
	configKeys []string
}

func (f *fakeAlignmentView) Node(context.Context, string) (store.Node, error) {
	return f.node, nil
}

func (f *fakeAlignmentView) GateDeclarations(context.Context, string) ([]store.GateDeclaration, error) {
	return f.gates, nil
}

func (f *fakeAlignmentView) GateSatisfied(_ context.Context, _, _ string, gate string) (bool, error) {
	return f.satisfied[gate], nil
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
	return "", false, nil
}

func (f *fakeAlignmentView) Repo(context.Context, string) (store.Repo, error) {
	return store.Repo{ID: f.node.Repo}, nil
}

type fakeAlignmentReader struct {
	states map[string]LiveState
	errors map[string]error
	reads  []string
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
	registry.Register("fake", func(store.Repo) (Seam, error) { return reader, nil })
	view := &fakeAlignmentView{
		node: store.Node{ID: "matter-1", Kind: store.ScaleMatter, Repo: "repo-1", Lifecycle: store.Done},
		refs: []string{"R-2", "R-1"},
		aggregates: map[string]store.TrackerDisposition{
			"R-2": store.TrackerActive,
			"R-1": store.TrackerCompleted,
		},
		backend: "fake",
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
	if !reflect.DeepEqual(view.configKeys, []string{store.TrackerBackendKey}) {
		t.Fatalf("config reads = %v; push level must not suppress live reads", view.configKeys)
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
	view.gates = []store.GateDeclaration{{Gate: "reviewed-local", Scale: store.ScaleMatter}}
	view.satisfied = map[string]bool{"reviewed-local": false}

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
		registry.Register("fake", func(store.Repo) (Seam, error) { return nil, errors.New("missing credentials") })
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
