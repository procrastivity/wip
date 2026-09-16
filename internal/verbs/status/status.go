// Package status implements the `status` pair (D112): `wip plumbing status`
// is the canonical member — the agent-grade contract with the full human
// render, `--all`, and `--json`, unchanged since `tiers/tier-verbs`
// step-08 built the tier-scoped stub and `read-surface` filled it with the
// founding "what is in progress, what is finished, what is next to start"
// content (MODEL §1). `wip status` is the porcelain member — the split
// pair's small, pretty, human-sized face: a minimal working set by
// default, `--full` for every section, and the identical `--json` payload.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
)

// Command constructs `wip plumbing status` — the pair's canonical member,
// keeping the agent-grade contract (`--all`, `--json`, the full render).
func Command(streams *iostreams.Streams) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "show what's in progress, finished, and next to start",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())

			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			d, err := gatherStatus(cmd.Context(), store.ActorFor(flags.AsRole), dir, all)
			if err != nil {
				return err
			}
			defer func() { _ = d.s.Close() }()

			if flags.JSON {
				return renderJSON(cmd.Context(), streams, d.s.View, d.view, d.content, d.cursor)
			}
			return renderHuman(cmd.Context(), streams, d.s.View, d.view, d.content, d.cursor)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show every finished node: expand sealed subtrees and include old sealed matters")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// PorcelainCommand constructs `wip status` — the split pair's human-sized
// member. Its deliverable is the working set a human checks: what is in
// progress and what is next. `--full` widens it to every section (still
// minus the clone/worktree enumeration — tier internals that belong to the
// plumbing member's render). `--json` emits the identical payload the
// plumbing member emits: one JSON contract for the concept, and scripts
// are pointed at the plumbing spelling anyway. `--all` is deliberately
// absent — audit expansion is the plumbing member's knob.
func PorcelainCommand(streams *iostreams.Streams) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "what's in progress and what's next — the human-sized digest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())

			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			d, err := gatherStatus(cmd.Context(), store.ActorFor(flags.AsRole), dir, false)
			if err != nil {
				return err
			}
			defer func() { _ = d.s.Close() }()

			if flags.JSON {
				return renderJSON(cmd.Context(), streams, d.s.View, d.view, d.content, d.cursor)
			}
			if full {
				return renderFull(cmd.Context(), streams, d.s.View, d.view, d.content, d.cursor)
			}
			return renderDigest(cmd.Context(), streams, d.s.View, d.view, d.content, d.cursor)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "every section — finished and blocked too (human output; --json always emits the complete payload)")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// statusData is the gather step both members share: the tier-scoped view,
// the per-repo founding-question content computed over it, the cursor node
// to mark, and the open store the renderers still read through (Address is
// a view query). Callers close s when the render returns.
type statusData struct {
	s       *store.Store
	view    tiers.StatusView
	content map[string]readsurface.RepoContent
	cursor  string
}

// gatherStatus collects everything either member renders. status carries
// no cursor dependency (the tiers/read-surface Briefs both say so): the
// durable content is identical from any clone. Marking the current clone's
// cursor node is orientation only, so a clone/worktree this command can't
// resolve (an un-init'd linked worktree, or the host-wide case) just means
// nothing gets marked — never a status failure.
func gatherStatus(ctx context.Context, actor store.Actor, dir string, all bool) (*statusData, error) {
	s, err := tiers.OpenStore()
	if err != nil {
		return nil, err
	}
	view, err := tiers.Status(ctx, s, actor, dir)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	var cursorNode string
	if cur, err := readsurface.ResolveCurrent(ctx, s, actor, dir); err == nil {
		if node, set, err := readsurface.Cursor(ctx, s.View, cur); err == nil && set {
			cursorNode = node
		}
	}
	content, err := contentByRepo(ctx, s, view, readsurface.ContentOptions{All: all, Cursor: cursorNode})
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return &statusData{s: s, view: view, content: content, cursor: cursorNode}, nil
}

