// Package writesurface implements the write-surface Matter: every verb that
// changes wip's state — birth and amendment of nodes, content prose,
// lifecycle, backlog, gates, dependencies, and the (inert) external-reference
// bind. It builds strictly against three Briefs it consumes and originates
// none of its own (write-surface earns no Brief, per its own workplan): the
// `chassis` Brief (registration conventions), the `vocabulary` Matter's
// drafted output (verb names, refusal messages, the no-bespoke-`review`-verb
// constraint), and the `schema` Brief (the entity model, the event envelope,
// and the one write path — `Commit`/Drafts/`Cause`/`ErrNoEvent`).
//
// Every verb here commits through store.Store.Commit; none writes directly
// (schema's write path already makes that structural, not this package's
// discipline). This package owns no storage of its own, exactly as `tiers`
// does not: it reads and writes exclusively through internal/store's
// data-access layer.
package writesurface

import (
	"context"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
)

// CurrentRepo resolves the Repo the current directory's clone belongs to,
// refusing exactly as every other tier-resolving verb does (MODEL §11: an
// unknown clone is a hard failure, never a guess) — the vocabulary-ratified
// `refusal.unknown-clone` message (vocabulary step-10), reused verbatim.
func CurrentRepo(ctx context.Context, s *store.Store, dir string) (store.Repo, error) {
	clone, found, err := tiers.ResolveCurrentClone(ctx, s, store.ActorHuman, dir)
	if err != nil {
		return store.Repo{}, err
	}
	if !found {
		return store.Repo{}, wiperr.New("refusal.unknown-clone",
			"refused — this clone is unknown to wip; run `wip init` here first (a write; if you cannot run it, ask the user)")
	}
	return s.Repo(ctx, clone.Repo)
}

// slugify renders a title as a locator: lowercase, alphanumerics kept,
// everything else collapsed to a single hyphen, leading/trailing hyphens
// trimmed. It is how a Matter or Stage's own locator is derived from its
// title flag (write-surface step-02: "a positional parent locator where one
// applies... plus a name/title flag" — the birthed node's own locator is
// not a second positional, it is this function of the title).
func slugify(title string) string {
	var b strings.Builder
	prevDash := true // true so a leading run of non-alnum characters is dropped, not hyphenated
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
