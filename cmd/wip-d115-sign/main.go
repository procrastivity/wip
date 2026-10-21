// Package main implements the one-shot D115 owner-signing command.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/wipdseed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("wip-d115-sign", flag.ContinueOnError)
	store := flags.String("bundle-store", "", "explicit owner-private public trust bundle directory")
	requestPath := flags.String("request-file", "", "pinned owner-signing request file")
	proofPath := flags.String("proof-out", "", "new detached proof output path")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *store == "" || *requestPath == "" || *proofPath == "" {
		return errors.New("wip-d115-sign requires --bundle-store, --request-file, and --proof-out")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("controlling TTY required")
	}
	defer func() { _ = tty.Close() }()
	requestBytes, err := wipdseed.ReadD115OwnerFile(*requestPath, 8192)
	if err != nil {
		return err
	}
	defer clear(requestBytes)
	request, err := d115.DecodeOwnerSigningRequest(requestBytes)
	if err != nil {
		return errors.New("signing.request-invalid")
	}
	bundleBytes, bundle, err := wipdseed.LoadD115OperatorTrustBundle(*store)
	if err != nil {
		return errors.New("signing.trust-bundle-invalid")
	}
	defer clear(bundleBytes)
	if bundle.RealmID != request.RealmID || !bundle.Enabled {
		return errors.New("signing.trust-bundle-unavailable")
	}
	grant, claims, err := d115.DecodeGrant(request.GrantArtifact)
	requestHash, hashErr := d115.RequestHash(request.RealmID, request.CommandID, request.GrantDigest)
	expectedPreimage, proofErr := d115.OwnerProofPreimage(request.RealmID, request.CommandID, request.RequestHash, request.OwnerKeyID)
	if err != nil || hashErr != nil || proofErr != nil || requestHash != request.RequestHash || string(expectedPreimage) != string(request.Preimage) ||
		claims.OwnerKeyID != request.OwnerKeyID || claims.Scope != d115.CreateDomainScope || claims.Audience != request.RealmID || d115.GrantDigest(request.GrantArtifact) != request.GrantDigest {
		return errors.New("signing.request-mismatch")
	}
	now := time.Now().UTC()
	issued, issuedErr := time.Parse(time.RFC3339Nano, claims.IssuedAt)
	notBefore, beforeErr := time.Parse(time.RFC3339Nano, claims.NotBefore)
	expires, expiryErr := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	if issuedErr != nil || beforeErr != nil || expiryErr != nil || issued.After(now) || now.Before(notBefore) || !now.Before(expires) ||
		!issued.Before(expires) || expires.Sub(issued) > 10*time.Minute {
		return errors.New("signing.grant-outside-validity")
	}
	keyID := ""
	for _, key := range bundle.Keys {
		keyNotBefore, keyBeforeErr := time.Parse(time.RFC3339Nano, key.NotBefore)
		keyNotAfter, keyAfterErr := time.Parse(time.RFC3339Nano, key.NotAfter)
		if key.KeyID == grant.SignerKeyID && key.State == "active" && key.Scope == d115.CreateDomainScope && key.Audience == request.RealmID &&
			keyBeforeErr == nil && keyAfterErr == nil && !now.Before(keyNotBefore) && now.Before(keyNotAfter) && d115.VerifyGrantSignature(grant, key.PublicKey) {
			keyID = key.KeyID
			break
		}
	}
	if keyID == "" {
		return errors.New("signing.operator-key-untrusted")
	}
	_, err = fmt.Fprintf(tty, "realm: %s\nscope: %s\nowner_key_id: %s\noperator_key_id: %s\nbundle_generation: %d\ncommand_id: %s\nrequest_hash: %s\neffect: create one new default domain and its initial Repo membership; the authority mints both IDs\nConfirm signing? Type SIGN:\n", request.RealmID, claims.Scope, claims.OwnerKeyID, keyID, bundle.Generation, request.CommandID, request.RequestHash)
	if err != nil {
		return err
	}
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "SIGN" {
		return errors.New("signing.owner-refused")
	}
	proof, err := wipdseed.OwnerSignD115Request(context.Background(), requestBytes, nil, bundleBytes)
	if err != nil {
		return err
	}
	defer clear(proof)
	return wipdseed.WriteD115OwnerFile(*proofPath, proof)
}
