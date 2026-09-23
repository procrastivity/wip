package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func commandFixture(t *testing.T) (*Store, string, tls.ConnectionState, ed25519.PrivateKey, time.Time) {
	t.Helper()
	s, root := fresh(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d, owner := identity(domainA, 7)
	ctx := context.Background()
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	artifactKey := key("step4-artifact")
	id, _ := spkiID(artifactKey.Public())
	cert := signedTest(t, owner, "authority-artifact-key", "wipd.authority-artifact-key/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": domainA, "authority_epoch": uint64(7), "key_generation": uint64(1), "key_id": id, "ed25519_public_key": []byte(artifactKey.Public().(ed25519.PublicKey)), "not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.RegisterArtifactKey(ctx, domainA, cert, now); err != nil {
		t.Fatal(err)
	}
	caKey := key("step4-ca")
	caDER := caFixture(t, caKey, now)
	caID, _ := spkiID(caKey.Public())
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID, "ca_generation": uint64(1), "ca_key_id": caID, "ca_certificate_der": caDER, "not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.InstallEnvironmentCA(ctx, domainA, delegation, now); err != nil {
		t.Fatal(err)
	}
	leafKey := key("step4-leaf")
	csr := csrFixture(t, leafKey, "Environment")
	leaf := leafFixture(t, leafKey, caKey, caDER, domainA, envA, d.OwnerKeyID, 7, now, 101)
	grant := grantFixture(t, owner, d, "environment-enroll", grantA, envA, leafKey, 1)
	if _, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, csr, [][]byte{leaf, caDER}, now); err != nil {
		t.Fatal(err)
	}
	return s, root, tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}, artifactKey, now
}

func matterCommand(id string, seq uint64, locator string) operation.Command {
	return operation.Command{ID: id, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA, EnvironmentSequence: seq, ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: id, Request: operation.Request{Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: repoA}, Input: operation.MatterCreateInput{Title: "A title", Locator: locator}}}
}

func hashCommand(t *testing.T, c operation.Command) string {
	t.Helper()
	h, e := c.RequestHash()
	if e != nil {
		t.Fatal(e)
	}
	return h
}

func signWith(k ed25519.PrivateKey) Signer {
	return func(_ context.Context, b []byte) ([]byte, error) { return ed25519.Sign(k, b), nil }
}

func success(id, locator string) operation.Result {
	return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: id, Locator: locator, Title: "A title"}}
}

func TestCommandDurableSubmissionTerminalAndReplay(t *testing.T) {
	s, root, peer, k, now := commandFixture(t)
	ctx := context.Background()
	c := matterCommand(domainB, 1, "alpha")
	h := hashCommand(t, c)
	if _, err := s.SubmitCommand(ctx, c, digest('f'), peer, now); err == nil {
		t.Fatal("accepted asserted hash")
	}
	if _, err := s.QueryCommand(ctx, domainA, c.ID, h, 7, peer, envA, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pre-submission: %v", err)
	}
	if _, err := s.SubmitCommand(ctx, c, h, tls.ConnectionState{}, now); err == nil {
		t.Fatal("accepted missing handshake")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.SubmitCommand(canceled, c, h, peer, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-submission cancellation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("before-submission reopen: %v", err)
	}
	if _, err := s.QueryCommand(ctx, domainA, c.ID, h, 7, peer, envA, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canceled command appeared: %v", err)
	}
	status, err := s.SubmitCommand(ctx, c, h, peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submission: %+v %v", status, err)
	}
	if err := s.AttachRepo(ctx, domainA, repoB); !errors.Is(err, ErrFenced) {
		t.Fatalf("membership changed after submission: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("pending reopen: %v", err)
	}
	defer func() { _ = s.Close() }()
	status, err = s.SubmitCommand(ctx, c, h, peer, now)
	if err != nil || !status.Pending || status.Owner != nil {
		t.Fatalf("coalesced retry: %+v %v", status, err)
	}
	conflict := matterCommand(domainB, 1, "beta")
	if _, err = s.SubmitCommand(ctx, conflict, hashCommand(t, conflict), tls.ConnectionState{}, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("unauthenticated command ID probe: %v", err)
	}
	if _, err = s.SubmitCommand(ctx, conflict, hashCommand(t, conflict), peer, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("different hash: %v", err)
	}
	owner, err := s.RecoverCommand(ctx, c, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecoverCommand(ctx, c, h); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("second owner: %v", err)
	}
	if _, err = s.CompleteCommand(ctx, owner, success(repoB, "alpha"), repoB, repoC, now, func(context.Context, []byte) ([]byte, error) { return nil, errors.New("sign unavailable") }); err == nil {
		t.Fatal("signing failure committed")
	}
	status, err = s.QueryCommand(ctx, domainA, c.ID, h, 7, peer, envA, now)
	if err != nil || !status.Pending {
		t.Fatalf("rolled-back pending: %+v %v", status, err)
	}
	if _, err = s.CompleteCommand(ctx, owner, success(repoB, "alpha"), repoB, repoC, now, signWith(k)); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	status, err = s.QueryCommand(ctx, domainA, c.ID, h, 7, peer, envA, now)
	if err != nil || status.Pending || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
		t.Fatalf("query: %+v %v", status, err)
	}
	var r receiptRecord
	r, err = readReceipt(status.Receipt)
	if err != nil || r.Range == nil || r.Range.First != repoC || r.Range.Last != repoC || r.Range.Count != 1 || r.Result.Code != "result.succeeded" {
		t.Fatalf("receipt: %+v %v", r, err)
	}
	if _, err = s.CompleteCommand(ctx, owner, success(repoB, "alpha"), repoB, repoC, now, signWith(k)); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("executed twice: %v", err)
	}
	before := append([]byte(nil), status.SignedReceipt...)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("terminal reopen: %v", err)
	}
	replayed, err := s.SubmitCommand(ctx, c, h, peer, now)
	if err != nil || replayed.Owner != nil || !bytes.Equal(replayed.SignedReceipt, before) || !bytes.Equal(replayed.Receipt, status.Receipt) {
		t.Fatalf("stable replay: %+v %v", replayed, err)
	}
	if _, err = s.RecoverCommand(ctx, c, h); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("terminal recovered: %v", err)
	}
}

