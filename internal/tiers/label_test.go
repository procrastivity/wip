package tiers

import "testing"

func TestDefaultLabel(t *testing.T) {
	if got := DefaultLabel("/home/beau/code/widget-a"); got != "widget-a" {
		t.Errorf("DefaultLabel = %q, want %q", got, "widget-a")
	}
	if got := DefaultLabel("/home/beau/code/widget-a/"); got != "widget-a" {
		t.Errorf("DefaultLabel with trailing slash = %q, want %q", got, "widget-a")
	}
}

// TestSuggestLabel_ParentDirTier is the collision-suggestion algorithm's
// first tier: the parent directory's name.
func TestSuggestLabel_ParentDirTier(t *testing.T) {
	got, ok := SuggestLabel("/home/beau/code/widget-a", map[string]bool{"widget-a": true})
	if !ok {
		t.Fatal("SuggestLabel reported no suggestion, want the parent-dir tier")
	}
	if got != "code" {
		t.Errorf("SuggestLabel = %q, want the parent dir name %q", got, "code")
	}
}

// TestSuggestLabel_BasenameParentTier is the second tier, reached when the
// parent-dir name is also taken.
func TestSuggestLabel_BasenameParentTier(t *testing.T) {
	got, ok := SuggestLabel("/home/beau/code/widget-a", map[string]bool{"widget-a": true, "code": true})
	if !ok {
		t.Fatal("SuggestLabel reported no suggestion, want the basename-parent tier")
	}
	if got != "widget-a-code" {
		t.Errorf("SuggestLabel = %q, want %q", got, "widget-a-code")
	}
}

// TestSuggestLabel_Exhausted asserts the Brief's "no -2, -3, ever": when both
// tiers collide, SuggestLabel reports no suggestion rather than inventing a
// third form.
func TestSuggestLabel_Exhausted(t *testing.T) {
	_, ok := SuggestLabel("/home/beau/code/widget-a", map[string]bool{
		"widget-a": true, "code": true, "widget-a-code": true,
	})
	if ok {
		t.Fatal("SuggestLabel found a suggestion after both tiers collided; want exhaustion (never auto-suffix)")
	}
}

// TestSuggestLabel_PureFunctionOfPathAndTakenSet confirms the suggestion
// does not depend on registration order (D51) — only the path and the
// current taken set.
func TestSuggestLabel_PureFunctionOfPathAndTakenSet(t *testing.T) {
	first, ok1 := SuggestLabel("/a/b/widget", map[string]bool{"widget": true})
	second, ok2 := SuggestLabel("/a/b/widget", map[string]bool{"widget": true})
	if !ok1 || !ok2 || first != second {
		t.Errorf("SuggestLabel is not deterministic: (%q,%v) vs (%q,%v)", first, ok1, second, ok2)
	}
}
