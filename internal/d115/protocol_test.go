package d115

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestGrantSubmitAndReceiptCanonicalRoundTrips(t *testing.T) {
	_, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := KeyID(ownerPrivate.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	claims, err := EncodeClaims(Claims{
		Schema: ClaimsSchema, GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Audience: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		Scope: CreateDomainScope, OwnerPublicKey: ownerPrivate.Public().(ed25519.PublicKey), OwnerKeyID: ownerID,
		IssuedAt: now, NotBefore: now, ExpiresAt: "2026-10-14T12:10:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	operatorPublic, operatorPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	operatorID, err := KeyID(operatorPublic)
	if err != nil {
		t.Fatal(err)
	}
	grant := Grant{Schema: GrantSchema, Claims: claims, SignerKeyID: operatorID, Signature: make([]byte, ed25519.SignatureSize)}
	preimage, err := GrantSigningPreimage(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Signature = ed25519.Sign(operatorPrivate, preimage)
	grantBytes, err := EncodeGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	decodedGrant, decodedClaims, err := DecodeGrant(grantBytes)
	if err != nil || !VerifyGrantSignature(decodedGrant, operatorPublic) || decodedClaims.OwnerKeyID != ownerID {
		t.Fatalf("decoded grant/claims/signature = %+v / %+v / %v", decodedGrant, decodedClaims, err)
	}
	digest := GrantDigest(grantBytes)
	commandID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	requestHash, err := RequestHash(decodedClaims.Audience, commandID, digest)
	if err != nil {
		t.Fatal(err)
	}
	ownerPreimage, err := OwnerProofPreimage(decodedClaims.Audience, commandID, requestHash, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	submit := Submit{
		Schema: SubmitSchema, RealmID: decodedClaims.Audience, CommandID: commandID,
		Grant: grantBytes, RequestHash: requestHash, OwnerProof: ed25519.Sign(ownerPrivate, ownerPreimage),
	}
	submitBytes, err := EncodeSubmit(submit)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeSubmit(submitBytes); err != nil || got.CommandID != commandID {
		t.Fatalf("DecodeSubmit() = %+v, %v", got, err)
	}
	receipt := Receipt{
		Schema: ReceiptSchema, RealmID: decodedClaims.Audience, CommandID: commandID,
		RequestHash: requestHash, Operation: Operation{"repo.create-local", 1}, ResultCode: "result.succeeded",
		DomainID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", RepoID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", InitialEpoch: 1,
		OwnerKeyID: ownerID, GrantID: decodedClaims.GrantID, CommittedAt: now,
	}
	receiptBytes, err := EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeReceipt(receiptBytes); err != nil || got != receipt {
		t.Fatalf("DecodeReceipt() = %+v, %v", got, err)
	}
}

func TestGrantDecoderRejectsNoncanonicalAndOversizedInput(t *testing.T) {
	if _, _, err := DecodeGrant(make([]byte, 4097)); err == nil {
		t.Fatal("oversized grant accepted")
	}
	if _, err := DecodeSubmit([]byte{0xbf, 0xff}); err == nil {
		t.Fatal("indefinite CBOR submit accepted")
	}
	if validTime("2026-10-14T12:00:00+00:00") || validTime("2026-10-14T12:00:00.000000000Z") {
		t.Fatal("alternate RFC3339Nano spelling accepted")
	}
	if validTime(strings.Repeat("x", 30)) {
		t.Fatal("malformed timestamp accepted")
	}
}
