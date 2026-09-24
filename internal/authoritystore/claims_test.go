package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The protocol-1 controls are deliberately built independently of the M1
// operation catalogue. In particular, the asserted hash includes its domain
// separator and the exact canonical bytes supplied to the store.
func claimTestID(n int) string { return fmt.Sprintf("%026d", n) }

type claimTestFixture struct {
	s                       *Store
	root                    string
	peer                    tls.ConnectionState
	key                     ed25519.PrivateKey
	now                     time.Time
	matter, worktree, clone string
}

func newClaimTestFixture(t *testing.T) *claimTestFixture {
	t.Helper()
	s, root, peer, key, now := commandFixture(t)
	f := &claimTestFixture{
		s: s, root: root, peer: peer, key: key, now: now,
		matter: claimTestID(20), worktree: claimTestID(21), clone: claimTestID(22),
	}
	c := matterCommand(claimTestID(10), 1, "alpha")
	status, err := s.SubmitCommand(context.Background(), c, hashCommand(t, c), peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("create Matter submission: %+v %v", status, err)
	}
	if _, err = s.CompleteCommand(context.Background(), status.Owner, success(f.matter, "alpha"), f.matter, claimTestID(100), now, signWith(key)); err != nil {
		t.Fatalf("create Matter completion: %v", err)
	}
	return f
}

func (f *claimTestFixture) command(t *testing.T, id int, sequence uint64, name string, claim any, input any, acting ...string) ([]byte, string) {
	t.Helper()
	environment := envA
	if len(acting) != 0 {
		environment = acting[0]
	}
	var clone, worktree any
	if name != "claim.stand-down" {
		clone, worktree = f.clone, f.worktree
	}
	raw := encodeTest(t, map[string]any{
		"schema": "wipd.command/1", "command_id": claimTestID(id),
		"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": environment, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": claimTestID(id),
		"operation": map[string]any{"name": name, "version": uint64(1)},
		"context":   map[string]any{"repo_id": repoA, "clone_id": clone, "worktree_id": worktree},
		"claim":     claim, "input": input, "blobs": []any{},
	})
	return raw, digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
}

func (f *claimTestFixture) anchor(t *testing.T) PrefixAnchor {
	t.Helper()
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	a, err := currentAnchor(context.Background(), tx, domainA)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *claimTestFixture) acquire(t *testing.T, id int, sequence uint64, installed PrefixAnchor, allocation AcquireAllocation) ([]byte, string, CommandStatus, ClaimGrant) {
	t.Helper()
	raw, hash := f.command(t, id, sequence, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(40 + id),
	})
	status, err := f.s.SubmitClaimAcquire(context.Background(), raw, hash, installed, f.peer, f.now)
	if err != nil || !status.Pending || status.Owner == nil {
		t.Fatalf("acquire submission: %+v %v", status, err)
	}
	completed, grant, err := f.s.CompleteClaimAcquire(context.Background(), status.Owner, allocation, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("acquire completion: %v", err)
	}
	return raw, hash, completed, grant
}

func claimTestAllocation(id int, anchor PrefixAnchor, events ...int) AcquireAllocation {
	a := AcquireAllocation{
		ClaimID: claimTestID(30 + id), BatchID: claimTestID(50 + id), GrantID: claimTestID(60 + id),
		SnapshotID: claimTestID(70 + id), JournalID: claimTestID(80 + id), Installed: anchor,
	}
	for _, event := range events {
		a.EventIDs = append(a.EventIDs, claimTestID(event))
	}
	return a
}

