package wipdseed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"time"

	"github.com/procrastivity/wip/internal/d115"
)

// D115LocalState is the minimal public local-IPC view of a durable attempt.
type D115LocalState struct {
	AttemptID      string
	State          D115Disposition
	SigningRequest []byte
	Receipt        []byte
}

// D115AttachReceipt is a receipt proven to be the installed terminal result
// of one exact D115 attempt on the configured pinned provisioning endpoint.
type D115AttachReceipt struct {
	AttemptID       string
	CheckoutBinding string
	RealmID         string
	AuthoritySPKI   string
	Bytes           []byte
	Receipt         d115.Receipt
}

// LoadInstalledD115ReceiptForAttach retrieves only an installed, exact receipt
// whose durable attempt is bound to the currently authenticated checkout.
func LoadInstalledD115ReceiptForAttach(profileRoot, commandID, checkoutBinding string) (D115AttachReceipt, error) {
	var empty D115AttachReceipt
	if commandID == "" || !validD115CheckoutBinding(checkoutBinding) {
		return empty, ErrD115LocalRefused
	}
	config, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		return empty, err
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return empty, err
	}
	attempt, err := ledger.findByCommandID(commandID)
	if err != nil || attempt.Disposition != D115Installed || attempt.CheckoutBinding != checkoutBinding ||
		attempt.RealmID != config.ProvisioningRealmID || len(attempt.ReceiptBytes) == 0 {
		return empty, ErrD115LocalRefused
	}
	receipt, err := d115.DecodeReceipt(attempt.ReceiptBytes)
	if err != nil || receipt.RealmID != attempt.RealmID || receipt.CommandID != attempt.CommandID ||
		receipt.RequestHash != attempt.RequestHash || receipt.OwnerKeyID != attempt.OwnerKeyID || receipt.GrantID != attempt.GrantID {
		return empty, ErrInvalidClientState
	}
	path := filepath.Join(ledger.directory, "receipts", commandID+".cbor")
	installed, err := readPrivateRegular(path, 8192, 0o600)
	if err != nil {
		return empty, ErrInvalidClientState
	}
	defer clear(installed)
	if !bytes.Equal(installed, attempt.ReceiptBytes) {
		return empty, ErrInvalidClientState
	}
	return D115AttachReceipt{
		AttemptID: attempt.CommandID, CheckoutBinding: attempt.CheckoutBinding,
		RealmID: attempt.RealmID, AuthoritySPKI: config.AuthoritySPKIPin,
		Bytes: bytes.Clone(attempt.ReceiptBytes), Receipt: receipt,
	}, nil
}

