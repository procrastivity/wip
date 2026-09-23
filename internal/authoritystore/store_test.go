package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	domainA = "01KZ7XHAQT1S46NYPN1PW1DX36"
	domainB = "01KZ7XHAQT1S46NYPN1PW1DX37"
	repoA   = "01KZ7XHAQT1S46NYPN1PW1DX38"
	repoB   = "01KZ7XHAQT1S46NYPN1PW1DX39"
	repoC   = "01KZ7XHAQT1S46NYPN1PW1DX3A"
)

func identity(id string, epoch uint64) (Domain, ed25519.PrivateKey) {
	seed := sha256.Sum256([]byte(id))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	der, _ := x509.MarshalPKIXPublicKey(public)
	sum := sha256.Sum256(der)
	return Domain{ID: id, OwnerPublicKey: public, OwnerKeyID: "sha256:" + hex.EncodeToString(sum[:]), ActiveEpoch: epoch}, private
}

func fresh(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "authority")
	s, err := CreateEmpty(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, root
}

func TestFreshCreateOpenAndPublicIdentity(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	d, private := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	d.OwnerPublicKey[0] ^= 0xff // caller's buffer cannot mutate persisted identity
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.LookupDomain(ctx, domainA)
	if err != nil || got.ActiveEpoch != 7 || got.OwnerKeyID != d.OwnerKeyID || got.OwnerPublicKey[0] == d.OwnerPublicKey[0] {
		t.Fatalf("reopened identity: %+v, %v", got, err)
	}
	got.OwnerPublicKey[0] ^= 0xff
	again, err := s.LookupDomain(ctx, domainA)
	if err != nil || bytes.Equal(got.OwnerPublicKey, again.OwnerPublicKey) {
		t.Fatalf("identity alias: %v", err)
	}
	owner, err := s.RepoDomain(ctx, repoA)
	if err != nil || owner != domainA {
		t.Fatalf("Repo owner: %q, %v", owner, err)
	}
	// Private seed and expanded secret key must never be persisted in the DB.
	content, err := os.ReadFile(filepath.Join(root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, private) || bytes.Contains(content, private.Seed()) {
		t.Fatal("private key material in DB")
	}
}

func TestBootstrapRollbackAndImmutableOwner(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	a, _ := identity(domainA, 1)
	b, _ := identity(domainB, 2)
	if err := s.BootstrapDomain(ctx, a, repoA); err != nil {
		t.Fatal(err)
	}
	if err := s.BootstrapDomain(ctx, a, repoB); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate domain: %v", err)
	}
	if _, err := s.RepoDomain(ctx, repoB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial duplicate domain: %v", err)
	}
	if err := s.BootstrapDomain(ctx, b, repoA); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Repo: %v", err)
	}
	if _, err := s.LookupDomain(ctx, domainB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial bootstrap: %v", err)
	}
	wrong, _ := identity(domainA, 9)
	if err := s.BootstrapDomain(ctx, wrong, repoC); !errors.Is(err, ErrExists) {
		t.Fatalf("rebootstrap conflicting owner/epoch: %v", err)
	}
	if err := s.BootstrapDomain(ctx, Domain{ID: domainB, OwnerPublicKey: b.OwnerPublicKey, OwnerKeyID: a.OwnerKeyID, ActiveEpoch: 1}, repoC); err == nil {
		t.Fatal("accepted mismatched key ID")
	}
	if err := s.BootstrapDomain(ctx, Domain{ID: domainB, OwnerPublicKey: b.OwnerPublicKey, OwnerKeyID: b.OwnerKeyID, ActiveEpoch: 0}, repoC); err == nil {
		t.Fatal("accepted zero epoch")
	}
	if err := s.AttachRepo(ctx, domainB, repoC); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown domain: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.BootstrapDomain(canceled, b, repoC); !errors.Is(err, context.Canceled) || errors.Is(err, ErrExists) {
		t.Fatalf("canceled bootstrap classified as duplicate: %v", err)
	}
	if _, err := s.LookupDomain(ctx, domainB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial canceled bootstrap: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.RepoDomain(ctx, repoB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial membership on reopen: %v", err)
	}
	if _, err := s.LookupDomain(ctx, domainB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial domain on reopen: %v", err)
	}
}

func TestSchemaTransactionRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Failure after the first baseline statement must roll back that table
	// and the version marker, rather than produce a plausible v1 header.
	if _, err := db.Exec(`CREATE TABLE domains (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := installBaseline(db); err == nil {
		t.Fatal("expected schema conflict")
	}
	var n, version int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'schema_migrations'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial migration table: %d, %v", n, err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("partial version marker: %d, %v", version, err)
	}
}

func TestOwnerRootSQLGuardAndReopenValidation(t *testing.T) {
	s, root := fresh(t)
	d, _ := identity(domainA, 1)
	if err := s.BootstrapDomain(context.Background(), d, repoA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE domains SET owner_key_id = ? WHERE domain_id = ?`, "replacement", domainA); err == nil {
		t.Fatal("SQL changed owner root")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO epoch_promotions(domain_id, from_epoch, to_epoch, prior_fence_digest, promotion_proof_digest) VALUES (?, 1, 3, ?, ?)`, domainA, digest('a'), digest('b')); err == nil {
		t.Fatal("accepted skipped epoch")
	}
	if _, err := db.Exec(`UPDATE domains SET owner_key_id = ? WHERE domain_id = ?`, "replacement", domainA); err == nil {
		t.Fatal("offline SQL changed owner root")
	}
	if _, err := db.Exec(`DROP TRIGGER owner_root_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE domains SET owner_key_id = ? WHERE domain_id = ?`, "replacement", domainA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER owner_root_immutable BEFORE UPDATE OF domain_id, owner_public_key, owner_key_id, initial_epoch ON domains BEGIN SELECT RAISE(ABORT, 'immutable owner root'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted mismatched persisted owner ID: %v", err)
	}
}

func digest(c byte) string { return "sha256:" + string(bytes.Repeat([]byte{c}, 64)) }

func TestPromotionChainReopen(t *testing.T) {
	s, root := fresh(t)
	d, _ := identity(domainA, 5)
	if err := s.BootstrapDomain(context.Background(), d, repoA); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Emulate only the Step 7 SQL commit shape to test persistent chain
	// validation. This is not an activation API or proof verification.
	for _, pair := range [][2]int{{5, 6}, {6, 7}} {
		if _, err := db.Exec(`INSERT INTO epoch_promotions(domain_id, from_epoch, to_epoch, prior_fence_digest, promotion_proof_digest) VALUES (?, ?, ?, ?, ?)`, domainA, pair[0], pair[1], digest('a'), digest('b')); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE domains SET active_epoch = 7 WHERE domain_id = ?`, domainA); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.LookupDomain(context.Background(), domainA)
	if err != nil || got.ActiveEpoch != 7 {
		t.Fatalf("promoted epoch: %+v, %v", got, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE epoch_promotions SET prior_fence_digest = ? WHERE domain_id = ? AND to_epoch = 6`, digest('c'), domainA); err == nil {
		t.Fatal("rewrote historical fence")
	}
	if _, err := db.Exec(`DELETE FROM epoch_promotions WHERE domain_id = ? AND to_epoch = 6`, domainA); err == nil {
		t.Fatal("deleted historical fence")
	}
	if _, err := db.Exec(`UPDATE domains SET active_epoch = 8 WHERE domain_id = ?`, domainA); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted missing successor proof: %v", err)
	}
}

func TestCrossDomainAttachRace(t *testing.T) {
	s, _ := fresh(t)
	ctx := context.Background()
	a, _ := identity(domainA, 1)
	b, _ := identity(domainB, 1)
	if err := s.BootstrapDomain(ctx, a, repoA); err != nil {
		t.Fatal(err)
	}
	if err := s.BootstrapDomain(ctx, b, repoB); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := domainA
			if i%2 == 1 {
				id = domainB
			}
			results <- s.AttachRepo(ctx, id, repoC)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrExists) {
			t.Fatalf("raced attachment: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winners, want one", winners)
	}
	owner, err := s.RepoDomain(ctx, repoC)
	if err != nil || (owner != domainA && owner != domainB) {
		t.Fatalf("global Repo owner: %q, %v", owner, err)
	}
	other := domainA
	if owner == other {
		other = domainB
	}
	if err := s.AttachRepo(ctx, other, repoC); !errors.Is(err, ErrExists) {
		t.Fatalf("cross-domain duplicate: %v", err)
	}
}

func TestConcurrentBootstrapSameRepoRollsBackLoser(t *testing.T) {
	s, root := fresh(t)
	a, _ := identity(domainA, 1)
	b, _ := identity(domainB, 1)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, d := range []Domain{a, b} {
		go func(d Domain) {
			<-start
			results <- s.BootstrapDomain(context.Background(), d, repoA)
		}(d)
	}
	close(start)
	winners := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrExists) {
			t.Fatalf("bootstrap race: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("bootstrap winners: %d", winners)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	owner, err := reopened.RepoDomain(context.Background(), repoA)
	if err != nil {
		t.Fatal(err)
	}
	loser := domainA
	if owner == loser {
		loser = domainB
	}
	if _, err := reopened.LookupDomain(context.Background(), loser); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loser left partial domain: %v", err)
	}
}

func TestOpeningRefusesMissingPartialNewerAndLegacy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("missing root: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open made root: %v", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("partial root: %v", err)
	}
	if _, err := CreateEmpty(root); !errors.Is(err, os.ErrExist) {
		t.Fatalf("create reused partial root: %v", err)
	}
	for _, tc := range []struct{ name, sql string }{
		{"legacy", `CREATE TABLE events (id TEXT)`},
		{"newer", `PRAGMA user_version = 5`},
		{"unversioned-new-schema", `CREATE TABLE future_records (id INTEGER)`},
		{"missing-table", `DROP TABLE repo_memberships`},
		{"missing-marker", `DELETE FROM schema_migrations`},
		{"broken-owner", `DROP TRIGGER owner_root_immutable`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := fresh(t)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(path, "authority.db"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "legacy" {
				if err := os.Remove(filepath.Join(path, "authority.db")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if _, err := OpenExisting(path); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("open accepted %s: %v", tc.name, err)
			}
		})
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(link); err == nil {
		t.Fatal("accepted symlink root")
	}

	t.Run("altered-trigger-definition", func(t *testing.T) {
		store, path := fresh(t)
		domain, _ := identity(domainA, 1)
		if err := store.BootstrapDomain(context.Background(), domain, repoA); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(path, "authority.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TRIGGER owner_root_immutable;
CREATE TRIGGER owner_root_immutable BEFORE UPDATE OF owner_key_id ON domains
BEGIN SELECT 1; END`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenExisting(path); !errors.Is(err, ErrInvalidStore) {
			t.Fatalf("open accepted altered owner trigger: %v", err)
		}
	})
}

func TestExclusiveWriterAndCrashReopen(t *testing.T) {
	if os.Getenv("AUTHORITYSTORE_CHILD") == "1" {
		child, err := OpenExisting(os.Getenv("AUTHORITYSTORE_ROOT"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = child
		fmt.Println("locked")
		time.Sleep(time.Minute) // parent kills process while lease is held
		return
	}
	s, root := fresh(t)
	if _, err := OpenExisting(root); !errors.Is(err, ErrHeld) {
		t.Fatalf("second writable open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExclusiveWriterAndCrashReopen$")
	cmd.Env = append(os.Environ(), "AUTHORITYSTORE_CHILD=1", "AUTHORITYSTORE_ROOT="+root)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready := make([]byte, len("locked\n"))
	if _, err := io.ReadFull(pipe, ready); err != nil {
		t.Fatal(err)
	}
	if string(ready) != "locked\n" {
		t.Fatalf("child readiness: %q", ready)
	}
	if _, err := OpenExisting(root); !errors.Is(err, ErrHeld) {
		t.Fatalf("child-held writer: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("after crash: %v", err)
	}
	defer func() { _ = reopened.Close() }()
}