func restoreClaimTrigger(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	for _, object := range step5Schema {
		if object.name == name {
			if _, err := db.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("missing schema trigger %s", name)
}

func claimTestReceipt(t *testing.T, status CommandStatus, code string, output map[string]any, ids ...int) {
	t.Helper()
	r, err := readReceipt(status.Receipt)
	if err != nil || r.Result.Code != code || len(status.SignedReceipt) == 0 {
		t.Fatalf("terminal receipt: %+v %v", r, err)
	}
	if output == nil {
		if r.Range != nil || len(r.Result.Output) != 0 {
			t.Fatalf("no-effect receipt had effects: %+v", r)
		}
	} else {
		if !bytes.Equal(r.Result.Output, encodeTest(t, output)) {
			t.Fatalf("output mismatch: %x, want %x", r.Result.Output, encodeTest(t, output))
		}
		if r.Range == nil || r.Range.First != claimTestID(ids[0]) || r.Range.Last != claimTestID(ids[len(ids)-1]) || r.Range.Count != uint64(len(ids)) {
			t.Fatalf("event range: %+v", r.Range)
		}
	}
}

func claimTestEvent(t *testing.T, f *claimTestFixture, position int, id int, command int, hash, kind, subject string, sequence uint64, payload map[string]any, acting ...string) {
	t.Helper()
	environment := envA
	if len(acting) != 0 {
		environment = acting[0]
	}
	var raw []byte
	if err := f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND position=?`, domainA, position).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want := encodeTest(t, map[string]any{
		"schema": "wipd.event/1", "event_id": claimTestID(id), "domain_id": domainA,
		"command_id": claimTestID(command), "request_hash": hash,
		"environment": map[string]any{"id": environment, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "occurred_at": f.now.Format(time.RFC3339Nano),
		"kind": kind, "subject_id": subject, "repo_id": repoA, "payload": payload,
	})
	if !bytes.Equal(raw, want) {
		t.Fatalf("event position %d %s: got %x want %x", position, kind, raw, want)
	}
}

func TestClaimAcquireGrantReopenAndContention(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	a := claimTestAllocation(1, installed, 101, 102, 103)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	forged := installed
	forged.Digest = digestBytes([]byte("wrong installed prefix"))
	if _, err := f.s.SubmitClaimAcquire(ctx, raw, hash, forged, f.peer, f.now); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("bad prefix before submission: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad prefix submitted: %v", err)
	}
	_, _, status, grant := f.acquire(t, 11, 2, installed, a)
	output := map[string]any{"claim": map[string]any{"id": a.ClaimID, "epoch": uint64(1)}, "matter_id": f.matter, "batch_id": a.BatchID, "dispatch_id": claimTestID(51)}
	claimTestReceipt(t, status, "result.succeeded", output, 101, 102, 103)
	claimTestEvent(t, f, 2, 101, 11, hash, "batch.anonymous-created", a.BatchID, 2, map[string]any{"batch_id": a.BatchID, "matter_id": f.matter})
	claimTestEvent(t, f, 3, 102, 11, hash, "claim.acquired", a.ClaimID, 2, map[string]any{"claim_id": a.ClaimID, "claim_epoch": uint64(1), "matter_id": f.matter, "batch_id": a.BatchID, "dispatch_id": claimTestID(51), "owner_environment_id": envA, "worktree_id": f.worktree})
	claimTestEvent(t, f, 4, 103, 11, hash, "dispatch.opened", claimTestID(51), 2, map[string]any{"dispatch_id": claimTestID(51), "matter_id": f.matter, "batch_id": a.BatchID, "claim_id": a.ClaimID, "worktree_id": f.worktree})
	queried, product, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(queried.Receipt, status.Receipt) || product.ID != grant.ID || !bytes.Equal(product.Wrapper, grant.Wrapper) || !bytes.Equal(product.Delta, grant.Delta) || !bytes.Equal(product.Manifest, grant.Manifest) || !bytes.Equal(product.End, grant.End) {
		t.Fatalf("grant query: %v, %+v", err, product)
	}
	// The query returns retained wire products, rather than repinning a new snapshot.
	if replay, e := f.s.SubmitClaimAcquire(ctx, raw, hash, emptyAnchor(), f.peer, f.now); e != nil || !bytes.Equal(replay.Receipt, status.Receipt) || replay.Owner != nil {
		t.Fatalf("replay rechecked prefix or reexecuted: %+v %v", replay, e)
	}
	badRaw, badHash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(99)})
	if _, e := f.s.SubmitClaimAcquire(ctx, badRaw, badHash, f.anchor(t), f.peer, f.now); !errors.Is(e, ErrConflict) {
		t.Fatalf("different-hash retry: %v", e)
	}
	contended := claimTestAllocation(2, f.anchor(t), 104, 105)
	_, _, refused, noGrant := f.acquire(t, 12, 3, contended.Installed, contended)
	claimTestReceipt(t, refused, "result.refused", nil)
	if len(noGrant.Wrapper) != 0 {
		t.Fatal("contention manufactured grant")
	}
	var events, claims, batches int
	_ = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&events)
	_ = f.s.db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=?`, domainA).Scan(&claims)
	_ = f.s.db.QueryRow(`SELECT count(*) FROM anonymous_batches WHERE domain_id=?`, domainA).Scan(&batches)
	if events != 4 || claims != 1 || batches != 1 {
		t.Fatalf("contention mutated state: events=%d claims=%d batches=%d", events, claims, batches)
	}
	if err := f.s.CollectExpired(ctx, grant.Snapshot.ExpiresAt); err != nil {
		t.Fatalf("collect expired temporary snapshot: %v", err)
	}
	var snapshots int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM snapshots WHERE snapshot_id=?`, a.SnapshotID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("temporary snapshot not collected: %d %v", snapshots, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	_, reopened, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, grant.Snapshot.ExpiresAt.Add(time.Second))
	if err != nil || reopened.ID != grant.ID || !bytes.Equal(reopened.Wrapper, grant.Wrapper) || !bytes.Equal(reopened.Start, grant.Start) || !bytes.Equal(reopened.Delta, grant.Delta) {
		t.Fatalf("grant lost on reopen/expiry: %v %+v", err, reopened)
	}
}

func TestClaimReleaseEmptySealedJournalReopenAndEpochFence(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	if err := f.s.SealClaimJournal(ctx, domainA, a.JournalID); err != nil {
		t.Fatalf("seal B0: %v", err)
	}
	root := sha256.Sum256([]byte("wipd/journal-barrier/v1\x00"))
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": a.JournalID,
		"claim": map[string]any{"id": a.ClaimID, "epoch": uint64(1)}, "entry_count": uint64(0), "last_position": uint64(0),
		"terminal_receipt_count": uint64(0), "entries_digest": digestRawBytes(root[:]), "sealed": true,
		"unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	ref := map[string]any{"id": a.ClaimID, "epoch": uint64(1)}
	wrong := map[string]any{}
	for k, v := range barrier {
		wrong[k] = v
	}
	wrong["entries_digest"] = digestBytes([]byte("not B0"))
	badRaw, badHash := f.command(t, 12, 3, "claim.release", ref, map[string]any{"barrier": wrong})
	bad, err := f.s.SubmitClaimLifecycle(ctx, badRaw, badHash, f.peer, f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := f.s.CompleteClaimLifecycle(ctx, bad.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	goodRaw, goodHash := f.command(t, 13, 4, "claim.release", ref, map[string]any{"barrier": barrier})
	good, err := f.s.SubmitClaimLifecycle(ctx, goodRaw, goodHash, f.peer, f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	released, err := f.s.CompleteClaimLifecycle(ctx, good.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{"claim_id": a.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51), "barrier_digest": digestRawBytes(root[:])}
	claimTestReceipt(t, released, "result.succeeded", out, 104, 105)
	claimTestEvent(t, f, 5, 104, 13, goodHash, "dispatch.closed", claimTestID(51), 4, map[string]any{"dispatch_id": claimTestID(51), "claim_id": a.ClaimID, "claim_epoch": uint64(1)})
	claimTestEvent(t, f, 6, 105, 13, goodHash, "claim.released", a.ClaimID, 4, out)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.SubmitClaimLifecycle(ctx, goodRaw, goodHash, f.peer, f.now, nil)
	if err != nil || !bytes.Equal(replayed.Receipt, released.Receipt) || replayed.Owner != nil {
		t.Fatalf("release replay after reopen: %+v %v", replayed, err)
	}
	oldRaw, oldHash := f.command(t, 14, 5, "claim.release", ref, map[string]any{"barrier": barrier})
	if _, err := f.s.SubmitClaimLifecycle(ctx, oldRaw, oldHash, f.peer, f.now, nil); !errors.Is(err, ErrFenced) {
		t.Fatalf("closed claim release crossed submission: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(14), oldHash, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed claim release has receipt or submission: %v", err)
	}
	if _, err = f.s.QueryCommand(ctx, domainA, claimTestID(12), badHash, 7, f.peer, envA, f.now); err != nil {
		t.Fatalf("mismatched barrier receipt lost: %v", err)
	}
	// A new claim epoch reuses the existing anonymous Batch, so only two
	// acquisition events are permitted and the old claim cannot become active.
	next := claimTestAllocation(15, f.anchor(t), 106, 107)
	next.BatchID = "" // must not be consulted on reuse
	_, nextHash, acquired, _ := f.acquire(t, 15, 5, next.Installed, next)
	claimTestReceipt(t, acquired, "result.succeeded", map[string]any{
		"claim":     map[string]any{"id": next.ClaimID, "epoch": uint64(2)},
		"matter_id": f.matter, "batch_id": a.BatchID, "dispatch_id": claimTestID(55),
	}, 106, 107)
	claimTestEvent(t, f, 7, 106, 15, nextHash, "claim.acquired", next.ClaimID, 5, map[string]any{
		"claim_id": next.ClaimID, "claim_epoch": uint64(2), "matter_id": f.matter,
		"batch_id": a.BatchID, "dispatch_id": claimTestID(55), "owner_environment_id": envA,
		"worktree_id": f.worktree,
	})
}

func TestClaimAcquireSignerFailureRollsBackEveryEffect(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(51),
	})
	pending, err := f.s.SubmitClaimAcquire(ctx, raw, hash, a.Installed, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit: %+v %v", pending, err)
	}
	_, _, err = f.s.CompleteClaimAcquire(ctx, pending.Owner, a, f.now, func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("signer unavailable")
	})
	if err == nil {
		t.Fatal("accepted unsigned acquisition")
	}
	queried, product, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
	if err != nil || !queried.Pending || product.ID != "" {
		t.Fatalf("failed signature changed pending grant: %+v %+v %v", queried, product, err)
	}
	var claims, batches, events int
	for _, item := range []struct {
		table string
		dest  *int
	}{{"claims", &claims}, {"anonymous_batches", &batches}, {"authority_events", &events}} {
		if err := f.s.db.QueryRow("SELECT count(*) FROM "+item.table+" WHERE domain_id=?", domainA).Scan(item.dest); err != nil {
			t.Fatal(err)
		}
	}
	if claims != 0 || batches != 0 || events != 1 {
		t.Fatalf("failed signer left claim effects: claims=%d batches=%d events=%d", claims, batches, events)
	}
	completed, _, err := f.s.CompleteClaimAcquire(ctx, pending.Owner, a, f.now, signWith(f.key))
	if err != nil || completed.Pending {
		t.Fatalf("retry terminal after rollback: %+v %v", completed, err)
	}
}

func TestClaimNoEffectProblemNamespaceBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		code, invalid, valid string
	}{
		{"result.rejected", "refusal.claim-contended", "validation.matter-not-found"},
		{"result.refused", "validation.matter-not-found", "refusal.claim-contended"},
		{"result.failed", "refusal.claim-contended", "internal.unavailable"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newClaimTestFixture(t)
			ctx := context.Background()
			raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
				"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
			})
			pending, err := f.s.SubmitClaimAcquire(ctx, raw, hash, f.anchor(t), f.peer, f.now)
			if err != nil || pending.Owner == nil {
				t.Fatalf("submit: %+v %v", pending, err)
			}
			if _, err := f.s.CompleteClaimNoEffect(ctx, pending.Owner, tc.code, tc.invalid, f.now, signWith(f.key)); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("invalid namespace persisted: %v", err)
			}
			status, err := f.s.QueryCommand(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
			if err != nil || !status.Pending || len(status.Receipt) != 0 {
				t.Fatalf("invalid namespace terminated submission: %+v %v", status, err)
			}
			var events, claims int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err := f.s.db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=?`, domainA).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if events != 1 || claims != 0 {
				t.Fatalf("invalid namespace mutated state: events=%d claims=%d", events, claims)
			}
			completed, err := f.s.CompleteClaimNoEffect(ctx, pending.Owner, tc.code, tc.valid, f.now, signWith(f.key))
			if err != nil {
				t.Fatal(err)
			}
			claimTestReceipt(t, completed, tc.code, nil)
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatalf("valid namespace did not reopen: %v", err)
			}
		})
	}
}

