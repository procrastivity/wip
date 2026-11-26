package localtransfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/transfer"
)

// ExportPlan binds a captured archive to the explicitly selected source copy.
// Its sibling snapshot is retained so old-schema adaptation is not repeated
// with newly synthesized migration timestamps when the archive is written.
type ExportPlan struct {
	Version     int                `json:"version"`
	SourceDB    string             `json:"source_db"`
	Fingerprint string             `json:"source_fingerprint"`
	Archive     string             `json:"captured_archive"`
	Digest      string             `json:"digest"`
	Snapshot    *transfer.Snapshot `json:"snapshot"`
}

// SyncState discloses retained destination policy without making a sync pass.
type SyncState struct {
	State    string                      `json:"state"`
	Bindings []store.ExchangeBindingInfo `json:"bindings"`
	Notice   string                      `json:"notice"`
}

// ImportPlan binds reviewed work mappings to one captured target and archive.
type ImportPlan struct {
	Version        int                 `json:"version"`
	Archive        string              `json:"archive"`
	TargetDB       string              `json:"target_db"`
	Fingerprint    string              `json:"target_fingerprint"`
	OriginalSchema int                 `json:"original_schema"`
	TargetSchema   int                 `json:"target_schema"`
	AllowMigration bool                `json:"allow_migration"`
	Sync           SyncState           `json:"sync"`
	Renames        map[string]string   `json:"renames"`
	Work           *store.TransferPlan `json:"work"`
}

// ImportResult includes the durable receipt and the retained pre-import backup.
type ImportResult struct {
	Receipt *store.TransferReceipt `json:"receipt"`
	Backup  string                 `json:"backup,omitempty"`
	Notice  string                 `json:"notice"`
}

// PlanExport captures a frozen copy, expands selected closure, then writes a
// private immutable snapshot beside the review plan. No source is opened.
func PlanExport(ctx context.Context, source, repo string, selection transfer.Selection, out string) (*ExportPlan, error) {
	source, err := physicalDB(source)
	if err != nil {
		return nil, err
	}
	out, err = filepath.Abs(out)
	if err != nil || out == "" {
		return nil, fmt.Errorf("transfer: an explicit plan output is required")
	}
	s, fingerprint, cleanup, err := Inspect(ctx, source)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	lineage, err := exportLineage(filepath.Dir(out), source, repo)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.ExportTransfer(ctx, repo, lineage, selection)
	if err != nil {
		return nil, err
	}
	digest, err := transfer.Digest(snapshot)
	if err != nil {
		return nil, err
	}
	archive := out + ".snapshot.wiparchive"
	if err := transfer.Write(archive, snapshot); err != nil {
		return nil, err
	}
	plan := &ExportPlan{Version: 1, SourceDB: source, Fingerprint: fingerprint, Archive: archive, Digest: digest, Snapshot: snapshot}
	if err := writeJSON(out, plan); err != nil {
		_ = os.Remove(archive)
		return nil, err
	}
	return plan, nil
}

// WriteExport validates the captured plan and creates a portable archive.
func WriteExport(planPath, out string) (*ExportPlan, error) {
	var plan ExportPlan
	if err := readJSON(planPath, &plan); err != nil {
		return nil, err
	}
	if plan.Version != 1 || plan.SourceDB == "" || plan.Archive == "" || plan.Digest == "" || plan.Snapshot == nil {
		return nil, fmt.Errorf("transfer: unsupported or incomplete export plan")
	}
	previewDigest, err := transfer.Digest(plan.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("transfer: invalid export preview: %w", err)
	}
	if previewDigest != plan.Digest {
		return nil, fmt.Errorf("transfer: export preview changed; create a fresh export plan")
	}
	fingerprint, err := Fingerprint(plan.SourceDB)
	if err != nil {
		return nil, err
	}
	if fingerprint != plan.Fingerprint {
		return nil, fmt.Errorf("transfer: source changed; create a fresh export plan")
	}
	snapshot, err := transfer.Read(plan.Archive)
	if err != nil {
		return nil, err
	}
	digest, err := transfer.Digest(snapshot)
	if err != nil {
		return nil, err
	}
	if digest != plan.Digest {
		return nil, fmt.Errorf("transfer: captured archive changed; create a fresh export plan")
	}
	if err := transfer.Write(out, snapshot); err != nil {
		return nil, err
	}
	return &plan, nil
}