// contentByRepo computes read-surface's founding-question content for every
// Repo the tier-scoped view is about to render — the single current Repo, or
// every Repo on the host in the host-wide case.
func contentByRepo(ctx context.Context, s *store.Store, view tiers.StatusView, opts readsurface.ContentOptions) (map[string]readsurface.RepoContent, error) {
	out := map[string]readsurface.RepoContent{}
	repos := view.Repos
	if !view.HostWide {
		repos = []tiers.RepoStatus{view.Repo}
	}
	for _, rs := range repos {
		c, err := readsurface.Content(ctx, s.View, rs.Repo.ID, opts)
		if err != nil {
			return nil, err
		}
		out[rs.Repo.ID] = c
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// JSON rendering
// ---------------------------------------------------------------------------

type cloneJSON struct {
	ID        string      `json:"id"`
	Label     string      `json:"label"`
	Current   bool        `json:"current"`
	Worktrees []worktreeJ `json:"worktrees,omitempty"`
}

type worktreeJ struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

type nodeJSON struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Kind      string `json:"kind"`
	Lifecycle string `json:"lifecycle"`
	Cursor    bool   `json:"cursor,omitempty"`
}

type finishedJSON struct {
	nodeJSON
	LocallyComplete bool `json:"locallyComplete"`
	Sealed          bool `json:"sealed"`
}

type blockedJSON struct {
	nodeJSON
	BlockedBy []nodeJSON `json:"blockedBy"`
}

type contentJSON struct {
	InProgress []nodeJSON     `json:"inProgress"`
	Finished   []finishedJSON `json:"finished"`
	Ready      []nodeJSON     `json:"ready"`
	Blocked    []blockedJSON  `json:"blocked"`
	// HiddenSealedMatters mirrors RepoContent.HiddenSealedMatters: sealed
	// Matters the default (non `--all`) view hid for recency.
	HiddenSealedMatters int `json:"hiddenSealedMatters,omitempty"`
}

type repoJSON struct {
	ID      string      `json:"id"`
	Header  string      `json:"header"`
	Clones  []cloneJSON `json:"clones"`
	Content contentJSON `json:"content"`
}

func toNodeJSON(n store.Node, address string, cursorNode string) nodeJSON {
	return nodeJSON{
		ID: n.ID, Address: address, Kind: string(n.Kind), Lifecycle: string(n.Lifecycle),
		Cursor: cursorNode != "" && n.ID == cursorNode,
	}
}

func toContentJSON(ctx context.Context, v store.View, c readsurface.RepoContent, cursorNode string) (contentJSON, error) {
	out := contentJSON{}
	for _, n := range c.InProgress {
		addr, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return contentJSON{}, err
		}
		out.InProgress = append(out.InProgress, toNodeJSON(n, addr, cursorNode))
	}
	for _, f := range c.Finished {
		addr, _, err := readsurface.Address(ctx, v, f.Node)
		if err != nil {
			return contentJSON{}, err
		}
		out.Finished = append(out.Finished, finishedJSON{
			nodeJSON: toNodeJSON(f.Node, addr, cursorNode), LocallyComplete: f.LocallyComplete, Sealed: f.Sealed,
		})
	}
	for _, n := range c.Ready {
		addr, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return contentJSON{}, err
		}
		out.Ready = append(out.Ready, toNodeJSON(n, addr, cursorNode))
	}
	for _, b := range c.Blocked {
		addr, _, err := readsurface.Address(ctx, v, b.Node)
		if err != nil {
			return contentJSON{}, err
		}
		bj := blockedJSON{nodeJSON: toNodeJSON(b.Node, addr, cursorNode)}
		for _, blocker := range b.Blockers {
			blockerAddr, _, err := readsurface.Address(ctx, v, blocker)
			if err != nil {
				return contentJSON{}, err
			}
			bj.BlockedBy = append(bj.BlockedBy, toNodeJSON(blocker, blockerAddr, cursorNode))
		}
		out.Blocked = append(out.Blocked, bj)
	}
	out.HiddenSealedMatters = c.HiddenSealedMatters
	return out, nil
}

func toRepoJSON(ctx context.Context, v store.View, rs tiers.RepoStatus, content map[string]readsurface.RepoContent, cursorNode string) (repoJSON, error) {
	out := repoJSON{ID: rs.Repo.ID, Header: tiers.RepoHeader(rs.Repo)}
	for _, c := range rs.Clones {
		cj := cloneJSON{ID: c.Clone.ID, Label: c.Clone.Label, Current: c.Current}
		for _, w := range c.Worktrees {
			cj.Worktrees = append(cj.Worktrees, worktreeJ{ID: w.Worktree.ID, Name: w.Worktree.Name, Current: w.Current})
		}
		out.Clones = append(out.Clones, cj)
	}
	cj, err := toContentJSON(ctx, v, content[rs.Repo.ID], cursorNode)
	if err != nil {
		return repoJSON{}, err
	}
	out.Content = cj
	return out, nil
}