func TestClaimAcquireRecoveryRetainsSubmittedAnchor(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	if status, err := f.s.SubmitClaimAcquire(ctx, raw, hash, installed, f.peer, f.now); err != nil || status.Owner == nil {
		t.Fatalf("submission: %+v %v", status, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := f.s.RecoverClaimAcquire(ctx, raw, hash)
	if err != nil {
		t.Fatal(err)
	}
	wrong := claimTestAllocation(1, emptyAnchor(), 101, 102, 103)
	if _, _, err = f.s.CompleteClaimAcquire(ctx, owner, wrong, f.now, signWith(f.key)); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("recovery accepted substituted anchor: %v", err)
	}
	var count int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM claims`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial claim after refused anchor: %d %v", count, err)
	}
	good := claimTestAllocation(1, installed, 101, 102, 103)
	if _, _, err = f.s.CompleteClaimAcquire(ctx, owner, good, f.now, signWith(f.key)); err != nil {
		t.Fatalf("exact retained anchor: %v", err)
	}
	if _, err = f.s.db.Exec(`DROP TRIGGER claim_acquire_intents_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec(`UPDATE claim_acquire_intents SET start_digest=? WHERE domain_id=? AND command_id=?`, emptyAnchor().Digest, domainA, claimTestID(11)); err != nil {
		t.Fatal(err)
	}
	restoreClaimTrigger(t, f.s.db, "claim_acquire_intents_immutable")
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("tampered acquire evidence reopened: %v", err)
	}
}

