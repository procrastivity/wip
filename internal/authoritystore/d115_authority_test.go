package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/d115"
)

func TestD115CreateIsAtomicOneUseAndReplayFirst(t *testing.T) {
	store, _ := fresh(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	operatorPub, operatorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := d115.KeyID(operatorPub)
	ownerID, _ := d115.KeyID(ownerPub)
	trust, err := store.D115Trust(context.Background())
	if err != nil || trust.Enabled || trust.Generation != 1 {
		t.Fatalf("initial D115 trust = %+v, %v", trust, err)
	}
	trust, err = store.UpdateD115Trust(context.Background(), trust.Generation, true, &D115OperatorKey{
		KeyID: operatorID, PublicKey: operatorPub, NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano),
		NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, "")
	if err != nil || !trust.Enabled || trust.Generation != 2 {
		t.Fatalf("enable D115 trust = %+v, %v", trust, err)
	}
	encodedGrant := testD115Grant(t, trust.RealmID, now, operatorID, operatorPriv, ownerPub)
	commandID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	request := testD115Submit(t, trust.RealmID, commandID, encodedGrant, ownerPriv, ownerID)
	receiptBytes, err := store.SubmitCreateDomainAt(context.Background(), request, now)
	if err != nil {
		t.Fatalf("SubmitCreateDomainAt(): %v", err)
	}
	receipt, err := d115.DecodeReceipt(receiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	retainedReceipt, err := store.VerifyD115CreateDomainReceipt(context.Background(), receiptBytes)
	if err != nil || retainedReceipt != receipt {
		t.Fatalf("verify exact committed D115 receipt = %+v, %v; want %+v", retainedReceipt, err, receipt)
	}
	if _, err = store.VerifyD115CreateDomainReceipt(context.Background(), append(bytes.Clone(receiptBytes), 0)); err == nil {
		t.Fatal("D115 receipt verifier accepted bytes different from the retained command result")
	}
	if receipt.InitialEpoch != 1 {
		t.Fatalf("created authority epoch = %d", receipt.InitialEpoch)
	}
	replayed, err := store.SubmitCreateDomainAt(context.Background(), request, now.Add(24*time.Hour))
	if err != nil || string(replayed) != string(receiptBytes) {
		t.Fatalf("expired exact replay differs: equal=%t err=%v", string(replayed) == string(receiptBytes), err)
	}
	if _, err = store.UpdateD115Trust(context.Background(), trust.Generation, false, nil, operatorID); err != nil {
		t.Fatalf("revoke and disable after committed create: %v", err)
	}
	replayed, err = store.SubmitCreateDomainAt(context.Background(), request, now.Add(48*time.Hour))
	if err != nil || string(replayed) != string(receiptBytes) {
		t.Fatalf("revoked exact replay differs: equal=%t err=%v", string(replayed) == string(receiptBytes), err)
	}
	domain, err := store.LookupDomain(context.Background(), receipt.DomainID)
	if err != nil || domain.OwnerKeyID != ownerID || domain.ActiveEpoch != 1 {
		t.Fatalf("persisted domain = %+v, %v", domain, err)
	}
	if got, err := store.RepoDomain(context.Background(), receipt.RepoID); err != nil || got != receipt.DomainID {
		t.Fatalf("persisted membership = %q, %v", got, err)
	}
	var consumed, commands, genesis, routes int
	for query, dest := range map[string]*int{
		`SELECT count(*) FROM d115_grant_consumptions`:               &consumed,
		`SELECT count(*) FROM d115_create_domain_commands`:           &commands,
		`SELECT count(*) FROM d115_domain_genesis WHERE domain_id=?`: &genesis,
		`SELECT count(*) FROM repo_route_index WHERE repo_id=?`:      &routes,
	} {
		var scanErr error
		switch query {
		case `SELECT count(*) FROM d115_domain_genesis WHERE domain_id=?`:
			scanErr = store.db.QueryRow(query, receipt.DomainID).Scan(dest)
		case `SELECT count(*) FROM repo_route_index WHERE repo_id=?`:
			scanErr = store.db.QueryRow(query, receipt.RepoID).Scan(dest)
		default:
			scanErr = store.db.QueryRow(query).Scan(dest)
		}
		if scanErr != nil {
			t.Fatal(scanErr)
		}
	}
	if consumed != 1 || commands != 1 || genesis != 1 || routes != 0 {
		t.Fatalf("D115 durable state: consumed=%d commands=%d genesis=%d routes=%d", consumed, commands, genesis, routes)
	}
	// An otherwise valid grant cannot be consumed under a second command.
	second := testD115Submit(t, trust.RealmID, "01ARZ3NDEKTSV4RRFFQ69G5FAY", encodedGrant, ownerPriv, ownerID)
	if _, err = store.SubmitCreateDomainAt(context.Background(), second, now); !errors.Is(err, ErrD115Refused) && !errors.Is(err, ErrD115GrantConsumed) {
		t.Fatalf("disabled/consumed grant got %v", err)
	}
}

func TestD115ConcurrentSameCommandReturnsOneReceipt(t *testing.T) {
	store, _ := fresh(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	operatorPub, operatorPriv, _ := ed25519.GenerateKey(rand.Reader)
	ownerPub, ownerPriv, _ := ed25519.GenerateKey(rand.Reader)
	operatorID, _ := d115.KeyID(operatorPub)
	ownerID, _ := d115.KeyID(ownerPub)
	trust, _ := store.D115Trust(context.Background())
	trust, err := store.UpdateD115Trust(context.Background(), trust.Generation, true, &D115OperatorKey{
		KeyID: operatorID, PublicKey: operatorPub, NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano),
		NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	grant := testD115Grant(t, trust.RealmID, now, operatorID, operatorPriv, ownerPub)
	request := testD115Submit(t, trust.RealmID, "01ARZ3NDEKTSV4RRFFQ69G5FAX", grant, ownerPriv, ownerID)
	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan []byte, callers)
	errorsSeen := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, callErr := store.SubmitCreateDomainAt(context.Background(), request, now)
			if callErr != nil {
				errorsSeen <- callErr
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent create: %v", err)
	}
	var first []byte
	for result := range results {
		if first == nil {
			first = result
		} else if string(result) != string(first) {
			t.Error("concurrent callers received different receipts")
		}
	}
	if first == nil {
		t.Fatal("no concurrent caller received receipt")
	}
	var domains, memberships, commands, consumed int
	if err = store.db.QueryRow(`SELECT count(*) FROM domains`).Scan(&domains); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM repo_memberships`).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM d115_create_domain_commands`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM d115_grant_consumptions`).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if domains != 1 || memberships != 1 || commands != 1 || consumed != 1 {
		t.Fatalf("concurrent D115 rows = domains:%d memberships:%d commands:%d consumed:%d", domains, memberships, commands, consumed)
	}
}

func TestD115GrantExpiryUsesSerializedAdmissionTime(t *testing.T) {
	store, _ := fresh(t)
	ctx := context.Background()
	issued := time.Now().UTC().Truncate(time.Second)
	expires := issued.Add(2 * time.Second)
	operatorPublic, operatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	ownerPublic, ownerPrivate, _ := ed25519.GenerateKey(rand.Reader)
	operatorID, _ := d115.KeyID(operatorPublic)
	ownerID, _ := d115.KeyID(ownerPublic)
	trust, err := store.D115Trust(ctx)
	if err != nil {
		t.Fatal(err)
	}
	trust, err = store.UpdateD115Trust(ctx, trust.Generation, true, &D115OperatorKey{
		KeyID: operatorID, PublicKey: operatorPublic, NotBefore: issued.Add(-time.Minute).Format(time.RFC3339Nano),
		NotAfter: issued.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	grantID, err := d115.NewULID(issued)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := d115.EncodeClaims(d115.Claims{
		Schema: d115.ClaimsSchema, GrantID: grantID, Audience: trust.RealmID, Scope: d115.CreateDomainScope,
		OwnerPublicKey: ownerPublic, OwnerKeyID: ownerID, IssuedAt: issued.Format(time.RFC3339Nano),
		NotBefore: issued.Format(time.RFC3339Nano), ExpiresAt: expires.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := d115.Grant{Schema: d115.GrantSchema, Claims: claims, SignerKeyID: operatorID, Signature: make([]byte, ed25519.SignatureSize)}
	grantPreimage, err := d115.GrantSigningPreimage(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Signature = ed25519.Sign(operatorPrivate, grantPreimage)
	encodedGrant, err := d115.EncodeGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	request := testD115Submit(t, trust.RealmID, "01ARZ3NDEKTSV4RRFFQ69G5FAX", encodedGrant, ownerPrivate, ownerID)

	admissionTime := issued.Add(time.Second)
	clockCalled := make(chan struct{}, 1)
	started := make(chan struct{})
	result := make(chan error, 1)
	store.mu.Lock()
	go func() {
		close(started)
		_, submitErr := store.submitCreateDomain(ctx, request, func() time.Time {
			clockCalled <- struct{}{}
			return admissionTime
		})
		result <- submitErr
	}()
	<-started
	select {
	case <-clockCalled:
		store.mu.Unlock()
		t.Fatal("validity clock sampled before the store writer lock was acquired")
	case <-time.After(20 * time.Millisecond):
	}
	// Simulate contention that spans the grant's expiry. The admission clock
	// must be observed only after the waiting submit acquires serialization.
	admissionTime = expires.Add(time.Nanosecond)
	store.mu.Unlock()
	if err = <-result; !errors.Is(err, ErrD115Refused) {
		t.Fatalf("expired-at-admission grant error = %v; want refusal", err)
	}
}

func testD115Grant(t *testing.T, realm string, now time.Time, keyID string, private ed25519.PrivateKey, owner ed25519.PublicKey) []byte {
	t.Helper()
	ownerID, _ := d115.KeyID(owner)
	claims, err := d115.EncodeClaims(d115.Claims{
		Schema: d115.ClaimsSchema, GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Audience: realm,
		Scope: d115.CreateDomainScope, OwnerPublicKey: owner, OwnerKeyID: ownerID, IssuedAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		NotBefore: now.Add(-time.Second).Format(time.RFC3339Nano), ExpiresAt: now.Add(9 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := d115.Grant{Schema: d115.GrantSchema, Claims: claims, SignerKeyID: keyID, Signature: make([]byte, ed25519.SignatureSize)}
	preimage, err := d115.GrantSigningPreimage(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Signature = ed25519.Sign(private, preimage)
	encoded, err := d115.EncodeGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testD115Submit(t *testing.T, realm, commandID string, grant []byte, owner ed25519.PrivateKey, ownerID string) []byte {
	t.Helper()
	hash, err := d115.RequestHash(realm, commandID, d115.GrantDigest(grant))
	if err != nil {
		t.Fatal(err)
	}
	preimage, err := d115.OwnerProofPreimage(realm, commandID, hash, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := d115.EncodeSubmit(d115.Submit{
		Schema: d115.SubmitSchema, RealmID: realm, CommandID: commandID,
		Grant: grant, RequestHash: hash, OwnerProof: ed25519.Sign(owner, preimage),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestD115TrustCasRejectsStaleAndKeepsDefaultDisabled(t *testing.T) {
	store, _ := fresh(t)
	state, err := store.D115Trust(context.Background())
	if err != nil || state.Enabled || state.Generation != 1 {
		t.Fatalf("initial trust = %+v, %v", state, err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keyID, _ := d115.KeyID(pub)
	if _, err = store.UpdateD115Trust(context.Background(), 0, false, &D115OperatorKey{
		KeyID: keyID, PublicKey: pub,
		NotBefore: "2026-10-14T00:00:00Z", NotAfter: "2026-10-15T00:00:00Z", State: "active",
	}, ""); !errors.Is(err, ErrD115Generation) {
		t.Fatalf("stale trust generation error = %v", err)
	}
	state, err = store.D115Trust(context.Background())
	if err != nil || state.Generation != 1 || state.Enabled {
		t.Fatalf("failed CAS changed trust = %+v, %v", state, err)
	}
}

func TestD115TrustRotationIsAtomicAndNeverOverlapsActiveKeys(t *testing.T) {
	store, _ := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	oldPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	oldID, _ := d115.KeyID(oldPublic)
	state, err := store.D115Trust(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.UpdateD115Trust(ctx, state.Generation, true, &D115OperatorKey{
		KeyID: oldID, PublicKey: oldPublic,
		NotBefore: now.Add(-time.Hour).Format(time.RFC3339Nano), NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	newPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	newID, _ := d115.KeyID(newPublic)
	state, err = store.UpdateD115Trust(ctx, state.Generation, true, &D115OperatorKey{
		KeyID: newID, PublicKey: newPublic,
		NotBefore: now.Add(-time.Hour).Format(time.RFC3339Nano), NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, oldID)
	if err != nil || state.Generation != 3 {
		t.Fatalf("atomic rotation = %+v, %v", state, err)
	}
	var oldState, newState string
	if err = store.db.QueryRow(`SELECT state FROM d115_operator_keys WHERE key_id=?`, oldID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT state FROM d115_operator_keys WHERE key_id=?`, newID).Scan(&newState); err != nil {
		t.Fatal(err)
	}
	var active int
	if err = store.db.QueryRow(`SELECT count(*) FROM d115_operator_keys WHERE state='active'`).Scan(&active); err != nil || active != 1 || oldState != "revoked" || newState != "active" {
		t.Fatalf("post-rotation states old=%s new=%s active=%d err=%v", oldState, newState, active, err)
	}
	// Force a post-revocation insert conflict. The generation, prior active
	// key, and tombstone state must all roll back together.
	if _, err = store.UpdateD115Trust(ctx, state.Generation, true, &D115OperatorKey{
		KeyID: oldID, PublicKey: oldPublic,
		NotBefore: now.Add(-time.Hour).Format(time.RFC3339Nano), NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, newID); err == nil {
		t.Fatal("duplicate key ID rotation unexpectedly succeeded")
	}
	stateAfter, err := store.D115Trust(ctx)
	if err != nil || stateAfter.Generation != state.Generation || !stateAfter.Enabled {
		t.Fatalf("failed rotation changed registry state: %+v %v", stateAfter, err)
	}
	if err = store.db.QueryRow(`SELECT state FROM d115_operator_keys WHERE key_id=?`, newID).Scan(&newState); err != nil || newState != "active" {
		t.Fatalf("failed rotation revoked prior active key: state=%q err=%v", newState, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM d115_operator_keys WHERE state='active'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("failed rotation left active-key overlap or absence: active=%d err=%v", active, err)
	}
}