func renderJSON(ctx context.Context, streams *iostreams.Streams, v store.View, view tiers.StatusView, content map[string]readsurface.RepoContent, cursorNode string) error {
	if view.HostWide {
		repos := make([]repoJSON, 0, len(view.Repos))
		for _, rs := range view.Repos {
			rj, err := toRepoJSON(ctx, v, rs, content, cursorNode)
			if err != nil {
				return err
			}
			repos = append(repos, rj)
		}
		b, err := json.Marshal(struct {
			HostWide bool       `json:"hostWide"`
			Repos    []repoJSON `json:"repos"`
		}{HostWide: true, Repos: repos})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}
	rj, err := toRepoJSON(ctx, v, view.Repo, content, cursorNode)
	if err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		HostWide bool     `json:"hostWide"`
		Repo     repoJSON `json:"repo"`
	}{HostWide: false, Repo: rj})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Out, string(b))
	return err
}

// ---------------------------------------------------------------------------
// Porcelain human rendering — the split pair's small face.
// ---------------------------------------------------------------------------

// renderDigest is the porcelain member's default view: the working set a
// human checks — what is in progress and what is next. Rows carry no
// lifecycle word (the section header already states it); finished and
// blocked never list — when the elided sections hold anything, one footer
// line counts them and names `--full`.
func renderDigest(ctx context.Context, streams *iostreams.Streams, v store.View, view tiers.StatusView, content map[string]readsurface.RepoContent, cursorNode string) error {
	if view.HostWide {
		if len(view.Repos) == 0 {
			_, err := fmt.Fprintln(streams.Out, "no repos known to wip on this host — run `wip init` in a clone")
			return err
		}
		for i, rs := range view.Repos {
			if i > 0 {
				if _, err := fmt.Fprintln(streams.Out); err != nil {
					return err
				}
			}
			if err := renderRepoDigest(ctx, streams, v, rs, content[rs.Repo.ID], cursorNode); err != nil {
				return err
			}
		}
		return nil
	}
	return renderRepoDigest(ctx, streams, v, view.Repo, content[view.Repo.Repo.ID], cursorNode)
}

func renderRepoDigest(ctx context.Context, streams *iostreams.Streams, v store.View, rs tiers.RepoStatus, c readsurface.RepoContent, cursorNode string) error {
	if _, err := fmt.Fprintln(streams.Out, tiers.RepoHeader(rs.Repo)); err != nil {
		return err
	}

	printed := false
	cursorVisible := false
	for _, sec := range []struct {
		title string
		nodes []store.Node
	}{
		{"in progress", c.InProgress},
		{"next to start", c.Ready},
	} {
		if len(sec.nodes) == 0 {
			continue
		}
		lines, err := slimNodeLines(ctx, v, sec.nodes, cursorNode)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(streams.Out, "\n%s:\n", sec.title); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := fmt.Fprintln(streams.Out, l); err != nil {
				return err
			}
		}
		printed = true
		for _, n := range sec.nodes {
			if n.ID == cursorNode {
				cursorVisible = true
				break
			}
		}
	}
	if !cursorVisible && cursorNode != "" {
		var cursorLines []string
		for _, f := range c.Finished {
			if f.Node.ID == cursorNode {
				lines, err := finishedLines(ctx, v, []readsurface.Finished{f}, cursorNode)
				if err != nil {
					return err
				}
				cursorLines = lines
				break
			}
		}
		if len(cursorLines) == 0 {
			for _, b := range c.Blocked {
				if b.Node.ID == cursorNode {
					lines, err := blockedLines(ctx, v, []readsurface.Blocked{b}, cursorNode)
					if err != nil {
						return err
					}
					cursorLines = lines
					break
				}
			}
		}
		if len(cursorLines) > 0 {
			if _, err := fmt.Fprintln(streams.Out, "\ncursor:"); err != nil {
				return err
			}
			for _, l := range cursorLines {
				if _, err := fmt.Fprintln(streams.Out, l); err != nil {
					return err
				}
			}
			printed = true
		}
	}
	if !printed {
		if _, err := fmt.Fprintln(streams.Out, "\nnothing in progress, nothing unblocked"); err != nil {
			return err
		}
	}

	var elided []string
	if n := len(c.Finished) + c.HiddenSealedMatters; n > 0 {
		elided = append(elided, fmt.Sprintf("%d finished", n))
	}
	if len(c.Blocked) > 0 {
		elided = append(elided, fmt.Sprintf("%d blocked", len(c.Blocked)))
	}
	if len(elided) > 0 {
		if _, err := fmt.Fprintf(streams.Out, "\n… %s — wip status --full\n", strings.Join(elided, " · ")); err != nil {
			return err
		}
	}
	return nil
}

