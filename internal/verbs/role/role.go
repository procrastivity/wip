// Package role implements `wip role` — spawning and closing role instances
// (MODEL §6). A role instance is execution: it keys at Clone and binds to
// this worktree's open Dispatch (D59). Spawn speaks as the caller; close
// speaks as the role itself, which is what keeps a Builder closing visible
// to Session (MODEL §10).
package role

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs the `wip role` parent command and its verbs.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "spawn and close role instances bound to this worktree's dispatch (MODEL §6, D59)",
	}
	cmd.AddCommand(spawnCommand(streams), closeCommand(streams), listCommand(streams), activeCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// openCurrent opens the store and resolves the full tier context of the
// working directory.
func openCurrent(cmd *cobra.Command, actor store.Actor) (*store.Store, render.Current, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, render.Current{}, err
	}
	s, err := tiers.OpenStore()
	if err != nil {
		return nil, render.Current{}, err
	}
	cur, err := render.ResolveCurrent(cmd.Context(), s, actor, dir)
	if err != nil {
		_ = s.Close()
		return nil, render.Current{}, err
	}
	return s, cur, nil
}

// roleJSON is the one JSON shape all three verbs speak.
type roleJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Dispatch    string `json:"dispatch"`
	Open        bool   `json:"open"`
	SpawnedAt   string `json:"spawned_at"`
	CloseReason string `json:"close_reason,omitempty"`
	ClosedAt    string `json:"closed_at,omitempty"`
}

func toJSON(r store.Role) roleJSON {
	out := roleJSON{
		ID:          r.ID,
		Name:        string(r.Name),
		Dispatch:    r.Dispatch,
		Open:        r.Open,
		SpawnedAt:   r.SpawnedAt.UTC().Format(time.RFC3339),
		CloseReason: string(r.CloseReason),
	}
	if r.ClosedAt != nil {
		out.ClosedAt = r.ClosedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func activeCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "active",
		Short: "show which outer-loop roles this repo's gate config activates (D14) — read-only, emits no event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			s, err := tiers.OpenStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}
			declared, err := s.GateDeclarations(cmd.Context(), repo.ID)
			if err != nil {
				return err
			}
			activation := writesurface.Activation(declared)

			if flags.JSON {
				type activationJSON struct {
					Role     string   `json:"role"`
					Owns     []string `json:"owns"`
					Declared []string `json:"declared"`
					Active   bool     `json:"active"`
					Inert    bool     `json:"inert"`
				}
				out := make([]activationJSON, 0, len(activation))
				for _, a := range activation {
					out = append(out, activationJSON{
						Role: string(a.Role), Owns: a.Owns, Declared: a.Declared,
						Active: a.Active, Inert: a.Inert,
					})
				}
				b, err := json.Marshal(out)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			for _, a := range activation {
				state := "dormant"
				switch {
				case a.Active:
					state = "active"
				case a.Inert && len(a.Declared) > 0:
					state = "inert until the forge seam (Phase 4)"
				}
				if _, err := fmt.Fprintf(streams.Out, "%-10s owns %-20s declared: %-12s %s\n",
					a.Role, strings.Join(a.Owns, ", "), strings.Join(a.Declared, ", "), state); err != nil {
					return err
				}
			}
			return nil
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func spawnCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spawn <role-name>",
		Short: "spawn a role instance into this worktree's open dispatch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			actor := store.ActorFor(flags.AsRole)
			s, cur, err := openCurrent(cmd, actor)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			r, err := writesurface.SpawnRole(cmd.Context(), s, actor, cur.Env(), store.RoleName(args[0]))
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(toJSON(r))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "spawned %s (%s) on dispatch %s\n", r.Name, r.ID, r.Dispatch)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func closeCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <role-name>",
		Short: "close this dispatch's open instance of a role, with reason completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, cur, err := openCurrent(cmd, store.ActorFor(flags.AsRole))
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			r, err := writesurface.CloseRole(cmd.Context(), s, cur.Env(), store.RoleName(args[0]))
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(toJSON(r))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "closed %s (%s)\n", r.Name, r.CloseReason)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func listCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list this worktree's dispatch roles, open and closed — read-only, emits no event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, cur, err := openCurrent(cmd, store.ActorFor(flags.AsRole))
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			dispatch, found, err := s.OpenDispatch(cmd.Context(), cur.Worktree.ID)
			if err != nil {
				return err
			}
			if !found {
				return wiperr.New("validation.no-open-dispatch",
					"no open dispatch on this worktree; run `wip refresh` first")
			}
			roles, err := s.RolesForDispatch(cmd.Context(), dispatch.ID)
			if err != nil {
				return err
			}

			if flags.JSON {
				out := struct {
					Dispatch string     `json:"dispatch"`
					Roles    []roleJSON `json:"roles"`
				}{Dispatch: dispatch.ID, Roles: []roleJSON{}}
				for _, r := range roles {
					out.Roles = append(out.Roles, toJSON(r))
				}
				b, err := json.Marshal(out)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if len(roles) == 0 {
				_, err = fmt.Fprintf(streams.Out, "no roles on dispatch %s\n", dispatch.ID)
				return err
			}
			for _, r := range roles {
				state := "open"
				if !r.Open {
					state = string(r.CloseReason)
				}
				if _, err := fmt.Fprintf(streams.Out, "%-12s %s · %s\n", r.Name, r.ID, state); err != nil {
					return err
				}
			}
			return nil
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
