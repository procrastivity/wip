package writesurface

// Stage content-prose: the four content verbs. This Stage fixes only their
// verb names, the create-once-vs-append property, the one-event-per-write
// property, and `payload.kind` discrimination (CONTRACT §B) — it does not
// decide argument shape (argument/stdin/scratch-file, D45), which the
// `agent-path` workplan resolves: stdin (default) or `--file <path>` for
// `wip brief`/`wip workplan`/`wip body`, and a positional argument (default)
// plus stdin/`--file` for `wip finding add` (agent-path step-01). That shape
// is implemented at the verb layer (internal/verbs/content); this file only
// carries the store-facing half — create-once refusal and the accumulating
// append — which is agnostic to how the bytes arrived.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// WriteOnce writes a create-once content kind (brief, workplan, body). A
// second call against a node that already carries live content of this kind
// is refused rather than silently overwriting — edit-in-place is deferred
// until earned (write-surface content-prose step-01).
func WriteOnce(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string, kind store.ContentKind, data []byte) (store.Node, error) {
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	existing, err := s.ContentSegments(ctx, n.ID, kind)
	if err != nil {
		return store.Node{}, err
	}
	if len(existing) > 0 {
		return store.Node{}, wiperr.New("validation.content-already-written",
			fmt.Sprintf("%s already has %s content, written once and never rewritten", locator, kind))
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		draft, err := tx.ContentDraft(n.ID, kind, data)
		if err != nil {
			return nil, err
		}
		return []store.Draft{draft}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return n, nil
}

// AppendFinding writes one more `findings` segment. Each call accumulates —
// the append counterpart to create-once — and never replaces a prior entry.
func AppendFinding(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string, data []byte) (store.Node, error) {
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		draft, err := tx.ContentDraft(n.ID, store.KindFindings, data)
		if err != nil {
			return nil, err
		}
		return []store.Draft{draft}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return n, nil
}
