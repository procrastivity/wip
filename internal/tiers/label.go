package tiers

import "path/filepath"

// DefaultLabel is a Clone's label at `wip init` time: the clone directory's
// basename (tiers Brief, "Labels and addressing").
func DefaultLabel(dir string) string {
	return filepath.Base(filepath.Clean(dir))
}

// SuggestLabel runs the collision-suggestion algorithm the Brief fixes: try
// the parent directory's name first, then `<basename>-<parent>` if that also
// collides. It is a pure function of the path and the taken set — it does
// not depend on registration order (D51) — and it never invents a third
// form: the false result means both attempts also collide, and the caller
// hard-errors rather than falling back to anything else (no `-2`, `-3`,
// ever). The same algorithm serves two callers: `wip init` runs it silently
// to resolve the default label automatically, and `wip label` runs it only
// to name the one alternative it proposes in its refusal.
func SuggestLabel(dir string, taken map[string]bool) (string, bool) {
	clean := filepath.Clean(dir)
	base := filepath.Base(clean)
	parent := filepath.Base(filepath.Dir(clean))

	if parent != "" && parent != "." && parent != base && !taken[parent] {
		return parent, true
	}
	combined := base + "-" + parent
	if !taken[combined] {
		return combined, true
	}
	return "", false
}
