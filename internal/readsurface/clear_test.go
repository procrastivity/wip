package readsurface

// Tests for `ClearCursor`/`clearCursor`: the explicit "leave what's next
// undecided" write D67's choose-next reframing adds beside `SetCursor` —
// mirroring next_test.go's own SetCursorForTest seam and countCursorMoves
// assertion style.

import (
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestClearCursor_SetThenClear_ClearsAndEmitsOneMove clears a cursor that
// was set, and checks the read side agrees (Cursor reports unset) and
// exactly one additional cursor.moved was appended, carrying an empty Node
// and the cleared target as Previous.
func TestClearCursor_SetThenClear_ClearsAndEmitsOneMove(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter")
	f.start(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}
	before := countCursorMoves(t, f)

	previous, err := clearCursor(ctx, f.Store, store.ActorHuman, cur)
	if err != nil {
		t.Fatal(err)
	}
	if previous != m {
		t.Errorf("previous = %q, want %q", previous, m)
	}

	node, set, err := Cursor(ctx, f.View, cur)
	if err != nil {
		t.Fatal(err)
	}
	if set || node != "" {
		t.Errorf("Cursor after clear = %q/%v, want unset", node, set)
	}

	after := countCursorMoves(t, f)
	if after != before+1 {
		t.Errorf("cursor.moved count = %d, want %d (exactly one new move)", after, before+1)
	}
}

// TestClearCursor_AlreadyClear_IsANoOp is the no-op contract: clearing a
// cursor that was never set (or already cleared) succeeds with an empty
// previous and appends no event.
func TestClearCursor_AlreadyClear_IsANoOp(t *testing.T) {
	f := newFixture(t)
	_ = f.matter("m", "A Matter")
	cur := f.current()
	before := countCursorMoves(t, f)

	previous, err := clearCursor(ctx, f.Store, store.ActorHuman, cur)
	if err != nil {
		t.Fatal(err)
	}
	if previous != "" {
		t.Errorf("previous = %q, want empty (nothing was set)", previous)
	}

	after := countCursorMoves(t, f)
	if after != before {
		t.Errorf("cursor.moved count changed from %d to %d, want no event for an already-clear cursor", before, after)
	}
}
