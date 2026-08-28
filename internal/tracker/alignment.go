package tracker

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// AlignmentClass classifies one live tracker observation against wip's fresh
// shared-reference aggregate.
type AlignmentClass string

// Alignment classes describe how a live provider state compares with wip.
const (
	AlignmentAligned     AlignmentClass = "aligned"
	AlignmentAhead       AlignmentClass = "ahead"
	AlignmentBehind      AlignmentClass = "behind"
	AlignmentConflict    AlignmentClass = "conflict"
	AlignmentUnavailable AlignmentClass = "unavailable"
)

// Alignment is one reference's post-seal comparison. Offer is present only
// for a forward transition from a nonterminal provider state. Lease remains
// opaque and is reported for a later guarded write. This coordinator never
// performs that write.
type Alignment struct {
	Reference      string                   `json:"reference,omitempty"`
	Classification AlignmentClass           `json:"classification"`
	Expected       store.TrackerDisposition `json:"expected,omitempty"`
	Observed       string                   `json:"observed,omitempty"`
	ObservedClass  LiveClass                `json:"observedClass,omitempty"`
	Lease          string                   `json:"lease,omitempty"`
	Offer          store.TrackerDisposition `json:"offer,omitempty"`
	Reason         string                   `json:"reason,omitempty"`
}

// AlignmentReport is the optional post-seal addition to a command's existing
// JSON success object. Items retain the Matter's stable reference order.
type AlignmentReport struct {
	Items []Alignment `json:"items"`
}

// Visible reports whether the command has anything to tell the operator.
// An all-aligned report is deliberately silent.
func (r AlignmentReport) Visible() bool {
	for _, item := range r.Items {
		if item.Classification != AlignmentAligned {
			return true
		}
	}
	return false
}

// Lines renders only differences and unavailable reads. Aligned references
// produce no human output.
func (r AlignmentReport) Lines() []string {
	var lines []string
	for _, item := range r.Items {
		switch item.Classification {
		case AlignmentAligned:
			continue
		case AlignmentAhead:
			lines = append(lines, fmt.Sprintf("tracker %s is ahead: expected %s, observed %s", item.Reference, item.Expected, item.Observed))
		case AlignmentBehind:
			lines = append(lines, fmt.Sprintf("tracker %s is behind: expected %s, observed %s (offer: move to %s)", item.Reference, item.Expected, item.Observed, item.Offer))
		case AlignmentConflict:
			lines = append(lines, fmt.Sprintf("tracker %s conflicts: expected %s, observed %s", item.Reference, item.Expected, item.Observed))
		case AlignmentUnavailable:
			ref := item.Reference
			if ref == "" {
				ref = "state"
			}
			lines = append(lines, fmt.Sprintf("tracker %s is unavailable: %s", ref, item.Reason))
		}
	}
	return lines
}

// AlignmentView is the provider-neutral local read surface used after seal.
// Store.View satisfies it, and focused tests can supply a read-only fake.
type AlignmentView interface {
	Node(context.Context, string) (store.Node, error)
	GateDeclarations(context.Context, string) ([]store.GateDeclaration, error)
	GateSatisfied(context.Context, string, string, string) (bool, error)
	TrackerReferences(context.Context, string) ([]string, error)
	TrackerAggregate(context.Context, string) (store.TrackerDisposition, bool, error)
	Config(context.Context, string, string) (string, bool, error)
	Repo(context.Context, string) (store.Repo, error)
}

// AlignmentCoordinator performs provider-neutral, read-only post-seal checks.
// Provider resolution and I/O failures become unavailable items. They never
// fail or undo the local command that sealed the Matter.
type AlignmentCoordinator struct {
	providers *Registry
}

// NewAlignmentCoordinator constructs a reusable post-seal coordinator.
func NewAlignmentCoordinator(providers *Registry) *AlignmentCoordinator {
	return &AlignmentCoordinator{providers: providers}
}

