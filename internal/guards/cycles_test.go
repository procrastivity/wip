package guards_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/guards"
)

// TestCheckCycles_ReportsEveryCycleNotJustTheFirst is the D65 case guards.md
// asks for explicitly: two cycles sharing an edge, both reported — the
// lesson of schema's own Cycles bug, whose five named test cases all passed
// against an implementation that reported one loop per tangle.
func TestCheckCycles_ReportsEveryCycleNotJustTheFirst(t *testing.T) {
	s := newStore(t)
	repo := newRepo(t, s)

	a := matter(t, s, repo, "render-scratch")
	b := matter(t, s, repo, "write-surface")
	c := matter(t, s, repo, "guards")

	// a waits for b, b waits for a (a two-node loop), and separately c waits
	// for b, b waits for c — sharing the edges out of b.
	addEdge(t, s, repo, a, b)
	addEdge(t, s, repo, b, a)
	addEdge(t, s, repo, c, b)
	addEdge(t, s, repo, b, c)

	findings, err := guards.CheckCycles(ctx, s, "")
	if err != nil {
		t.Fatalf("CheckCycles: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want exactly 2 (one per cycle)", findings)
	}
	for _, f := range findings {
		if f.Code != "refusal.blocked-by-cycle" {
			t.Errorf("finding code = %q, want %q", f.Code, "refusal.blocked-by-cycle")
		}
	}
	want := map[string]bool{
		"found: a blocked-by cycle — render-scratch → write-surface → render-scratch": false,
		"found: a blocked-by cycle — write-surface → guards → write-surface":          false,
	}
	for _, f := range findings {
		if _, ok := want[f.Message]; !ok {
			t.Errorf("unexpected finding message %q", f.Message)
			continue
		}
		want[f.Message] = true
	}
	for msg, seen := range want {
		if !seen {
			t.Errorf("expected finding %q was not reported", msg)
		}
	}
}

func TestCheckCycles_CleanStoreReportsNothing(t *testing.T) {
	s := newStore(t)
	repo := newRepo(t, s)
	matter(t, s, repo, "no-cycles-here")

	findings, err := guards.CheckCycles(ctx, s, "")
	if err != nil {
		t.Fatalf("CheckCycles: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none", findings)
	}
}
