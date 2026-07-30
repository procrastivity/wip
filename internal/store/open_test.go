package store

import (
	"context"
	"path/filepath"
	"testing"
)

// openTestStore opens a fresh store under t.TempDir(), so the blob sidecar
// directory lands beside it exactly as it does under $XDG_DATA_HOME.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestFreshStoreOpensAtTheBaselineVersion is the floor under everything else:
// the v1 baseline applies, the taxonomy is seeded, and the projection version is
// recorded.
func TestFreshStoreOpensAtTheBaselineVersion(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if got, want := s.SchemaVersion(), latestVersion(register); got != want {
		t.Errorf("schema version = %d, want %d", got, want)
	}

	var types int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_types`).Scan(&types); err != nil {
		t.Fatalf("count event types: %v", err)
	}
	if got, want := types, len(P1Taxonomy); got != want {
		t.Errorf("seeded %d event types, want %d", got, want)
	}

	recorded, present, err := s.meta(ctx, projectionVersionKey)
	if err != nil {
		t.Fatalf("read projection version: %v", err)
	}
	if !present || recorded != "1" {
		t.Errorf("projection version = %q (present %v), want \"1\"", recorded, present)
	}

	// An empty store answers the founding question with nothing, not an error.
	inProgress, err := s.InProgress(ctx)
	if err != nil {
		t.Fatalf("in progress: %v", err)
	}
	if len(inProgress) != 0 {
		t.Errorf("a fresh store reports %d nodes in progress, want 0", len(inProgress))
	}
}