// ListInstalledD115ReceiptsForAttach returns only installed receipts bound to
// this live checkout and the currently configured provisioning realm and
// endpoint. Callers must still select exactly one receipt by the owner grant's
// domain/Repo/route claims and refuse ambiguity.
func ListInstalledD115ReceiptsForAttach(profileRoot, checkoutBinding string) ([]D115AttachReceipt, error) {
	if !validD115CheckoutBinding(checkoutBinding) {
		return nil, ErrD115LocalRefused
	}
	config, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		return nil, err
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return nil, err
	}
	attempts, err := ledger.loadAll()
	if err != nil {
		return nil, err
	}
	receipts := make([]D115AttachReceipt, 0)
	for _, attempt := range attempts {
		if attempt.Disposition != D115Installed || attempt.RealmID != config.ProvisioningRealmID || attempt.CheckoutBinding != checkoutBinding {
			continue
		}
		receipt, loadErr := LoadInstalledD115ReceiptForAttach(profileRoot, attempt.CommandID, checkoutBinding)
		if loadErr != nil {
			return nil, loadErr
		}
		if receipt.RealmID != config.ProvisioningRealmID || receipt.AuthoritySPKI != config.AuthoritySPKIPin {
			return nil, ErrInvalidClientState
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

// PrepareD115CreateLocal durably reserves the one intent for this exact realm,
// grant digest, and daemon-derived checkout correlation before returning the
// signing request. It performs no signing or network operation.
func PrepareD115CreateLocal(profileRoot, grantArtifact, checkoutBinding string) (D115LocalState, error) {
	if !validD115CheckoutBinding(checkoutBinding) {
		return D115LocalState{}, ErrInvalidClientState
	}
	config, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		return D115LocalState{}, err
	}
	grantBytes, grant, claims, err := d115.DecodeGrantArtifact(grantArtifact)
	if err != nil || claims.Audience != config.ProvisioningRealmID || claims.Scope != d115.CreateDomainScope || grant.SignerKeyID == claims.OwnerKeyID {
		return D115LocalState{}, ErrD115LocalRefused
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return D115LocalState{}, err
	}
	grantDigest := d115.GrantDigest(grantBytes)
	commandID, err := d115.NewULID(time.Now().UTC())
	if err != nil {
		return D115LocalState{}, err
	}
	requestHash, err := d115.RequestHash(config.ProvisioningRealmID, commandID, grantDigest)
	if err != nil {
		return D115LocalState{}, err
	}
	preimage, err := d115.OwnerProofPreimage(config.ProvisioningRealmID, commandID, requestHash, claims.OwnerKeyID)
	if err != nil {
		return D115LocalState{}, err
	}
	signingRequest, err := d115.EncodeOwnerSigningRequest(d115.OwnerSigningRequest{
		Schema: d115.SigningRequestSchema, RealmID: config.ProvisioningRealmID, CommandID: commandID,
		RequestHash: requestHash, GrantDigest: grantDigest, GrantArtifact: grantBytes,
		OwnerKeyID: claims.OwnerKeyID, Preimage: preimage,
	})
	if err != nil {
		return D115LocalState{}, err
	}
	attempt, err := ledger.getOrCreate(D115Attempt{
		RealmID: config.ProvisioningRealmID, GrantDigest: grantDigest, CheckoutBinding: checkoutBinding,
		CommandID: commandID, RequestHash: requestHash, OwnerKeyID: claims.OwnerKeyID, GrantID: claims.GrantID,
		SigningRequest: signingRequest, Disposition: D115PendingSignature, Exchanges: []D115Exchange{},
	})
	if err != nil {
		return D115LocalState{}, err
	}
	if len(attempt.SigningRequest) == 0 || attempt.CommandID == "" {
		return D115LocalState{}, ErrInvalidClientState
	}
	return d115State(attempt), nil
}

// ImportD115OwnerProof validates an imported detached proof against the exact
// pinned signing request and atomically persists proof plus submit bytes. It
// never contacts the authority.
func ImportD115OwnerProof(profileRoot, commandID string, proofBytes []byte) (D115LocalState, error) {
	if len(proofBytes) > 2048 {
		return D115LocalState{}, ErrD115LocalRefused
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return D115LocalState{}, err
	}
	attempt, err := ledger.findByCommandID(commandID)
	if err != nil {
		return D115LocalState{}, ErrD115LocalRefused
	}
	if attempt.Disposition != D115PendingSignature {
		return D115LocalState{}, ErrD115LocalRefused
	}
	request, err := d115.DecodeOwnerSigningRequest(attempt.SigningRequest)
	proof, proofErr := d115.DecodeOwnerProofArtifact(proofBytes)
	_, claims, grantErr := d115.DecodeGrant(request.GrantArtifact)
	if err != nil || proofErr != nil || grantErr != nil ||
		request.RealmID != attempt.RealmID || request.CommandID != attempt.CommandID || request.RequestHash != attempt.RequestHash ||
		request.GrantDigest != attempt.GrantDigest || request.OwnerKeyID != attempt.OwnerKeyID ||
		proof.RealmID != request.RealmID || proof.CommandID != request.CommandID || proof.RequestHash != request.RequestHash ||
		proof.GrantDigest != request.GrantDigest || proof.OwnerKeyID != request.OwnerKeyID ||
		d115.GrantDigest(request.GrantArtifact) != request.GrantDigest || claims.OwnerKeyID != request.OwnerKeyID ||
		!ed25519.Verify(ed25519.PublicKey(claims.OwnerPublicKey), request.Preimage, proof.OwnerProof) {
		return D115LocalState{}, ErrD115LocalRefused
	}
	expectedPreimage, err := d115.OwnerProofPreimage(request.RealmID, request.CommandID, request.RequestHash, request.OwnerKeyID)
	if err != nil || !bytes.Equal(expectedPreimage, request.Preimage) {
		return D115LocalState{}, ErrD115LocalRefused
	}
	profile, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil || time.Now().UTC().Before(parseD115Time(claims.NotBefore)) || !time.Now().UTC().Before(parseD115Time(claims.ExpiresAt)) ||
		profile.ProvisioningRealmID != request.RealmID {
		_, _ = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
			current.Disposition = D115Refused
			return nil
		})
		return D115LocalState{}, ErrD115LocalRefused
	}
	submit, err := d115.EncodeSubmit(d115.Submit{
		Schema: d115.SubmitSchema, RealmID: request.RealmID, CommandID: request.CommandID,
		Grant: request.GrantArtifact, RequestHash: request.RequestHash, OwnerProof: proof.OwnerProof,
	})
	if err != nil {
		return D115LocalState{}, ErrD115LocalRefused
	}
	attempt, err = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		if current.Disposition != D115PendingSignature || current.CommandID != request.CommandID ||
			(len(current.RequestBytes) != 0 && !bytes.Equal(current.RequestBytes, submit)) {
			return ErrInvalidClientState
		}
		current.ProofArtifact = bytes.Clone(proofBytes)
		current.RequestBytes = bytes.Clone(submit)
		current.Disposition = D115Prepared
		return nil
	})
	if err != nil {
		return D115LocalState{}, err
	}
	return d115State(attempt), nil
}

