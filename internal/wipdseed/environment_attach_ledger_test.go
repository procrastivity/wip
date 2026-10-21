package wipdseed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func attachLedgerFixture(t *testing.T) (string, *EnvironmentAttachLedger, EnvironmentAttachAttempt) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := OpenEnvironmentAttachLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	grant, receipt := []byte("grant-artifact"), []byte("exact-d115-receipt")
	g, r := sha256.Sum256(grant), sha256.Sum256(receipt)
	a := EnvironmentAttachAttempt{EnvironmentID: "environment-1", GrantBytes: grant, GrantDigest: "sha256:" + hex.EncodeToString(g[:]), D115ReceiptBytes: receipt, D115ReceiptDigest: "sha256:" + hex.EncodeToString(r[:]), D115AttemptID: "d115-attempt-1", CommandID: "command-1", RequestBytes: []byte("request"), RequestHash: "sha256:request", AuthorityDomainID: "domain-1", AuthorityEpoch: 4, EnvironmentSPKI: "sha256:spki", Route: "ssh://example/repo", CheckoutProof: []byte("proof")}
	return root, l, a
}

func TestEnvironmentAttachLedgerReuseConflictAndReopen(t *testing.T) {
	root, l, a := attachLedgerFixture(t)
	created, err := l.GetOrCreate(a)
	if err != nil {
		t.Fatal(err)
	}
	again, err := l.GetOrCreate(a)
	if err != nil || again.CommandID != created.CommandID {
		t.Fatalf("reuse=%+v err=%v", again, err)
	}
	conflict := a
	conflict.Route = "ssh://other/repo"
	if _, err = l.GetOrCreate(conflict); !errors.Is(err, ErrStateExists) {
		t.Fatalf("conflict err=%v", err)
	}
	if _, err = l.AppendMarker(a.EnvironmentID, a.GrantDigest, EnvironmentAttachMarker{Sequence: 1, Kind: "terminal-received"}, EnvironmentAttachTerminal); err != nil {
		t.Fatal(err)
	}
	if _, err = l.PersistResult(a.EnvironmentID, a.GrantDigest, []byte("terminal"), EnvironmentAttachTerminal); err != nil {
		t.Fatal(err)
	}
	l, err = OpenEnvironmentAttachLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.Load(a.EnvironmentID, a.GrantDigest)
	if err != nil || string(got.ResultBytes) != "terminal" || got.Disposition != EnvironmentAttachTerminal {
		t.Fatalf("reopen=%+v err=%v", got, err)
	}
}

func TestEnvironmentAttachLedgerUnknownIsCumulativeAndTerminalIsImmutable(t *testing.T) {
	root, ledger, attempt := attachLedgerFixture(t)
	if _, err := ledger.GetOrCreate(attempt); err != nil {
		t.Fatal(err)
	}
	unknown, err := ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		EnvironmentAttachMarker{Sequence: 1, Kind: "send-started"}, EnvironmentAttachOutcomeUnknown)
	if err != nil || unknown.Disposition != EnvironmentAttachOutcomeUnknown {
		t.Fatalf("record unknown: %+v, %v", unknown, err)
	}
	if _, err = ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		EnvironmentAttachMarker{Sequence: 2, Kind: "connect-failed"}, EnvironmentAttachUnavailable); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("unknown disposition was downgraded: %v", err)
	}
	if _, err = ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		EnvironmentAttachMarker{Sequence: 2, Kind: "submission-accepted"}, EnvironmentAttachSubmitted); err != nil {
		t.Fatalf("authenticated M2 acceptance did not advance unknown: %v", err)
	}
	ledger, err = OpenEnvironmentAttachLedger(root)
	if err != nil {
		t.Fatalf("reopen outcome-unknown attempt: %v", err)
	}
	if _, err = ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		EnvironmentAttachMarker{Sequence: 3, Kind: "terminal-received"}, EnvironmentAttachTerminal); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.PersistResult(attempt.EnvironmentID, attempt.GrantDigest, []byte("receipt-1"), EnvironmentAttachTerminal); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.PersistResult(attempt.EnvironmentID, attempt.GrantDigest, []byte("receipt-2"), EnvironmentAttachTerminal); !errors.Is(err, ErrStateExists) {
		t.Fatalf("terminal receipt was replaced: %v", err)
	}
	if _, err = ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		EnvironmentAttachMarker{Sequence: 4, Kind: "local-install-pending"}, EnvironmentAttachLocalInstallPending); err != nil {
		t.Fatalf("terminal recovery did not advance to local-install-pending: %v", err)
	}
	if _, err = ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		EnvironmentAttachMarker{Sequence: 5, Kind: "local-installed"}, EnvironmentAttachInstalled); err != nil {
		t.Fatalf("recovered local install did not advance to installed: %v", err)
	}
}

func TestEnvironmentAttachLedgerConcurrentCreate(t *testing.T) {
	_, l, a := attachLedgerFixture(t)
	const n = 12
	ids := make(chan string, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); got, err := l.GetOrCreate(a); ids <- got.CommandID; errs <- err }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range ids {
		if id != a.CommandID {
			t.Fatalf("command id %q", id)
		}
	}
}

func TestRememberedRouteBindingDoesNotRewriteAttachLedger(t *testing.T) {
	root, ledger, attempt := attachLedgerFixture(t)
	if _, err := ledger.GetOrCreate(attempt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "environment-attach-attempts", environmentAttachLedgerFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.RememberedRouteBinding(attempt.Route); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("read-only remembered-route lookup rewrote the attach ledger")
	}
}

func TestEnvironmentAttachLedgerRecoversStaleTemp(t *testing.T) {
	root, l, a := attachLedgerFixture(t)
	path := filepath.Join(l.dir, ".environment-attach.tmp")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.GetOrCreate(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp remains: %v", err)
	}
	l, err := OpenEnvironmentAttachLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Load(a.EnvironmentID, a.GrantDigest); err != nil {
		t.Fatal(err)
	}
}