// PlanImport saves a reviewable decision without opening the real target store.
func PlanImport(ctx context.Context, archive, target, repo, out string, renames map[string]string, allowMigration bool) (*ImportPlan, error) {
	archive, err := filepath.Abs(archive)
	if err != nil {
		return nil, err
	}
	target, err = physicalDB(target)
	if err != nil {
		return nil, err
	}
	snapshot, err := transfer.Read(archive)
	if err != nil {
		return nil, err
	}
	s, fingerprint, cleanup, err := Inspect(ctx, target)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	work, err := s.PlanTransfer(ctx, snapshot, repo, renames)
	if err != nil {
		return nil, err
	}
	syncState, err := syncState(ctx, s, repo)
	if err != nil {
		return nil, err
	}
	originalSchema, err := capturedSchema(ctx, s.Path())
	if err != nil {
		return nil, err
	}
	plan := &ImportPlan{
		Version: 1, Archive: archive, TargetDB: target, Fingerprint: fingerprint,
		OriginalSchema: originalSchema, TargetSchema: s.SchemaVersion(), AllowMigration: allowMigration,
		Sync: syncState, Renames: renames, Work: work,
	}
	if err := writeJSON(out, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// ApplyImport reconciles a repeat or backs up and applies the reviewed work
// under the current invocation's actor, never an actor supplied by the archive.
func ApplyImport(ctx context.Context, actor store.Actor, planPath string) (*ImportResult, error) {
	var plan ImportPlan
	if err := readJSON(planPath, &plan); err != nil {
		return nil, err
	}
	if plan.Version != 1 || plan.Work == nil || plan.TargetDB == "" || plan.Archive == "" || plan.Fingerprint == "" {
		return nil, fmt.Errorf("transfer: unsupported or incomplete import plan")
	}
	target, err := physicalDB(plan.TargetDB)
	if err != nil {
		return nil, err
	}
	if target != plan.TargetDB {
		return nil, fmt.Errorf("transfer: target path changed; create a fresh import plan")
	}
	snapshot, err := transfer.Read(plan.Archive)
	if err != nil {
		return nil, err
	}
	// Check receipts on another disposable copy first. A committed operation is
	// still a no-op after legitimate subsequent edits to the destination.
	s, fingerprint, cleanup, err := Inspect(ctx, target)
	if err != nil {
		return nil, err
	}
	current, err := s.PlanTransfer(ctx, snapshot, plan.Work.Repo, plan.Renames)
	if err != nil {
		cleanup()
		return nil, err
	}
	if current.AlreadyImported {
		receipt, err := s.ApplyTransfer(ctx, actor, snapshot, plan.Work)
		cleanup()
		if err != nil {
			return nil, err
		}
		return &ImportResult{Receipt: receipt, Notice: "Already imported; no destination changes or network action."}, nil
	}
	cleanup()
	if fingerprint != plan.Fingerprint {
		return nil, fmt.Errorf("transfer: destination changed; create a fresh import plan")
	}
	if plan.OriginalSchema != plan.TargetSchema && !plan.AllowMigration {
		return nil, fmt.Errorf("transfer: target needs schema v%d → v%d; review a new plan with --allow-migration; the target has not been opened", plan.OriginalSchema, plan.TargetSchema)
	}
	backupRoot := filepath.Join(filepath.Dir(target), "transfer-backups")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return nil, err
	}
	backup, err := os.MkdirTemp(backupRoot, "import-")
	if err != nil {
		return nil, err
	}
	// Capture creates its own destination, keeping a failed capture separate
	// from a usable backup. Its parent is retained for diagnostics.
	backup = filepath.Join(backup, "store")
	backupFingerprint, err := Capture(ctx, target, backup)
	if err != nil {
		return nil, err
	}
	if backupFingerprint != plan.Fingerprint {
		return nil, fmt.Errorf("transfer: target changed before backup; replan (backup retained at %s)", backup)
	}
	destination, err := store.Open(target)
	if err != nil {
		return nil, fmt.Errorf("transfer: target open/migration failed; backup retained at %s: %w", backup, err)
	}
	defer func() { _ = destination.Close() }()
	receipt, err := destination.ApplyTransfer(ctx, actor, snapshot, plan.Work)
	if err != nil {
		return nil, fmt.Errorf("transfer: import failed; backup retained at %s (schema migration may have committed separately): %w", backup, err)
	}
	return &ImportResult{Receipt: receipt, Backup: backup, Notice: "Local import committed; no network action. Supported work follows the project's existing pairing on ordinary sync."}, nil
}

func syncState(ctx context.Context, s *store.Store, repo string) (SyncState, error) {
	bindings, err := s.ExchangeBindings(ctx)
	if err != nil {
		return SyncState{}, err
	}
	state := SyncState{State: "unpaired", Notice: "Unpaired: imported work stays local until pairing."}
	for _, b := range bindings {
		if b.LocalRepo != repo {
			continue
		}
		state.Bindings = append(state.Bindings, b)
		state.State = "paused"
		state.Notice = "Paused: import does not resume sync. Supported work is eligible after ordinary explicit resume."
		if b.Active {
			state.State = "paired"
			state.Notice = "Paired: supported imported work is eligible for the next ordinary sync to the retained destination; import itself performs no network action."
		}
	}
	return state, nil
}

func exportLineage(dir, source, repo string) (string, error) {
	key := sha256.Sum256([]byte(source + "\x00" + repo))
	path := filepath.Join(dir, ".wip-export-lineage-"+hex.EncodeToString(key[:8]))
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return "", fmt.Errorf("transfer: invalid exporter lineage file %s", path)
		}
		if _, err := hex.DecodeString(string(data)); err != nil {
			return "", fmt.Errorf("transfer: invalid exporter lineage: %w", err)
		}
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	lineage := hex.EncodeToString(token[:])
	if err := writePrivate(path, []byte(lineage)); err != nil {
		return "", err
	}
	return lineage, nil
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return transfer.DecodeJSON(data, into)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

func writePrivate(path string, data []byte) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := out.Write(data)
	syncErr := out.Sync()
	closeErr := out.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	return nil
}
