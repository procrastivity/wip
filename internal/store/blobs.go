package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReapOrphanBlobs implements D68's other orphan population: a spilled blob
// file no live content row references. It is `wip clean`'s to call, never the
// store's own initiative — Open never reaps, and neither does any read — and
// only past cutoff, a caller-supplied point in time rather than a duration,
// so an in-flight command's just-written blob (ContentDraft writes the file
// before its transaction commits, content.go's own doc explains why) is never
// mistaken for debris between its write and its commit.
//
// It returns the names of every blob it removed, for a caller to report.
func (s *Store) ReapOrphanBlobs(ctx context.Context, cutoff time.Time) ([]string, error) {
	live := map[string]bool{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT blob_ref FROM content WHERE blob_ref IS NOT NULL AND tombstone_event IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("store: read live blob references: %w", err)
	}
	func() {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var ref string
			if err == nil {
				err = rows.Scan(&ref)
			}
			if err == nil {
				live[ref] = true
			}
		}
	}()
	if err != nil {
		return nil, fmt.Errorf("store: read live blob references: %w", err)
	}

	entries, err := os.ReadDir(s.blobDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: read the blob directory %s: %w", s.blobDir, err)
	}

	var reaped []string
	for _, entry := range entries {
		if entry.IsDir() || live[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("store: stat blob %s: %w", entry.Name(), err)
		}
		if info.ModTime().After(cutoff) {
			continue // possibly still in flight — not this reap's business
		}
		path := filepath.Join(s.blobDir, entry.Name())
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("store: reap orphan blob %s: %w", path, err)
		}
		reaped = append(reaped, entry.Name())
	}
	return reaped, nil
}
