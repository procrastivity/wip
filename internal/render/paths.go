package render

import "path/filepath"

// The `.wip/` layout (MODEL §3.1, D41): exactly two halves, both disposable.
// Neither is durable; the test the whole package is disciplined by is D33 —
// deleting `.wip/` between dispatches must lose nothing, because nothing
// that must survive lives under either half.

// WipDir is `.wip/` at a worktree's root.
func WipDir(root string) string { return filepath.Join(root, ".wip") }

// GeneratedDir is `.wip/generated/`: the rendered projection, 0444, never
// parsed back (D36, D40).
func GeneratedDir(root string) string { return filepath.Join(WipDir(root), "generated") }

// WorkDir is `.wip/work/`: agent scratch, namespaced per dispatch.
func WorkDir(root string) string { return filepath.Join(WipDir(root), "work") }

// ScratchDir is one dispatch's own scratch directory,
// `.wip/work/<dispatch-id>/` — the path a launcher hands an agent at spawn
// (MODEL §3.1's accepted ergonomic cost: the agent never composes this
// itself).
func ScratchDir(root, dispatchID string) string { return filepath.Join(WorkDir(root), dispatchID) }