func TestClaimAcquireConcurrentTerminalAndGrantAreSingleProduct(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	pending, err := f.s.SubmitClaimAcquire(ctx, raw, hash, a.Installed, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit: %+v %v", pending, err)
	}
	type outcome struct {
		status CommandStatus
		grant  ClaimGrant
		err    error
	}
	var wg sync.WaitGroup
	results := make(chan outcome, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, grant, err := f.s.CompleteClaimAcquire(ctx, pending.Owner, a, f.now, signWith(f.key))
			results <- outcome{status, grant, err}
		}()
	}
	wg.Wait()
	close(results)
	var winner outcome
	succeeded, fenced := 0, 0
	for got := range results {
		switch {
		case got.err == nil:
			succeeded++
			winner = got
		case errors.Is(got.err, ErrNotOwner):
			fenced++
		default:
			t.Fatalf("unexpected competing terminal: %v", got.err)
		}
	}
	if succeeded != 1 || fenced != 1 {
		t.Fatalf("competing completions: success=%d fenced=%d", succeeded, fenced)
	}
	status, grant, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(status.Receipt, winner.status.Receipt) || !bytes.Equal(grant.Wrapper, winner.grant.Wrapper) {
		t.Fatalf("race retained different product: %v", err)
	}
}

func TestClaimJournalPendingRepairProofAndGeneration(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	ref := map[string]any{"id": a.ClaimID, "epoch": uint64(1)}
	journalRaw, journalHash := f.command(t, 12, 3, "cursor.move", ref, map[string]any{"target_id": nil})
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, journalHash); !errors.Is(err, ErrPending) {
		t.Fatalf("out of order position: %v", err)
	}
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 1, journalRaw, journalHash); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, journalHash); !errors.Is(err, ErrPending) {
		t.Fatalf("pending head admitted next: %v", err)
	}
	if err := f.s.SealClaimJournal(ctx, domainA, a.JournalID); !errors.Is(err, ErrPending) {
		t.Fatalf("sealed pending journal: %v", err)
	}
	input := func(proof any) map[string]any {
		return map[string]any{
			"journal_id": a.JournalID, "head_position": uint64(1),
			"head_command_id": claimTestID(12), "head_request_hash": journalHash,
			"action": map[string]any{"kind": "abandon", "proof": proof},
		}
	}
	wrongProof := map[string]any{"terminal_receipt": nil, "not_submitted_proof": "same-epoch-receipt-not-found"}
	staleRef := map[string]any{"id": a.ClaimID, "epoch": uint64(2)}
	staleRaw, staleHash := f.command(t, 14, 3, "claim.journal-repair", staleRef, input(wrongProof))
	if _, err := f.s.SubmitClaimLifecycle(ctx, staleRaw, staleHash, f.peer, f.now, nil); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong-epoch repair crossed submission: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(14), staleHash, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-epoch repair has receipt or submission: %v", err)
	}
	badRaw, badHash := f.command(t, 13, 3, "claim.journal-repair", ref, input(wrongProof))
	pending, err := f.s.SubmitClaimLifecycle(ctx, badRaw, badHash, f.peer, f.now, nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("repair submission: %+v %v", pending, err)
	}
	newJournal := claimTestID(90)
	completed, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, newJournal, []string{claimTestID(104)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("not-found proof repair: %v", err)
	}
	out := map[string]any{"claim_id": a.ClaimID, "archived_journal_id": a.JournalID, "new_journal_id": newJournal, "action": "abandon"}
	claimTestReceipt(t, completed, "result.succeeded", out, 104)
	claimTestEvent(t, f, 5, 104, 13, badHash, "claim.journal-repaired", a.ClaimID, 3, out)
	var prior, next string
	var generation uint64
	if err = f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, a.JournalID).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT state,generation FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, newJournal).Scan(&next, &generation); err != nil {
		t.Fatal(err)
	}
	if prior != "quarantined" || next != "open" || generation != 2 {
		t.Fatalf("repair generation: old=%s new=%s generation=%d", prior, next, generation)
	}
	if err = f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, journalHash); !errors.Is(err, ErrFenced) {
		t.Fatalf("archived journal accepts writes: %v", err)
	}
}

