package writesurface

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

func wantValidation(t *testing.T, err error, code, fragment string) {
	t.Helper()
	var werr *wiperr.Error
	if !errors.As(err, &werr) {
		t.Fatalf("err = %v (%T), want *wiperr.Error", err, err)
	}
	if werr.Code != code || !strings.Contains(werr.Message, fragment) {
		t.Fatalf("err = %s %q, want code %s containing %q", werr.Code, werr.Message, code, fragment)
	}
}

func TestBacklogExitsRefuseANonEnteredEntryWithValidation(t *testing.T) {
	f := newBatchFixture(t, "backlog-exits")
	ctx := context.Background()

	a, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "entry A", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "entry B", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "entry C", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := BacklogDelegate(ctx, f.s, store.ActorHuman, f.env.Repo, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := BacklogPlan(ctx, f.s, store.ActorHuman, f.env.Repo, b.ID, "backlog-exits"); err != nil {
		t.Fatal(err)
	}
	if _, err := BacklogDecline(ctx, f.s, store.ActorHuman, f.env.Repo, c.ID, "not now"); err != nil {
		t.Fatal(err)
	}

	before, err := f.s.EventsOfSubject(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BacklogPlan(ctx, f.s, store.ActorHuman, f.env.Repo, a.ID, "backlog-exits")
	wantValidation(t, err, "validation.backlog-not-entered", "is delegated; only an entered entry can be planned")
	if !strings.Contains(err.Error(), a.ID) {
		t.Fatalf("err = %q, want it to name entry %s", err, a.ID)
	}

	_, err = BacklogDecline(ctx, f.s, store.ActorHuman, f.env.Repo, a.ID, "x")
	wantValidation(t, err, "validation.backlog-not-entered", "can be declined")

	_, err = BacklogDelegate(ctx, f.s, store.ActorHuman, f.env.Repo, a.ID)
	wantValidation(t, err, "validation.backlog-not-entered", "is delegated; only an entered entry can be delegated")

	_, err = BacklogPlan(ctx, f.s, store.ActorHuman, f.env.Repo, b.ID, "backlog-exits")
	wantValidation(t, err, "validation.backlog-not-entered", "is planned")

	_, err = BacklogDelegate(ctx, f.s, store.ActorHuman, f.env.Repo, c.ID)
	wantValidation(t, err, "validation.backlog-not-entered", "is declined")

	after, err := f.s.EventsOfSubject(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("EventsOfSubject(%s) = %d events after refused calls, want unchanged from %d", a.ID, len(after), len(before))
	}
	if len(after) != 2 {
		t.Fatalf("EventsOfSubject(%s) = %d events, want 2 (entered + delegated)", a.ID, len(after))
	}
}

func TestBacklogExitsRefuseAnUnknownEntryWithValidation(t *testing.T) {
	f := newBatchFixture(t, "backlog-exits")
	ctx := context.Background()

	_, err := BacklogPlan(ctx, f.s, store.ActorHuman, f.env.Repo, "01NOPE", "backlog-exits")
	wantValidation(t, err, "validation.unknown-backlog-entry", "01NOPE")

	_, err = BacklogDecline(ctx, f.s, store.ActorHuman, f.env.Repo, "01NOPE", "reason")
	wantValidation(t, err, "validation.unknown-backlog-entry", "01NOPE")

	_, err = BacklogDelegate(ctx, f.s, store.ActorHuman, f.env.Repo, "01NOPE")
	wantValidation(t, err, "validation.unknown-backlog-entry", "01NOPE")
}

func TestBacklogExitsStillSucceedOnAnEnteredEntry(t *testing.T) {
	f := newBatchFixture(t, "backlog-exits")
	ctx := context.Background()

	entry, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "entry", "", "")
	if err != nil {
		t.Fatal(err)
	}

	planned, err := BacklogPlan(ctx, f.s, store.ActorHuman, f.env.Repo, entry.ID, "backlog-exits")
	if err != nil {
		t.Fatal(err)
	}
	if planned.State != "planned" {
		t.Fatalf("planned.State = %q, want %q", planned.State, "planned")
	}
	if planned.Matter != f.matter {
		t.Fatalf("planned.Matter = %q, want %q", planned.Matter, f.matter)
	}
}