// Check re-reads matter after the local commit. It returns without provider
// I/O unless matter is now a sealed Matter. Each active reference is read once
// in the stable order supplied by the store.
func (c *AlignmentCoordinator) Check(ctx context.Context, v AlignmentView, matterID string) AlignmentReport {
	matter, err := v.Node(ctx, matterID)
	if err != nil {
		return unavailableReport("", err)
	}
	if matter.Kind != store.ScaleMatter || matter.Lifecycle != store.Done {
		return AlignmentReport{}
	}
	declarations, err := v.GateDeclarations(ctx, matter.Repo)
	if err != nil {
		return unavailableReport("", err)
	}
	for _, declaration := range declarations {
		if declaration.Scale != store.ScaleMatter {
			continue
		}
		satisfied, err := v.GateSatisfied(ctx, matter.Repo, matter.ID, declaration.Gate)
		if err != nil {
			return unavailableReport("", err)
		}
		if !satisfied {
			return AlignmentReport{}
		}
	}

	refs, err := v.TrackerReferences(ctx, matter.ID)
	if err != nil {
		return unavailableReport("", err)
	}
	if len(refs) == 0 {
		return AlignmentReport{}
	}

	backend, present, err := v.Config(ctx, matter.Repo, store.TrackerBackendKey)
	if err != nil {
		return unavailableForRefs(refs, err.Error())
	}
	backend = strings.TrimSpace(backend)
	if !present || backend == "" || backend == "none" {
		return unavailableForRefs(refs, "tracker backend is none")
	}
	target, _, err := v.Config(ctx, matter.Repo, store.TrackerTargetKey)
	if err != nil {
		return unavailableForRefs(refs, err.Error())
	}
	canceledLabel, _, err := v.Config(ctx, matter.Repo, store.TrackerCanceledLabelKey)
	if err != nil {
		return unavailableForRefs(refs, err.Error())
	}
	repo, err := v.Repo(ctx, matter.Repo)
	if err != nil {
		return unavailableForRefs(refs, err.Error())
	}
	seam, err := c.providers.Resolve(backend, FactoryInput{Repo: repo, Target: target, CanceledLabel: canceledLabel})
	if err != nil {
		return unavailableForRefs(refs, err.Error())
	}
	reader, ok := seam.(StateReader)
	if !ok || reader == nil {
		return unavailableForRefs(refs, fmt.Sprintf("tracker backend %q does not support live-state reads", backend))
	}

	report := AlignmentReport{Items: make([]Alignment, 0, len(refs))}
	for _, ref := range refs {
		expected, found, aggregateErr := v.TrackerAggregate(ctx, ref)
		if aggregateErr != nil {
			report.Items = append(report.Items, unavailable(ref, aggregateErr.Error()))
			continue
		}
		if !found {
			report.Items = append(report.Items, unavailable(ref, "reference has no active local binding"))
			continue
		}
		live, readErr := reader.ReadState(ctx, ref)
		if readErr != nil {
			report.Items = append(report.Items, unavailable(ref, readErr.Error()))
			continue
		}
		if err := validateLiveState(live); err != nil {
			report.Items = append(report.Items, unavailable(ref, err.Error()))
			continue
		}
		report.Items = append(report.Items, classifyAlignment(ref, expected, live))
	}
	return report
}

func classifyAlignment(ref string, expected store.TrackerDisposition, live LiveState) Alignment {
	item := Alignment{
		Reference: ref, Expected: expected, Observed: live.Display,
		ObservedClass: live.Class, Lease: live.Lease,
	}
	switch expected {
	case store.TrackerActive:
		switch live.Class {
		case LiveActive, LiveNonterminal:
			item.Classification = AlignmentAligned
		case LiveBacklog:
			item.Classification, item.Offer = AlignmentBehind, expected
		case LiveCompleted:
			item.Classification = AlignmentAhead
		default:
			item.Classification = AlignmentConflict
		}
	case store.TrackerCompleted:
		switch live.Class {
		case LiveCompleted:
			item.Classification = AlignmentAligned
		case LiveBacklog, LiveActive, LiveNonterminal:
			item.Classification, item.Offer = AlignmentBehind, expected
		default:
			item.Classification = AlignmentConflict
		}
	case store.TrackerCanceled:
		switch live.Class {
		case LiveCanceled:
			item.Classification = AlignmentAligned
		case LiveBacklog, LiveActive, LiveNonterminal:
			item.Classification, item.Offer = AlignmentBehind, expected
		default:
			item.Classification = AlignmentConflict
		}
	default:
		return unavailable(ref, fmt.Sprintf("unknown expected tracker disposition %q", expected))
	}
	return item
}

func validateLiveState(live LiveState) error {
	switch live.Class {
	case LiveBacklog, LiveActive, LiveNonterminal, LiveCompleted, LiveCanceled, LiveTerminal:
	default:
		return fmt.Errorf("provider returned unknown live-state class %q", live.Class)
	}
	if strings.TrimSpace(live.Display) == "" || strings.TrimSpace(live.Lease) == "" {
		return fmt.Errorf("provider returned live state without a display state and lease")
	}
	return nil
}

func unavailable(ref, reason string) Alignment {
	return Alignment{Reference: ref, Classification: AlignmentUnavailable, Reason: reason}
}

func unavailableReport(ref string, err error) AlignmentReport {
	return AlignmentReport{Items: []Alignment{unavailable(ref, err.Error())}}
}

func unavailableForRefs(refs []string, reason string) AlignmentReport {
	report := AlignmentReport{Items: make([]Alignment, 0, len(refs))}
	for _, ref := range refs {
		report.Items = append(report.Items, unavailable(ref, reason))
	}
	return report
}