// renderFull is the porcelain member's `--full` view: every section, still
// human-sized — the plumbing member's section set and collapsing rules,
// minus the clone/worktree enumeration and the lifecycle words the section
// headers already carry.
func renderFull(ctx context.Context, streams *iostreams.Streams, v store.View, view tiers.StatusView, content map[string]readsurface.RepoContent, cursorNode string) error {
	if view.HostWide {
		if len(view.Repos) == 0 {
			_, err := fmt.Fprintln(streams.Out, "no repos known to wip on this host — run `wip init` in a clone")
			return err
		}
		for i, rs := range view.Repos {
			if i > 0 {
				if _, err := fmt.Fprintln(streams.Out); err != nil {
					return err
				}
			}
			if err := renderRepoFull(ctx, streams, v, rs, content[rs.Repo.ID], cursorNode); err != nil {
				return err
			}
		}
		return nil
	}
	return renderRepoFull(ctx, streams, v, view.Repo, content[view.Repo.Repo.ID], cursorNode)
}

func renderRepoFull(ctx context.Context, streams *iostreams.Streams, v store.View, rs tiers.RepoStatus, c readsurface.RepoContent, cursorNode string) error {
	if _, err := fmt.Fprintln(streams.Out, tiers.RepoHeader(rs.Repo)); err != nil {
		return err
	}

	sections := []struct {
		title string
		lines func() ([]string, error)
	}{
		{"in progress", func() ([]string, error) { return slimNodeLines(ctx, v, c.InProgress, cursorNode) }},
		{"finished", func() ([]string, error) {
			lines, err := finishedLines(ctx, v, c.Finished, cursorNode)
			if err != nil {
				return nil, err
			}
			if c.HiddenSealedMatters > 0 {
				lines = append(lines, hiddenSealedLine(c.HiddenSealedMatters))
			}
			return lines, nil
		}},
		{"next to start", func() ([]string, error) { return slimNodeLines(ctx, v, c.Ready, cursorNode) }},
		{"blocked", func() ([]string, error) { return blockedLines(ctx, v, c.Blocked, cursorNode) }},
	}
	for _, sec := range sections {
		lines, err := sec.lines()
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(streams.Out, "\n%s:\n", sec.title); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := fmt.Fprintln(streams.Out, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// slimNodeLines renders in-progress/next-to-start rows for the porcelain
// member: address, kind, and the cursor mark — no lifecycle word, which the
// section header already carries.
func slimNodeLines(ctx context.Context, v store.View, nodes []store.Node, cursorNode string) ([]string, error) {
	var out []string
	for _, n := range nodes {
		addr, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("  %-24s %s%s", addr, n.Kind, cursorSuffix(n.ID, cursorNode)))
	}
	return out, nil
}

// hiddenSealedLine is the one shared "more sealed" pointer both members'
// finished sections end with. It names `wip plumbing status --all` — the
// porcelain member has no --all flag, so the expansion lives at the
// plumbing spelling.
func hiddenSealedLine(n int) string {
	return fmt.Sprintf("  … %d more sealed matter(s) · wip plumbing status --all", n)
}

// ---------------------------------------------------------------------------
// Plumbing-member human rendering
// ---------------------------------------------------------------------------

func renderHuman(ctx context.Context, streams *iostreams.Streams, v store.View, view tiers.StatusView, content map[string]readsurface.RepoContent, cursorNode string) error {
	if view.HostWide {
		if len(view.Repos) == 0 {
			_, err := fmt.Fprintln(streams.Out, "no repos known to wip on this host — run `wip init` in a clone")
			return err
		}
		for i, rs := range view.Repos {
			if i > 0 {
				if _, err := fmt.Fprintln(streams.Out); err != nil {
					return err
				}
			}
			if err := renderRepo(ctx, streams, v, rs, content[rs.Repo.ID], cursorNode); err != nil {
				return err
			}
		}
		return nil
	}
	return renderRepo(ctx, streams, v, view.Repo, content[view.Repo.Repo.ID], cursorNode)
}

func renderRepo(ctx context.Context, streams *iostreams.Streams, v store.View, rs tiers.RepoStatus, c readsurface.RepoContent, cursorNode string) error {
	if _, err := fmt.Fprintln(streams.Out, tiers.RepoHeader(rs.Repo)); err != nil {
		return err
	}
	for _, cl := range rs.Clones {
		line := fmt.Sprintf("  %-18s Clone", cl.Clone.Label)
		if cl.Current {
			line += " · current"
		}
		if _, err := fmt.Fprintln(streams.Out, line); err != nil {
			return err
		}
		for _, w := range cl.Worktrees {
			wline := fmt.Sprintf("  %-18s Worktree (of %s)", w.Worktree.Name, cl.Clone.Label)
			if w.Current {
				wline += " · current"
			}
			if _, err := fmt.Fprintln(streams.Out, wline); err != nil {
				return err
			}
		}
	}

	sections := []struct {
		title string
		lines func() ([]string, error)
	}{
		{"in progress", func() ([]string, error) { return nodeLines(ctx, v, c.InProgress, cursorNode) }},
		{"finished", func() ([]string, error) {
			lines, err := finishedLines(ctx, v, c.Finished, cursorNode)
			if err != nil {
				return nil, err
			}
			if c.HiddenSealedMatters > 0 {
				lines = append(lines, hiddenSealedLine(c.HiddenSealedMatters))
			}
			return lines, nil
		}},
		{"next to start", func() ([]string, error) { return nodeLines(ctx, v, c.Ready, cursorNode) }},
		{"blocked", func() ([]string, error) { return blockedLines(ctx, v, c.Blocked, cursorNode) }},
	}
	for _, sec := range sections {
		lines, err := sec.lines()
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(streams.Out, "\n%s:\n", sec.title); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := fmt.Fprintln(streams.Out, l); err != nil {
				return err
			}
		}
	}
	return nil
}

func nodeLines(ctx context.Context, v store.View, nodes []store.Node, cursorNode string) ([]string, error) {
	var out []string
	for _, n := range nodes {
		addr, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("  %-24s %s · %s%s", addr, n.Kind, n.Lifecycle, cursorSuffix(n.ID, cursorNode)))
	}
	return out, nil
}