func TestClaimRepairCannotTreatSubmittedPendingHeadAsNotFound(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	ref := map[string]any{"id": a.ClaimID, "epoch": uint64(1)}
	journalRaw, journalHash := f.command(t, 12, 3, "cursor.move", ref, map[string]any{"target_id": nil})
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 1, journalRaw, journalHash); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, digestBytes([]byte("wrong journal bytes"))); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("journal hash not verified: %v", err)
	}
	if err := f.s.AcknowledgeClaimJournalEntry(ctx, domainA, a.JournalID, 1, nil, f.anchor(t)); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("missing terminal receipt acknowledged: %v", err)
	}
	// Model an admitted but still pending Step 6 return. A query for a
	// terminal receipt is not-found, but that absence is not a no-submission
	// proof and must not permit repair to abandon the head.
	if _, err := f.s.db.Exec(`INSERT INTO submissions(domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state) VALUES(?,?,?,?,?,?,?,?,?,'submitted')`, domainA, claimTestID(12), journalHash, journalRaw, 7, envA, 3, "cursor.move", 1); err != nil {
		t.Fatal(err)
	}
	raw, hash := f.command(t, 13, 3, "claim.journal-repair", ref, map[string]any{
		"journal_id": a.JournalID, "head_position": uint64(1), "head_command_id": claimTestID(12),
		"head_request_hash": journalHash, "action": map[string]any{
			"kind": "abandon", "proof": map[string]any{"terminal_receipt": nil, "not_submitted_proof": "same-epoch-receipt-not-found"},
		},
	})
	// The pending return owns sequence 3. The repair must not bypass that
	// head even though it supplies a "receipt not found" assertion.
	if _, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, f.peer, f.now, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("pending return did not fence repair submission: %v", err)
	}
	queried, err := f.s.QueryCommand(ctx, domainA, claimTestID(12), journalHash, 7, f.peer, envA, f.now)
	if err != nil || !queried.Pending {
		t.Fatalf("pending return lost: %+v %v", queried, err)
	}
	var state string
	if err := f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, a.JournalID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("pending return journal quarantined: %s", state)
	}
}