// D115CreateLocalStatus returns only state and an already durable receipt.
func D115CreateLocalStatus(profileRoot, commandID string) (D115LocalState, error) {
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return D115LocalState{}, err
	}
	attempt, err := ledger.findByCommandID(commandID)
	if err != nil {
		return D115LocalState{}, ErrD115LocalRefused
	}
	return d115State(attempt), nil
}

// CancelD115CreateLocal cancels only attempts with no possible exchange.
func CancelD115CreateLocal(profileRoot, commandID string) (D115LocalState, error) {
	unlock := lockD115Attempt(profileRoot, commandID)
	defer unlock()
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return D115LocalState{}, err
	}
	attempt, err := ledger.findByCommandID(commandID)
	if err != nil {
		return D115LocalState{}, ErrD115LocalRefused
	}
	attempt, err = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		if current.CommandID != commandID ||
			(current.Disposition != D115PendingSignature && current.Disposition != D115Prepared && current.Disposition != D115DefinitelyUnsent) {
			return ErrD115LocalRefused
		}
		for _, exchange := range current.Exchanges {
			if exchange.Disposition == string(D115PossiblySubmitted) || exchange.Disposition == string(D115OutcomeUnknown) {
				return ErrD115LocalRefused
			}
		}
		current.Disposition = D115Cancelled
		return nil
	})
	if err != nil {
		return D115LocalState{}, err
	}
	return d115State(attempt), nil
}

// SubmitPreparedD115CreateLocal performs one explicit submit of an already
// imported request. It does not sign, and startup never calls it.
func SubmitPreparedD115CreateLocal(ctx context.Context, profileRoot, checkoutBinding, commandID string) (d115.Receipt, error) {
	if ctx == nil {
		return d115.Receipt{}, ErrInvalidClientState
	}
	if !validD115CheckoutBinding(checkoutBinding) {
		return d115.Receipt{}, ErrD115LocalRefused
	}
	unlock := lockD115Attempt(profileRoot, commandID)
	defer unlock()
	config, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		return d115.Receipt{}, err
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return d115.Receipt{}, err
	}
	attempt, err := ledger.findByCommandID(commandID)
	if err != nil || attempt.CheckoutBinding != checkoutBinding {
		return d115.Receipt{}, ErrD115LocalRefused
	}
	if attempt.Disposition == D115ReceiptDurable || attempt.Disposition == D115Installed {
		return installD115Receipt(ledger, profileRoot, attempt)
	}
	if attempt.Disposition != D115Prepared && attempt.Disposition != D115DefinitelyUnsent && attempt.Disposition != D115OutcomeUnknown && attempt.Disposition != D115PossiblySubmitted {
		return d115.Receipt{}, ErrD115LocalRefused
	}
	if !hasPossiblySubmitted(attempt.Exchanges) {
		submit, decodeErr := d115.DecodeSubmit(attempt.RequestBytes)
		_, claims, grantErr := d115.DecodeGrant(submit.Grant)
		if decodeErr != nil || grantErr != nil || !time.Now().UTC().Before(parseD115Time(claims.ExpiresAt)) {
			_, _ = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error { current.Disposition = D115Refused; return nil })
			return d115.Receipt{}, ErrD115LocalRefused
		}
	}
	return sendD115Attempt(ctx, ledger, profileRoot, config, attempt)
}

