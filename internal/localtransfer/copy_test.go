package localtransfer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturePreservesCommittedWALAndOriginalFiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE work(value TEXT); INSERT INTO work VALUES ('only in WAL')"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no committed WAL: %v", err)
	}
	blobs := filepath.Join(filepath.Dir(path), "blobs")
	if err := os.Mkdir(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, "segment"), []byte("private content"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "captured")
	got, err := Capture(ctx, path, destination)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint(path)
	if err != nil || before != after || got != before {
		t.Fatalf("original changed: before=%s after=%s captured=%s err=%v", before, after, got, err)
	}
	copyDB, err := sql.Open("sqlite", filepath.Join(destination, "wip.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copyDB.Close() }()
	var value string
	if err := copyDB.QueryRow("SELECT value FROM work").Scan(&value); err != nil || value != "only in WAL" {
		t.Fatalf("lost committed WAL row: %q %v", value, err)
	}
	bytes, err := os.ReadFile(filepath.Join(destination, "blobs", "segment"))
	if err != nil || string(bytes) != "private content" {
		t.Fatalf("lost sibling blob: %q %v", bytes, err)
	}
}

func TestCaptureRefusesLinkedBlobsAndExistingDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE work(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	dest := t.TempDir()
	if _, err := Capture(context.Background(), path, dest); err == nil {
		t.Fatal("overwrote existing capture directory")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(filepath.Dir(path), "blobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), path, filepath.Join(dest, "copy")); err == nil {
		t.Fatal("followed linked blob directory")
	}
}
