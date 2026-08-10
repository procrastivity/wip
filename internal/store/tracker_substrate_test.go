package store

import (
	"context"
	"strings"
	"testing"
)

func wantTrackerReferences(t *testing.T, h *harness, matter string, want ...string) {
	t.Helper()
	got, err := h.TrackerReferences(h.ctx, matter)
	if err != nil {
		t.Fatalf("read tracker references: %v", err)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("tracker references are [%s], want [%s]", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestTrackerReferencesAreAMatterOwnedSet(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("reference-set", "Reference set")
	step := h.step(matter, "step-01", "A child")

	// Historical Matter bindings retain singleton-replacement meaning. A child
	// binding remains readable through nodes.external_ref but is seam-inert.
	h.commit(Draft{Type: TypeReferenceBound, Subject: matter, Payload: ReferenceBound{Ref: "GH-1"}})
	h.commit(Draft{Type: TypeReferenceBound, Subject: matter, Payload: ReferenceBound{Ref: "GH-2"}})
	h.commit(Draft{Type: TypeReferenceBound, Subject: step, Payload: ReferenceBound{Ref: "GH-child"}})
	wantTrackerReferences(t, h, matter, "GH-2")

	// New events add independent, equally active memberships.
	h.commit(Draft{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{Ref: "GL-3"}})
	wantTrackerReferences(t, h, matter, "GH-2", "GL-3")

	before := len(h.eventsOf(matter))
	err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{Ref: "GL-3"}}}, nil
	})
	refusalMentions(t, "adding an existing membership", err, "touched 0 projection rows")
	if got := len(h.eventsOf(matter)); got != before {
		t.Fatalf("a refused duplicate addition appended %d events", got-before)
	}

	h.commit(Draft{Type: TypeReferenceRemoved, Subject: matter, Payload: ReferenceRemoved{Ref: "GH-2"}})
	wantTrackerReferences(t, h, matter, "GL-3")

	// Rebind changes source and destination in one event and one transaction.
	h.commit(Draft{Type: TypeReferenceRebound, Subject: matter, Payload: ReferenceRebound{From: "GL-3", To: "BB-4"}})
	wantTrackerReferences(t, h, matter, "BB-4")

	// A failed destination leaves the source membership intact: no intermediate
	// set is observable even when the second half refuses.
	h.commit(Draft{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{Ref: "GH-5"}})
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceRebound, Subject: matter, Payload: ReferenceRebound{From: "BB-4", To: "GH-5"}}}, nil
	})
	refusalMentions(t, "rebinding onto an existing membership", err, "touched 0 projection rows")
	wantTrackerReferences(t, h, matter, "BB-4", "GH-5")

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild tracker references: %v", err)
	}
	wantTrackerReferences(t, h, matter, "BB-4", "GH-5")
}

func TestTrackerReferenceEventsRefuseChildrenWithoutAnEvent(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("children-are-inert", "Children are inert")
	step := h.step(matter, "step-01", "A child")
	before := len(h.eventsOf(step))

	err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceAdded, Subject: step, Payload: ReferenceAdded{Ref: "GH-6"}}}, nil
	})
	refusalMentions(t, "adding a child reference", err, "not a Matter")
	if got := len(h.eventsOf(step)); got != before {
		t.Fatalf("a refused child binding appended %d events", got-before)
	}
}