func finishedLines(ctx context.Context, v store.View, finished []readsurface.Finished, cursorNode string) ([]string, error) {
	var out []string
	for _, f := range finished {
		addr, _, err := readsurface.Address(ctx, v, f.Node)
		if err != nil {
			return nil, err
		}
		state := "awaiting gate"
		switch {
		case f.Sealed:
			state = "sealed"
		case f.LocallyComplete:
			state = "locally complete"
		}
		out = append(out, fmt.Sprintf("  %-24s %s · %s%s", addr, f.Node.Kind, state, cursorSuffix(f.Node.ID, cursorNode)))
	}
	return out, nil
}

func blockedLines(ctx context.Context, v store.View, blocked []readsurface.Blocked, cursorNode string) ([]string, error) {
	var out []string
	for _, b := range blocked {
		addr, _, err := readsurface.Address(ctx, v, b.Node)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(b.Blockers))
		for _, blocker := range b.Blockers {
			blockerAddr, _, err := readsurface.Address(ctx, v, blocker)
			if err != nil {
				return nil, err
			}
			names = append(names, blockerAddr)
		}
		out = append(out, fmt.Sprintf("  %-24s blocked-by: %s%s", addr, joinComma(names), cursorSuffix(b.Node.ID, cursorNode)))
	}
	return out, nil
}

func cursorSuffix(id, cursorNode string) string {
	if cursorNode != "" && id == cursorNode {
		return " · cursor"
	}
	return ""
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