func TestClaimStandDownCrossEnvironmentSignedScopeAndLoss(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	d, owner := identity(domainA, 7)
	ca := key("step4-ca")
	caDER := caFixture(t, ca, f.now)
	leafKey := key("claim-second-environment")
	leaf := leafFixture(t, leafKey, ca, caDER, domainA, envB, d.OwnerKeyID, 7, f.now, 102)
	grant := grantFixture(t, owner, d, "environment-enroll", claimTestID(95), envB, leafKey, 2)
	if _, err := f.s.IssueEnvironmentCertificate(ctx, domainA, envB, grant, csrFixture(t, leafKey, "Environment B"), [][]byte{leaf, caDER}, f.now); err != nil {
		t.Fatalf("enroll second Environment: %v", err)
	}
	peerB := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
	target := map[string]any{"claim_id": a.ClaimID, "claim_epoch": uint64(1), "owner_environment_id": envA}
	reason := "Owner accepts loss of unreturned work"
	authorize := func(id int, hash string, nonce byte, scopeClaim string) []byte {
		subject := encodeTest(t, map[string]any{
			"schema": "wipd.claim-stand-down-subject/1", "command_id": claimTestID(id), "request_hash": hash,
			"claim_id": scopeClaim, "claim_epoch": uint64(1), "owner_environment_id": envA,
			"acting_environment_id": envB, "reason_digest": digestBytes([]byte(reason)),
		})
		return signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", domainA, d.OwnerKeyID, 7, map[string]any{
			"schema": "wipd.owner-attestation/1", "action": "claim-stand-down", "domain_id": domainA,
			"current_epoch": uint64(7), "next_epoch": nil, "subject_schema": "wipd.claim-stand-down-subject/1",
			"subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16),
			"issued_at":  f.now.Add(-time.Minute).Format(time.RFC3339Nano),
			"expires_at": f.now.Add(time.Minute).Format(time.RFC3339Nano), "loss_accepted": true,
		})
	}
	input := func(loss bool) map[string]any {
		return map[string]any{"target": target, "reason": reason, "acknowledge_unreturned_work_loss": loss}
	}
	raw, hash := f.command(t, 12, 1, "claim.stand-down", nil, input(false), envB)
	proof := authorize(12, hash, 3, a.ClaimID)
	if _, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, peerB, f.now, authorize(12, hash, 4, claimTestID(99))); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("wrong owner scope admitted: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(12), hash, 7, peerB, envB, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-scope attestation submitted: %v", err)
	}
	pending, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, peerB, f.now, proof)
	if err != nil || pending.Owner == nil {
		t.Fatalf("stand-down submission: %+v %v", pending, err)
	}
	refused, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	goodRaw, goodHash := f.command(t, 13, 2, "claim.stand-down", nil, input(true), envB)
	goodProof := authorize(13, goodHash, 5, a.ClaimID)
	good, err := f.s.SubmitClaimLifecycle(ctx, goodRaw, goodHash, peerB, f.now, goodProof)
	if err != nil || good.Owner == nil {
		t.Fatalf("authorized stand-down: %+v %v", good, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("pending stand-down reopen: %v", err)
	}
	if _, err = f.s.RecoverClaimLifecycle(ctx, goodRaw, goodHash, []byte("different proof")); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("recovery accepted substituted owner proof: %v", err)
	}
	recovered, err := f.s.RecoverClaimLifecycle(ctx, goodRaw, goodHash)
	if err != nil {
		t.Fatalf("admitted owner proof expired after submission: %v", err)
	}
	closed, err := f.s.CompleteClaimLifecycle(ctx, recovered, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	reasonDigest := digestBytes([]byte(reason))
	claimTestReceipt(t, closed, "result.succeeded", map[string]any{
		"claim_id": a.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51), "reason_digest": reasonDigest,
	}, 104, 105)
	claimTestEvent(t, f, 5, 104, 13, goodHash, "dispatch.closed", claimTestID(51), 2,
		map[string]any{"dispatch_id": claimTestID(51), "claim_id": a.ClaimID, "claim_epoch": uint64(1)}, envB)
	claimTestEvent(t, f, 6, 105, 13, goodHash, "claim.stood-down", a.ClaimID, 2,
		map[string]any{
			"claim_id": a.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51),
			"owner_environment_id": envA, "acting_environment_id": envB, "reason_digest": reasonDigest,
			"loss_accepted": true,
		}, envB)
	if err = f.s.AuthorizeMigration(ctx, domainA, migrationAuthorizationFixture(t, owner, d, f.now, 5), f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("Step 7 reused consumed Step 5 owner nonce: %v", err)
	}
	if _, err = f.s.db.Exec(`DROP TRIGGER claim_stand_down_proofs_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec(`UPDATE claim_stand_down_proofs SET authorization=? WHERE domain_id=? AND command_id=?`, []byte("forged"), domainA, claimTestID(13)); err != nil {
		t.Fatal(err)
	}
	restoreClaimTrigger(t, f.s.db, "claim_stand_down_proofs_immutable")
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("tampered owner proof reopened: %v", err)
	}
}
