package writesurface

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// roleActive is D14's derivation: an outer-loop role is active for a Repo
// exactly when the Repo declares a gate that role owns.
func roleActive(name store.RoleName, declared []store.GateDeclaration) bool {
	for _, d := range declared {
		if owner, owned := store.GateOwner(d.Gate); owned && owner == name {
			return true
		}
	}
	return false
}

// RoleActivation is the derived activation state of one outer-loop role for
// one Repo (D14): the gates it owns, the owned gates the Repo declares, and
// what that means for spawning it.
type RoleActivation struct {
	Role store.RoleName
	// Owns is every gate whose declaration would activate the role; Declared
	// is the subset this Repo actually declares.
	Owns     []string
	Declared []string
	Active   bool
	// Inert marks a role whose activation derives normally but whose gates
	// have no seam to answer from yet (Warden until Phase 4).
	Inert bool
}

// Activation derives the outer-loop activation list from a Repo's gate
// declarations — the gate list read from the role side.
func Activation(declared []store.GateDeclaration) []RoleActivation {
	out := make([]RoleActivation, 0, 2)
	for _, name := range []store.RoleName{store.RoleVerifier, store.RoleWarden} {
		a := RoleActivation{Role: name, Owns: name.OwnedGates(), Declared: []string{}, Inert: name == store.RoleWarden}
		for _, d := range declared {
			if owner, owned := store.GateOwner(d.Gate); owned && owner == name {
				a.Declared = append(a.Declared, d.Gate)
			}
		}
		a.Active = len(a.Declared) > 0 && !a.Inert
		out = append(out, a)
	}
	return out
}

// SpawnRole births a role instance into this worktree's open dispatch (MODEL
// §6): execution keyed at Clone, bound to the Dispatch bracket (D59). The
// actor is the spawner — a human today, an Orchestrator once the scheduler
// exists — never the role being born.
func SpawnRole(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, name store.RoleName) (store.Role, error) {
	if !store.KnownRole(name) {
		return store.Role{}, wiperr.New("validation.unknown-role",
			fmt.Sprintf("%q is not one of the six roles (orchestrator, coordinator, researcher, builder, verifier, warden)", name))
	}
	// An outer-loop role activates by gate declaration alone (D14): the gate
	// list and the outer-loop role list are one list from two sides, and no
	// config ever names a role directly. Undeclared gates leave the role
	// dormant. Warden's activation derives by the same rule but stays inert
	// until the forge seam (Phase 4) gives `reviewed`/`ci-green` something to
	// answer from — a hand-closed CI gate would be a seal that lies.
	if name.OuterLoop() {
		declared, err := s.GateDeclarations(ctx, env.Repo)
		if err != nil {
			return store.Role{}, err
		}
		if !roleActive(name, declared) {
			return store.Role{}, wiperr.New("refusal.role-dormant",
				fmt.Sprintf("%s is dormant here: none of its gates (%s) are declared, and gate config drives activation (D14)",
					name, strings.Join(name.OwnedGates(), ", ")))
		}
		if name == store.RoleWarden {
			return store.Role{}, wiperr.New("refusal.role-inert",
				"warden activates by declaration (D14) but stays inert until the forge seam exists (Phase 4)")
		}
	}
	dispatch, found, err := s.OpenDispatch(ctx, env.Worktree)
	if err != nil {
		return store.Role{}, err
	}
	if !found {
		return store.Role{}, wiperr.New("validation.no-open-dispatch",
			"no open dispatch on this worktree; run `wip refresh` first")
	}

	req := store.Request{Actor: actor, Env: env}
	var roleID string
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		if _, open, err := tx.OpenRole(ctx, dispatch.ID, name); err != nil {
			return nil, err
		} else if open {
			return nil, wiperr.New("refusal.role-contention",
				fmt.Sprintf("this dispatch already has an open %s", name))
		}
		roleID = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeRoleSpawned,
			Subject: roleID,
			Payload: store.RoleSpawned{Dispatch: dispatch.ID, Name: name},
		}}, nil
	}); err != nil {
		return store.Role{}, err
	}
	return s.Role(ctx, roleID)
}

// CloseRole closes the open instance of a role on this worktree's dispatch
// with reason `completed`. The event's actor is the role's own token — a
// Builder closing is the Builder speaking (MODEL §10), which is what keeps
// outer-loop roles visible to Session. The `reaped` reason is never spoken
// through this verb: it is the dispatch close's cascade.
func CloseRole(ctx context.Context, s *store.Store, env store.Env, name store.RoleName) (store.Role, error) {
	if !store.KnownRole(name) {
		return store.Role{}, wiperr.New("validation.unknown-role",
			fmt.Sprintf("%q is not one of the six roles (orchestrator, coordinator, researcher, builder, verifier, warden)", name))
	}
	dispatch, found, err := s.OpenDispatch(ctx, env.Worktree)
	if err != nil {
		return store.Role{}, err
	}
	if !found {
		return store.Role{}, wiperr.New("validation.no-open-dispatch",
			"no open dispatch on this worktree; run `wip refresh` first")
	}

	// Asked before the commit as well as inside it: the commit speaks as the
	// role, and a close with nothing open should refuse as "no open role"
	// rather than as an unbacked actor claim.
	if _, open, err := s.OpenRole(ctx, dispatch.ID, name); err != nil {
		return store.Role{}, err
	} else if !open {
		return store.Role{}, wiperr.New("validation.no-open-role",
			fmt.Sprintf("no open %s on this dispatch", name))
	}

	req := store.Request{Actor: name.Actor(), Env: env}
	var roleID string
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		role, open, err := tx.OpenRole(ctx, dispatch.ID, name)
		if err != nil {
			return nil, err
		}
		if !open {
			return nil, wiperr.New("validation.no-open-role",
				fmt.Sprintf("no open %s on this dispatch", name))
		}
		roleID = role.ID
		return []store.Draft{{
			Type:    store.TypeRoleClosed,
			Subject: role.ID,
			Payload: store.RoleClosed{Reason: store.RoleCloseCompleted},
		}}, nil
	}); err != nil {
		return store.Role{}, err
	}
	return s.Role(ctx, roleID)
}