func TestCommandConcurrentCoalescingAndNoEffect(t *testing.T) {
	s, root, peer, k, now := commandFixture(t)
	ctx := context.Background()
	c := matterCommand(domainB, 1, "alpha")
	h := hashCommand(t, c)
	var wg sync.WaitGroup
	start := make(chan struct{})
	outcomes := make(chan CommandStatus, 24)
	failures := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := s.SubmitCommand(ctx, c, h, peer, now)
			outcomes <- out
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	close(failures)
	owners := 0
	var execution *Execution
	for out := range outcomes {
		if out.Owner != nil {
			owners++
			execution = out.Owner
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if owners != 1 {
		t.Fatalf("execution owners: %d", owners)
	}
	owner, err := s.RecoverCommand(ctx, c, h)
	if !errors.Is(err, ErrNotOwner) || owner != nil {
		t.Fatalf("live owner reclaim: %v", err)
	}
	// A successful empty fold is not a declared no-op for matter.create@v1.
	if _, err = s.CompleteCommand(ctx, execution, operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{}}, "", "", now, signWith(k)); err == nil {
		t.Fatal("empty success accepted")
	}
	refused := operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemUnknownClone, Message: "not eligible"}}
	if _, err = s.CompleteCommand(ctx, execution, refused, "", "", now, signWith(k)); err != nil {
		t.Fatalf("refusal: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var eventCount, matterCount, head int
	_ = s.db.QueryRow(`SELECT count(*) FROM authority_events`).Scan(&eventCount)
	_ = s.db.QueryRow(`SELECT count(*) FROM matters`).Scan(&matterCount)
	_ = s.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&head)
	if eventCount != 0 || matterCount != 0 || head != 1 {
		t.Fatalf("no-effect state events=%d matters=%d head=%d", eventCount, matterCount, head)
	}
	status, err := s.QueryCommand(ctx, domainA, c.ID, h, 7, peer, envA, now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := readReceipt(status.Receipt)
	if err != nil || r.Range != nil || r.Result.Output != nil || r.Result.Code != "result.refused" {
		t.Fatalf("no-effect receipt: %+v %v", r, err)
	}
}

func TestCommandRollbackOnProjectionAndReopenTamper(t *testing.T) {
	s, root, peer, k, now := commandFixture(t)
	ctx := context.Background()
	c := matterCommand(domainB, 1, "alpha")
	h := hashCommand(t, c)
	out, err := s.SubmitCommand(ctx, c, h, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	// A projection constraint failure occurs after the event insert, and must
	// roll back event, prefix, receipt, artifact and head together.
	if _, err = s.db.Exec(`CREATE TEMP TRIGGER fail_projection BEFORE INSERT ON matters BEGIN SELECT RAISE(ABORT,'injected projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteCommand(ctx, out.Owner, success(repoB, "alpha"), repoB, repoC, now, signWith(k)); err == nil {
		t.Fatal("projection conflict committed")
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM authority_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("event survived rollback: %d", n)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteCommand(ctx, out.Owner, success(repoB, "alpha"), repoB, repoC, now, signWith(k)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TRIGGER matters_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE matters SET title='altered'`); err != nil {
		t.Fatal(err)
	}
	for _, o := range step4Schema {
		if o.name == "matters_no_update" {
			if _, err = db.Exec(o.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = db.Close()
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted divergent projection: %v", err)
	}
}

func TestOpenRejectsPendingBeyondNextSequence(t *testing.T) {
	s, root, peer, _, now := commandFixture(t)
	original := matterCommand(domainB, 1, "alpha")
	if _, err := s.SubmitCommand(context.Background(), original, hashCommand(t, original), peer, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	var head uint64
	if err = db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&head); err != nil || head != 0 {
		t.Fatalf("corrupt-state fixture head=%d: %v", head, err)
	}
	corrupt := matterCommand(domainB, 2, "alpha")
	encoded, err := corrupt.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hash := hashCommand(t, corrupt)
	if _, err = db.Exec(`DROP TRIGGER submissions_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE submissions SET request_hash=?,command=?,environment_sequence=? WHERE domain_id=? AND command_id=?`, hash, encoded, corrupt.EnvironmentSequence, domainA, corrupt.ID); err != nil {
		t.Fatal(err)
	}
	for _, object := range step4Schema {
		if object.name == "submissions_immutable" {
			if _, err = db.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted pending sequence 2 at head 0: %v", err)
	}
}

func TestStep4ExplicitUpgradeAndIncompleteBackup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "authority")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := connect(filepath.Join(root, "authority.db"), "rwc", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(root, "authority.db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = installBaseline(db); err != nil {
		t.Fatal(err)
	}
	if err = installStep3(db); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open migrated: %v", err)
	}
	backup := filepath.Join(root, "authority-v2.backup.db")
	if err = os.WriteFile(backup, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV2(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted incomplete backup: %v", err)
	}
	if err = os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV2(root); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV2(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("repeated migration: %v", err)
	}
	b, err := connect(backup, "rw", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaVersion(b, 2); err != nil {
		t.Fatalf("v2 backup: %v", err)
	}
	_ = b.Close()
	if err := checkV3Root(root); err != nil {
		t.Fatal(err)
	}
}

func TestStep4UpgradeRejectsUnrelatedValidBackup(t *testing.T) {
	makeV2 := func(root, domain, repo string) {
		t.Helper()
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(root, "authority.db")
		db, err := connect(file, "rwc", true)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
		if err = installBaseline(db); err != nil {
			t.Fatal(err)
		}
		if err = installStep3(db); err != nil {
			t.Fatal(err)
		}
		d, _ := identity(domain, 7)
		if _, err = db.Exec(`INSERT INTO domains(domain_id,owner_public_key,owner_key_id,initial_epoch,active_epoch) VALUES(?,?,?,?,?)`, d.ID, []byte(d.OwnerPublicKey), d.OwnerKeyID, d.ActiveEpoch, d.ActiveEpoch); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO repo_memberships(repo_id,domain_id) VALUES(?,?)`, repo, domain); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		check, err := connect(file, "rw", false)
		if err != nil {
			t.Fatal(err)
		}
		if err = checkSchemaVersion(check, 2); err != nil {
			t.Fatalf("invalid v2 fixture %s: %v", domain, err)
		}
		if err = check.Close(); err != nil {
			t.Fatal(err)
		}
	}

	base := t.TempDir()
	source := filepath.Join(base, "source")
	other := filepath.Join(base, "other")
	makeV2(source, domainA, repoA)
	makeV2(other, domainB, repoB)
	backup, err := os.ReadFile(filepath.Join(other, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "authority-v2.backup.db"), backup, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV2(source); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted unrelated valid v2 backup: %v", err)
	}
	db, err := connect(filepath.Join(source, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err = checkSchemaVersion(db, 2); err != nil {
		t.Fatalf("refusal mutated source schema: %v", err)
	}
	var stored string
	if err = db.QueryRow(`SELECT domain_id FROM domains`).Scan(&stored); err != nil || stored != domainA {
		t.Fatalf("refusal mutated source identity: %q %v", stored, err)
	}
}

func TestCommandSequenceOwnershipAndConflict(t *testing.T) {
	s, _, peer, k, now := commandFixture(t)
	c := matterCommand(domainB, 1, "alpha")
	h := hashCommand(t, c)
	out, err := s.SubmitCommand(context.Background(), c, h, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	other := matterCommand(repoC, 1, "beta")
	if _, err = s.SubmitCommand(context.Background(), other, hashCommand(t, other), peer, now); !errors.Is(err, ErrExists) {
		t.Fatalf("second command for same sequence: %v", err)
	}
	if _, err = s.SubmitCommand(context.Background(), matterCommand(repoB, 2, "beta"), hashCommand(t, matterCommand(repoB, 2, "beta")), peer, now); !errors.Is(err, ErrPending) {
		t.Fatalf("skipped head: %v", err)
	}
	if _, err = s.CompleteCommand(context.Background(), out.Owner, success(repoB, "alpha"), repoB, repoC, now, signWith(k)); err != nil {
		t.Fatal(err)
	}
	next := matterCommand(repoB, 2, "beta")
	if _, err = s.SubmitCommand(context.Background(), next, hashCommand(t, next), peer, now); err != nil {
		t.Fatalf("next after terminal: %v", err)
	}
}

func TestCommandDerivedLocatorAndCollision(t *testing.T) {
	s, root, peer, k, now := commandFixture(t)
	ctx := context.Background()
	c := matterCommand(domainB, 1, "")
	out, err := s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteCommand(ctx, out.Owner, success(repoB, "a-title"), repoB, repoC, now, signWith(k)); err != nil {
		t.Fatalf("derived locator: %v", err)
	}
	next := matterCommand(repoB, 2, "")
	out, err = s.SubmitCommand(ctx, next, hashCommand(t, next), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteCommand(ctx, out.Owner, success(repoA, "a-title"), repoA, grantA, now, signWith(k)); !errors.Is(err, ErrFenced) {
		t.Fatalf("collision succeeded: %v", err)
	}
	rejected := operation.Result{Code: operation.ResultRejected, Problem: &operation.Problem{Code: operation.ProblemLocatorCollision, Message: "already exists"}}
	if _, err = s.CompleteCommand(ctx, out.Owner, rejected, "", "", now, signWith(k)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var eventCount int
	if err = s.db.QueryRow(`SELECT count(*) FROM authority_events`).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("collision appended event: %d %v", eventCount, err)
	}
}

func TestCommandProcessCrashBoundaries(t *testing.T) {
	if mode := os.Getenv("AUTHORITYSTORE_STEP4_CHILD"); mode != "" {
		s, err := OpenExisting(os.Getenv("AUTHORITYSTORE_STEP4_ROOT"))
		if err != nil {
			panic(err)
		}
		if mode != "before" {
			var leaf, ca []byte
			if err = s.db.QueryRow(`SELECT certificate,ca_certificate FROM environment_certificates WHERE domain_id=? AND environment_id=? AND generation=1`, domainA, envA).Scan(&leaf, &ca); err != nil {
				panic(err)
			}
			parse := func(raw []byte) *x509.Certificate {
				cert, e := x509.ParseCertificate(raw)
				if e != nil {
					panic(e)
				}
				return cert
			}
			peer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{parse(leaf), parse(ca)}}
			c := matterCommand(domainB, 1, "alpha")
			h, e := c.RequestHash()
			if e != nil {
				panic(e)
			}
			now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
			out, e := s.SubmitCommand(context.Background(), c, h, peer, now)
			if e != nil {
				panic(e)
			}
			if mode == "terminal" {
				_, e = s.CompleteCommand(context.Background(), out.Owner, success(repoB, "alpha"), repoB, repoC, now, signWith(key("step4-artifact")))
				if e != nil {
					panic(e)
				}
			}
		}
		fmt.Println("committed")
		time.Sleep(time.Minute) // parent kills the live writer without Close
		return
	}
	for _, mode := range []string{"before", "submitted", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			s, root, peer, _, now := commandFixture(t)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestCommandProcessCrashBoundaries$")
			child.Env = append(os.Environ(), "AUTHORITYSTORE_STEP4_CHILD="+mode, "AUTHORITYSTORE_STEP4_ROOT="+root)
			pipe, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if child.ProcessState == nil {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			})
			ready := make([]byte, len("committed\n"))
			if _, err = io.ReadFull(pipe, ready); err != nil || string(ready) != "committed\n" {
				t.Fatalf("child commit marker %q: %v", ready, err)
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			s, err = OpenExisting(root)
			if err != nil {
				t.Fatalf("reopen after SIGKILL: %v", err)
			}
			defer func() { _ = s.Close() }()
			c := matterCommand(domainB, 1, "alpha")
			h := hashCommand(t, c)
			status, err := s.QueryCommand(context.Background(), domainA, c.ID, h, 7, peer, envA, now)
			switch mode {
			case "before":
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("pre-submission became visible: %v", err)
				}
			case "submitted":
				if err != nil || !status.Pending {
					t.Fatalf("submitted recovery: %+v %v", status, err)
				}
				if _, err = s.RecoverCommand(context.Background(), c, h); err != nil {
					t.Fatalf("lost owner: %v", err)
				}
			case "terminal":
				if err != nil || status.Pending || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
					t.Fatalf("terminal recovery: %+v %v", status, err)
				}
			}
		})
	}
}