// OwnerSignD115Request is used only by the offline tool's injected signer.
// Production launchers pass nil and receive a stable fail-closed error.
func OwnerSignD115Request(ctx context.Context, requestBytes []byte, signer D115OwnerRootSigner, bundleBytes []byte) ([]byte, error) {
	if signer == nil {
		return nil, errors.New("signing.owner-provider-unavailable")
	}
	if ctx == nil {
		return nil, ErrD115LocalRefused
	}
	request, err := d115.DecodeOwnerSigningRequest(requestBytes)
	if err != nil {
		return nil, ErrD115LocalRefused
	}
	grant, claims, err := d115.DecodeGrant(request.GrantArtifact)
	bundle, bundleErr := d115.DecodeOperatorTrustBundle(bundleBytes)
	expectedHash, hashErr := d115.RequestHash(request.RealmID, request.CommandID, request.GrantDigest)
	issued, issuedErr := time.Parse(time.RFC3339Nano, claims.IssuedAt)
	notBefore, beforeErr := time.Parse(time.RFC3339Nano, claims.NotBefore)
	expires, expiryErr := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	now := time.Now().UTC()
	if err != nil || claims.Audience != request.RealmID || claims.Scope != d115.CreateDomainScope || claims.OwnerKeyID != request.OwnerKeyID ||
		grant.SignerKeyID == request.OwnerKeyID || signer.OwnerRootKeyID() != request.OwnerKeyID || d115.GrantDigest(request.GrantArtifact) != request.GrantDigest ||
		bundleErr != nil || bundle.RealmID != request.RealmID || !bundle.Enabled || hashErr != nil || expectedHash != request.RequestHash ||
		issuedErr != nil || beforeErr != nil || expiryErr != nil || issued.After(now) || notBefore.Before(issued) || !notBefore.Before(expires) ||
		expires.Sub(issued) > 10*time.Minute || now.Before(notBefore) || !now.Before(expires) {
		return nil, ErrD115LocalRefused
	}
	trusted := false
	for _, key := range bundle.Keys {
		if key.KeyID == grant.SignerKeyID && key.State == "active" && key.Scope == d115.CreateDomainScope && key.Audience == request.RealmID &&
			!now.Before(parseD115Time(key.NotBefore)) && now.Before(parseD115Time(key.NotAfter)) && d115.VerifyGrantSignature(grant, ed25519.PublicKey(key.PublicKey)) {
			trusted = true
			break
		}
	}
	if !trusted {
		return nil, ErrD115LocalRefused
	}
	preimage, err := d115.OwnerProofPreimage(request.RealmID, request.CommandID, request.RequestHash, request.OwnerKeyID)
	if err != nil || !bytes.Equal(preimage, request.Preimage) {
		return nil, ErrD115LocalRefused
	}
	signature, err := signer.SignD115OwnerProof(ctx, bytes.Clone(request.GrantArtifact), claims, bytes.Clone(preimage))
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(claims.OwnerPublicKey), preimage, signature) {
		clear(signature)
		return nil, ErrD115LocalRefused
	}
	defer clear(signature)
	return d115.EncodeOwnerProofArtifact(d115.OwnerProofArtifact{
		Schema: d115.OwnerProofSchema, RealmID: request.RealmID, CommandID: request.CommandID,
		RequestHash: request.RequestHash, GrantDigest: request.GrantDigest, OwnerKeyID: request.OwnerKeyID, OwnerProof: signature,
	})
}

func d115State(attempt D115Attempt) D115LocalState {
	state := D115LocalState{AttemptID: attempt.CommandID, State: attempt.Disposition, SigningRequest: bytes.Clone(attempt.SigningRequest)}
	if len(attempt.ReceiptBytes) != 0 {
		state.Receipt = bytes.Clone(attempt.ReceiptBytes)
	}
	return state
}

func parseD115Time(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
