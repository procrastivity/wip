package store

import (
	"context"
	"strings"
	"testing"
)

// openDispatchForTest opens a bare P1-shaped dispatch bracket on the harness
// worktree and returns its identity.
func openDispatchForTest(h *harness) string {
	h.t.Helper()
	return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID()}, nil
	})
}

func spawnRoleForTest(h *harness, dispatch string, name RoleName) string {
	h.t.Helper()
	return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: dispatch, Name: name},
		}, nil
	})
}

func TestRoleLifecycleBindsToDispatchAndClone(t *testing.T) {
	h := newHarness(t)
	dispatch := openDispatchForTest(h)

	role := spawnRoleForTest(h, dispatch, RoleBuilder)
	got, err := h.Role(h.ctx, role)
	if err != nil || !got.Open || got.Name != RoleBuilder || got.Clone != h.Clone || got.Dispatch != dispatch {
		t.Fatalf("Role = %#v, err=%v", got, err)
	}

	open, found, err := h.OpenRole(h.ctx, dispatch, RoleBuilder)
	if err != nil || !found || open.ID != role {
		t.Fatalf("OpenRole = %#v found=%v err=%v", open, found, err)
	}

	// Closing under the role's own actor token is the end-to-end exercise the
	// envelope carried since event one (MODEL §10).
	if _, err := h.Commit(h.ctx,
		Request{Actor: RoleBuilder.Actor(), Env: Env{Repo: h.Repo, Clone: h.Clone, Worktree: h.Worktree}},
		func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeRoleClosed, Subject: role,
				Payload: RoleClosed{Reason: RoleCloseCompleted},
			}}, nil
		}); err != nil {
		t.Fatalf("close under role actor: %v", err)
	}
	got, err = h.Role(h.ctx, role)
	if err != nil || got.Open || got.CloseReason != RoleCloseCompleted || got.ClosedAt == nil {
		t.Fatalf("closed Role = %#v, err=%v", got, err)
	}
	closeEvents := h.rowsOf("events", "type = ?", TypeRoleClosed)
	if len(closeEvents) != 1 || closeEvents[0]["actor"] != "'role:builder'" {
		t.Fatalf("role.closed events = %v, want one under role:builder", closeEvents)
	}

	if _, found, _ := h.OpenRole(h.ctx, dispatch, RoleBuilder); found {
		t.Fatal("a closed role is still reported open")
	}
	all, err := h.RolesForDispatch(h.ctx, dispatch)
	if err != nil || len(all) != 1 || all[0].ID != role {
		t.Fatalf("RolesForDispatch = %#v, err=%v", all, err)
	}
}

func TestRoleSpawnRefusals(t *testing.T) {
	h := newHarness(t)
	dispatch := openDispatchForTest(h)

	// An unknown role name is a typo, not a new role.
	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: dispatch, Name: "reviewer"},
		}}, nil
	})
	if !strings.Contains(err.Error(), "not one of the six roles") {
		t.Fatalf("unknown role refusal = %v", err)
	}

	// The same role twice into one bracket is contention, not a second row.
	spawnRoleForTest(h, dispatch, RoleResearcher)
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: dispatch, Name: RoleResearcher},
		}}, nil
	})
	if !strings.Contains(err.Error(), "refusal.role-contention") {
		t.Fatalf("duplicate role refusal = %v", err)
	}
	// A different role beside it is normal.
	spawnRoleForTest(h, dispatch, RoleVerifier)

	// A closed bracket is not a place to spawn into.
	h.commit(Draft{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: CloseCompleted}})
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: dispatch, Name: RoleBuilder},
		}}, nil
	})
	if !strings.Contains(err.Error(), "closed Dispatch") {
		t.Fatalf("closed dispatch refusal = %v", err)
	}

	// An unknown Dispatch identity is refused outright.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		id := tx.NewID()
		return []Draft{{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: id, Name: RoleBuilder},
		}}, nil
	})
	if !strings.Contains(err.Error(), "unknown Dispatch") {
		t.Fatalf("unknown dispatch refusal = %v", err)
	}
}

func TestRoleEventsCarryTheDispatchDimensions(t *testing.T) {
	h := newHarness(t)
	feature := h.attachWorktree(h.Repo, WorktreeAttached{Clone: h.Clone, Name: "feature"})
	onFeature := h.with(Env{Repo: h.Repo, Clone: h.Clone, Worktree: feature})
	dispatch := onFeature.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID()}, nil
	})

	// Spawning from the main worktree against a feature-worktree Dispatch
	// disagrees with the bracket and is refused.
	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: dispatch, Name: RoleBuilder},
		}}, nil
	})
	if !strings.Contains(err.Error(), "dimensions disagree") {
		t.Fatalf("cross-worktree spawn refusal = %v", err)
	}

	role := onFeature.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type: TypeRoleSpawned, Subject: tx.NewID(),
			Payload: RoleSpawned{Dispatch: dispatch, Name: RoleBuilder},
		}, nil
	})
	// And the close has to speak from the same bracket too.
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeRoleClosed, Subject: role,
			Payload: RoleClosed{Reason: RoleCloseCompleted},
		}}, nil
	})
	if !strings.Contains(err.Error(), "dimensions disagree") {
		t.Fatalf("cross-worktree close refusal = %v", err)
	}
}

func TestDispatchCloseReapsItsOpenRoles(t *testing.T) {
	h := newHarness(t)
	dispatch := openDispatchForTest(h)
	reaped := spawnRoleForTest(h, dispatch, RoleBuilder)
	closed := spawnRoleForTest(h, dispatch, RoleResearcher)
	h.commit(Draft{Type: TypeRoleClosed, Subject: closed, Payload: RoleClosed{Reason: RoleCloseCompleted}})

	h.commit(Draft{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: CloseSuperseded}})

	got, err := h.Role(h.ctx, reaped)
	if err != nil || got.Open || got.CloseReason != RoleCloseReaped {
		t.Fatalf("role left open across a dispatch close = %#v, err=%v", got, err)
	}
	// The explicitly closed role keeps its own reason; the cascade never
	// touches a closed row.
	got, err = h.Role(h.ctx, closed)
	if err != nil || got.CloseReason != RoleCloseCompleted {
		t.Fatalf("explicitly closed role = %#v, err=%v", got, err)
	}

	// A second close of the reaped role finds nothing open.
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeRoleClosed, Subject: reaped,
			Payload: RoleClosed{Reason: RoleCloseCompleted},
		}}, nil
	})
	if !strings.Contains(err.Error(), "touched 0 projection rows") {
		t.Fatalf("double close refusal = %v", err)
	}
}

func TestBatchSweepReapsRolesThroughRunAndDispatch(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("sweep-roles", "Sweep reaps roles")
	batch := h.newAnonymousBatch(matter)
	run := startRunForTest(h, batch, "run-01", matter)
	dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type: TypeDispatchOpened, Subject: tx.NewID(),
			Payload: DispatchOpened{Run: run, Matter: matter},
		}, nil
	})
	role := spawnRoleForTest(h, dispatch, RoleOrchestrator)

	h.commit(Draft{Type: TypeBatchSwept, Subject: batch, Payload: BatchSweptPayload{}})

	got, err := h.Role(h.ctx, role)
	if err != nil || got.Open || got.CloseReason != RoleCloseReaped {
		t.Fatalf("role after sweep = %#v, err=%v", got, err)
	}
}
