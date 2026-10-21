package wipdremote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type attachLifecycle struct {
	profileRoot  string
	resolver     *wipdseed.LocalRouteResolver
	ledger       *wipdseed.EnvironmentAttachLedger
	journal      *wipdjournal.Journal
	runCanonical func(context.Context, operation.Command, func(context.Context, wipd.CommandStartSnapshot, operation.Command) error) (wipd.CommandStartResult, error)
}

func configureAttachLifecycle(server *wipd.Server, profileRoot string, journals ...*wipdjournal.Journal) error {
	resolver, err := loadPrebindingResolver(profileRoot)
	if err != nil || resolver == nil {
		return err
	}
	ledger, err := wipdseed.OpenEnvironmentAttachLedger(profileRoot)
	if err != nil {
		return err
	}
	var journal *wipdjournal.Journal
	if len(journals) != 0 {
		journal = journals[0]
	}
	lifecycle := &attachLifecycle{profileRoot: profileRoot, resolver: resolver, ledger: ledger, journal: journal}
	if server.ConnectedCommandsConfigured() {
		lifecycle.runCanonical = server.RunConnectedCanonicalTerminal
	}
	return server.ConfigureAttachLifecycle(lifecycle)
}

func (handler *attachLifecycle) AttachOnce(ctx context.Context, peer *wipd.PeerProcess, grantArtifact string) (wipd.AttachLocalAttempt, error) {
	if ctx == nil || peer == nil || handler == nil || handler.resolver == nil || handler.ledger == nil {
		return wipd.AttachLocalAttempt{}, wipd.ErrUnsupportedCheckoutContext
	}
	unlock := handler.ledger.LockIntent()
	defer unlock()
	checkout, err := peer.DiscoverGitCheckout(ctx, "origin")
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	checkoutBinding, err := peer.D115CheckoutCorrelation(ctx)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	grantBytes, err := d115DecodeBase64URL(grantArtifact)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	grantDigest := digestAttachBytes(grantBytes)
	if prior, loadErr := handler.ledger.LoadByGrantDigest(grantDigest); loadErr == nil {
		claims, connection, replayErr := handler.verifyReplayGrant(grantArtifact, prior)
		if replayErr != nil || !bytes.Equal(prior.GrantBytes, grantBytes) || prior.D115ReceiptDigest != digestAttachBytes(prior.D115ReceiptBytes) {
			return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach intent failed exact replay validation")
		}
		installed, receiptErr := wipdseed.LoadInstalledD115ReceiptForAttach(handler.profileRoot, prior.D115AttemptID, checkoutBinding)
		if receiptErr != nil || !bytes.Equal(installed.Bytes, prior.D115ReceiptBytes) ||
			installed.Receipt.DomainID != claims.DomainID || installed.Receipt.RepoID != claims.RepoID {
			return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach intent no longer has its exact installed D115 receipt")
		}
		credential, certificate, credentialErr := wipdseed.LoadEnvironmentCredential(connection.ProfileRoot,
			connection.AuthorityProfile, connection.OwnerRoot, connection.CADElegation, time.Now().UTC())
		if credentialErr != nil {
			return wipd.AttachLocalAttempt{}, credentialErr
		}
		defer clear(credential.PrivateKeyPKCS8)
		defer clear(certificate.PrivateKey.(ed25519.PrivateKey))
		if credential.EnvironmentID != prior.EnvironmentID || claims.EnvironmentID != prior.EnvironmentID {
			return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach identity is no longer active")
		}
		proof, proofErr := operation.DecodeAttachCheckoutProof(prior.CheckoutProof)
		if proofErr != nil || checkout.NormalizedRemote != prior.Route || proof.NormalizedRepoKey != prior.Route {
			return wipd.AttachLocalAttempt{}, errors.New("wipdremote: live normalized remote differs from retained attach intent")
		}
		if len(prior.RequestBytes) == 0 {
			entry, journal, closeJournal, prepareErr := handler.prepareAttachCommand(ctx, connection, claims, credential,
				grantArtifact, proof, prior.CommandID)
			if prepareErr != nil {
				return wipd.AttachLocalAttempt{}, prepareErr
			}
			if closeJournal {
				defer func() { _ = journal.Close() }()
			}
			prior, prepareErr = handler.ledger.StorePreparedCommand(prior.EnvironmentID, prior.GrantDigest,
				entry.CanonicalBytes, entry.RequestHash)
			if prepareErr != nil {
				return wipd.AttachLocalAttempt{}, prepareErr
			}
			return handler.execute(ctx, connection, claims, prior, entry, checkout, journal)
		}
		return handler.resume(ctx, connection, claims, prior, checkout)
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return wipd.AttachLocalAttempt{}, loadErr
	}
	claims, connection, err := handler.verifyGrantForConfiguredConnection(grantArtifact)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	credential, certificate, err := wipdseed.LoadEnvironmentCredential(connection.ProfileRoot, connection.AuthorityProfile,
		connection.OwnerRoot, connection.CADElegation, time.Now().UTC())
	if err != nil {
		return wipd.AttachLocalAttempt{}, fmt.Errorf("load D54 Environment credential: %w", err)
	}
	defer clear(credential.PrivateKeyPKCS8)
	defer clear(certificate.PrivateKey.(ed25519.PrivateKey))
	if credential.EnvironmentID != claims.EnvironmentID || credential.SPKIDigest != claims.EnvironmentSPKI {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach grant does not identify this Environment")
	}
	if claims.GrantDigest != grantDigest {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach grant digest did not match canonical bytes")
	}
	route, err := handler.resolver.ResolveRoute(ctx, peer)
	if err != nil || route.DomainID != claims.DomainID || route.Epoch != claims.Epoch || route.ConnectionProfile == "" ||
		route.RepoID != nil && *route.RepoID != claims.RepoID || route.RepoID == nil &&
		(connection.AuthorityProfile.DomainID() != claims.DomainID || connection.AuthorityProfile.Epoch() != claims.Epoch) {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach route is unresolved or conflicts with the owner grant")
	}
	if route.ConnectionProfile != connectionName(handler.resolver.Connections, connection) || checkout.NormalizedRemote != claims.NormalizedKey {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: current D54 route differs from the owner grant")
	}
	d115Receipts, err := wipdseed.ListInstalledD115ReceiptsForAttach(handler.profileRoot, checkoutBinding)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	var selected *wipdseed.D115AttachReceipt
	for index := range d115Receipts {
		candidate := &d115Receipts[index]
		if candidate.Receipt.DomainID == claims.DomainID && candidate.Receipt.RepoID == claims.RepoID {
			if selected != nil {
				return wipd.AttachLocalAttempt{}, errors.New("wipdremote: multiple installed D115 receipts match this attach grant")
			}
			selected = candidate
		}
	}
	if selected == nil {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: no installed D115 receipt matches this attach grant")
	}
	if err = preflightAttachedCheckout(ctx, filepath.Join(handler.profileRoot, "local-registry.db"), claims.RepoID, checkout); err != nil {
		return wipd.AttachLocalAttempt{}, fmt.Errorf("preflight local attach projection: %w", err)
	}
	proof, proofBytes, err := makeAttachCheckoutProof(checkout, route, claims, credential)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	commandID, err := d115.NewULID(time.Now().UTC())
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	intent := wipdseed.EnvironmentAttachAttempt{
		EnvironmentID: credential.EnvironmentID, GrantDigest: grantDigest, GrantBytes: grantBytes,
		D115ReceiptBytes: selected.Bytes, D115ReceiptDigest: digestAttachBytes(selected.Bytes), D115AttemptID: selected.AttemptID,
		CommandID: commandID, AuthorityDomainID: claims.DomainID, AuthorityEpoch: claims.Epoch,
		EnvironmentSPKI: claims.EnvironmentSPKI, Route: claims.NormalizedKey, CheckoutProof: proofBytes,
		Disposition: wipdseed.EnvironmentAttachPrepared,
	}
	intent, err = handler.ledger.GetOrCreate(intent)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	entry, journal, closeJournal, err := handler.prepareAttachCommand(ctx, connection, claims, credential, grantArtifact, proof, intent.CommandID)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	if closeJournal {
		defer func() { _ = journal.Close() }()
	}
	intent, err = handler.ledger.StorePreparedCommand(intent.EnvironmentID, intent.GrantDigest, entry.CanonicalBytes, entry.RequestHash)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	return handler.execute(ctx, connection, claims, intent, entry, checkout, journal)
}

func (handler *attachLifecycle) AttachStatus(ctx context.Context, peer *wipd.PeerProcess, attemptID string) (wipd.AttachLocalAttempt, error) {
	if ctx == nil || peer == nil || handler == nil || handler.ledger == nil || !d115.ValidULID(attemptID) {
		return wipd.AttachLocalAttempt{}, wipd.ErrUnsupportedCheckoutContext
	}
	attempt, err := handler.ledger.LoadByCommandID(attemptID)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	checkout, err := peer.DiscoverGitCheckout(ctx, "origin")
	proof, proofErr := operation.DecodeAttachCheckoutProof(attempt.CheckoutProof)
	if err != nil || proofErr != nil || !checkoutMatchesAttachProof(checkout, proof) {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach status checkout does not match retained attempt")
	}
	if len(attempt.ResultBytes) == 0 {
		return wipd.AttachLocalAttempt{AttemptID: attempt.CommandID, State: attempt.Disposition}, nil
	}
	var terminal wipdwire.SignedCommandTerminal
	if wipdwire.DecodeCanonical(attempt.ResultBytes, &terminal, "schema", "command_id", "request_hash", "receipt", "signed_receipt") != nil {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: invalid retained attach terminal")
	}
	return wipd.AttachLocalAttempt{
		AttemptID: attempt.CommandID, State: attempt.Disposition,
		Receipt: bytes.Clone(terminal.Receipt), SignedReceipt: bytes.Clone(terminal.SignedReceipt),
	}, nil
}

func (handler *attachLifecycle) verifyGrantForConfiguredConnection(encoded string) (authoritystore.EnvironmentAttachGrantClaims, wipdseed.PrebindingConnection, error) {
	var selected wipdseed.PrebindingConnection
	var claims authoritystore.EnvironmentAttachGrantClaims
	found := false
	for _, connection := range handler.resolver.Connections {
		verified, err := authoritystore.VerifyEnvironmentAttachGrantArtifact(encoded, connection.OwnerRoot,
			connection.AuthorityProfile.DomainID(), connection.AuthorityProfile.Epoch(), connection.AuthorityProfile.OwnerRootSPKI(), time.Now().UTC())
		if err != nil || verified.AudienceSPKI != connection.AuthorityProfile.AuthoritySPKI() {
			continue
		}
		if found {
			return claims, selected, errors.New("wipdremote: attach grant ambiguously matches configured D54 authorities")
		}
		claims, selected, found = verified, connection, true
	}
	if !found {
		return claims, selected, errors.New("wipdremote: attach grant does not verify under a configured D54 owner root and audience")
	}
	return claims, selected, nil
}

func (handler *attachLifecycle) verifyReplayGrant(encoded string, attempt wipdseed.EnvironmentAttachAttempt) (authoritystore.EnvironmentAttachGrantClaims, wipdseed.PrebindingConnection, error) {
	var selected wipdseed.PrebindingConnection
	var claims authoritystore.EnvironmentAttachGrantClaims
	found := false
	for _, connection := range handler.resolver.Connections {
		if connection.AuthorityProfile.DomainID() != attempt.AuthorityDomainID || connection.AuthorityProfile.Epoch() != attempt.AuthorityEpoch {
			continue
		}
		verified, err := authoritystore.VerifyEnvironmentAttachGrantArtifactForReplay(encoded, connection.OwnerRoot,
			attempt.AuthorityDomainID, attempt.AuthorityEpoch, connection.AuthorityProfile.OwnerRootSPKI())
		if err != nil || verified.GrantDigest != attempt.GrantDigest || verified.EnvironmentID != attempt.EnvironmentID ||
			verified.EnvironmentSPKI != attempt.EnvironmentSPKI {
			continue
		}
		if found {
			return claims, selected, errors.New("wipdremote: retained attach intent ambiguously matches current authority profiles")
		}
		claims, selected, found = verified, connection, true
	}
	if !found {
		return claims, selected, errors.New("wipdremote: retained attach grant is not bound to a current authority profile")
	}
	return claims, selected, nil
}

func (handler *attachLifecycle) prepareAttachCommand(ctx context.Context, connection wipdseed.PrebindingConnection,
	claims authoritystore.EnvironmentAttachGrantClaims, credential wipdseed.EnvironmentCredential, grant string,
	proof operation.AttachCheckoutProof, commandID string,
) (wipdjournal.Entry, *wipdjournal.Journal, bool, error) {
	var empty wipdjournal.Entry
	var err error
	stateDir := filepath.Join(handler.profileRoot, "environment-attach-state", claims.DomainID, claims.RepoID)
	seed, err := wipdseed.SeedRepoFromEnvironmentCredential(ctx, connection.AuthorityProfile,
		connection.AuthorityRoots, connection.OwnerRoot, connection.CADElegation, claims.RepoID,
		connection.ProfileRoot, stateDir)
	if err != nil {
		return empty, nil, false, fmt.Errorf("seed attach Environment state: %w", err)
	}
	clear(seed.PrivateKeyPKCS8)
	journal := handler.journal
	owned := false
	if journal == nil {
		journal, err = openStep11Journal(handler.profileRoot, connection.AuthorityProfile.M5LabRepoID(), claims.DomainID, claims.Epoch,
			credential.EnvironmentID, credential.OwnerKeyID)
		if err != nil {
			return empty, nil, false, err
		}
		owned = true
	}
	client, err := openAttachExchangeClient(ctx, connection, stateDir, claims.RepoID)
	if err != nil {
		if owned {
			_ = journal.Close()
		}
		return empty, nil, false, err
	}
	_, err = reconcilePrebindingJournal(ctx, connection.AuthorityProfile, client, journal)
	closeErr := client.Close()
	if err = errors.Join(err, closeErr); err != nil {
		if owned {
			_ = journal.Close()
		}
		return empty, nil, false, fmt.Errorf("reconcile prebinding attach journal: %w", err)
	}
	proofBytes, err := wipdwire.EncodeCanonical(proof)
	if err != nil {
		if owned {
			_ = journal.Close()
		}
		return empty, nil, false, err
	}
	entry, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: commandID, Request: operation.Request{
		Operation: operation.EnvironmentAttachOnceV1.Metadata().Operation, Actor: "human",
		Context: operation.Context{Repo: claims.RepoID},
		Input:   operation.EnvironmentAttachOnceInput{GrantArtifact: grant, CheckoutProof: operation.CanonicalBytes(proofBytes)},
	}})
	if err != nil {
		if owned {
			_ = journal.Close()
		}
		return empty, nil, false, err
	}
	return entry, journal, owned, nil
}

func openStep11Journal(profileRoot, repoID, domainID string, epoch uint64, environmentID, ownerRootSPKI string) (*wipdjournal.Journal, error) {
	if repoID == "" {
		return nil, errors.New("wipdremote: Step 11 Environment journal requires its Repo anchor")
	}
	identity := wipdjournal.Identity{
		// The configured M5 Repo anchors the identity while each Step 11
		// command retains its actual Repo in the canonical request context.
		RepoID: repoID, DomainID: domainID, AuthorityEpoch: epoch,
		EnvironmentID: environmentID, OwnerRootSPKI: ownerRootSPKI,
	}
	root := filepath.Join(profileRoot, "environment-journal")
	return wipdjournal.Open(root, identity)
}

func (handler *attachLifecycle) resume(ctx context.Context, connection wipdseed.PrebindingConnection,
	claims authoritystore.EnvironmentAttachGrantClaims, attempt wipdseed.EnvironmentAttachAttempt, checkout wipd.GitCheckout,
) (wipd.AttachLocalAttempt, error) {
	command, err := operation.DecodeCanonicalCommand(attempt.RequestBytes)
	if err != nil || command.ID != attempt.CommandID || operation.VerifyRequestHash(command, attempt.RequestHash) != nil {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach command identity is invalid")
	}
	input, ok := command.Request.Input.(operation.EnvironmentAttachOnceInput)
	if !ok || input.GrantArtifact == "" {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach command input is invalid")
	}
	grantBytes, grantErr := d115DecodeBase64URL(input.GrantArtifact)
	if grantErr != nil || !bytes.Equal(attempt.GrantBytes, grantBytes) {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach command grant conflicts with local intent")
	}
	proof, err := operation.DecodeAttachCheckoutProof([]byte(input.CheckoutProof))
	if err != nil || !checkoutMatchesAttachProof(checkout, proof) {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: live checkout differs from retained attach intent")
	}
	journal := handler.journal
	owned := false
	if journal == nil {
		journal, err = openStep11Journal(handler.profileRoot, connection.AuthorityProfile.M5LabRepoID(), claims.DomainID, claims.Epoch,
			attempt.EnvironmentID, connection.AuthorityProfile.OwnerRootSPKI())
		if err != nil {
			return wipd.AttachLocalAttempt{}, err
		}
		owned = true
	}
	if owned {
		defer func() { _ = journal.Close() }()
	}
	entry, err := journal.Get(attempt.CommandID)
	if err != nil || !bytes.Equal(entry.CanonicalBytes, attempt.RequestBytes) || entry.RequestHash != attempt.RequestHash ||
		entry.Command.ID != command.ID || entry.State != wipdjournal.StateAttemptPrepared || entry.Delivery != operation.DeliveryAuthority {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: retained attach attempt does not match its exact shared-journal entry")
	}
	return handler.execute(ctx, connection, claims, attempt, entry, checkout, journal)
}

func (handler *attachLifecycle) execute(ctx context.Context, connection wipdseed.PrebindingConnection,
	claims authoritystore.EnvironmentAttachGrantClaims, attempt wipdseed.EnvironmentAttachAttempt,
	entry wipdjournal.Entry, checkout wipd.GitCheckout, journal *wipdjournal.Journal,
) (wipd.AttachLocalAttempt, error) {
	current, err := handler.ledger.Load(attempt.EnvironmentID, attempt.GrantDigest)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	client, err := openAttachExchangeClient(ctx, connection,
		filepath.Join(handler.profileRoot, "environment-attach-state", claims.DomainID, claims.RepoID), claims.RepoID)
	if err != nil {
		return wipd.AttachLocalAttempt{}, fmt.Errorf("open attach M2 exchange: %w", err)
	}
	defer func() { _ = client.Close() }()
	var receipt, signed []byte
	if len(current.ResultBytes) != 0 {
		receipt, signed, err = decodeAttachTerminal(current.ResultBytes, entry.Command.ID, entry.RequestHash)
		if err != nil {
			return wipd.AttachLocalAttempt{}, err
		}
	} else {
		if current.Disposition == wipdseed.EnvironmentAttachPrepared || current.Disposition == wipdseed.EnvironmentAttachUnavailable {
			if err = handler.appendAttachMarker(attempt, "send-started", wipdseed.EnvironmentAttachSubmitted); err != nil {
				return wipd.AttachLocalAttempt{}, err
			}
		}
		submit := func(submitContext context.Context, _ wipd.CommandStartSnapshot, submitted operation.Command) error {
			canonical, encodeErr := submitted.CanonicalBytes()
			if encodeErr != nil || !bytes.Equal(canonical, entry.CanonicalBytes) {
				return wipdjournal.ErrCommandIDConflict
			}
			exactSubmission, encodeErr := entry.SubmissionPayload()
			if encodeErr != nil {
				return encodeErr
			}
			frames, exchangeErr := client.Exchange(submitContext, "command.submit", exactSubmission)
			if exchangeErr != nil {
				return fmt.Errorf("%w: attach response is uncertain: %v", wipd.ErrOutcomeUnknown, exchangeErr)
			}
			receipt, signed, err = terminalFromAttachSubmit(submitContext, client, claims.DomainID, entry, frames)
			return err
		}
		if handler.runCanonical != nil {
			if _, err = handler.runCanonical(ctx, entry.Command, submit); err != nil {
				if markerErr := handler.appendAttachMarker(attempt, "outcome-unknown", wipdseed.EnvironmentAttachOutcomeUnknown); markerErr != nil {
					return wipd.AttachLocalAttempt{}, errors.Join(err, markerErr)
				}
				if errors.Is(err, wipd.ErrOutcomeUnknown) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return wipd.AttachLocalAttempt{AttemptID: attempt.CommandID, State: wipdseed.EnvironmentAttachOutcomeUnknown}, nil
				}
				return wipd.AttachLocalAttempt{}, err
			}
			if len(receipt) == 0 || len(signed) == 0 {
				submission, encodeErr := entry.SubmissionPayload()
				if encodeErr != nil {
					return wipd.AttachLocalAttempt{}, encodeErr
				}
				frames, exchangeErr := client.Exchange(ctx, "command.submit", submission)
				if exchangeErr != nil {
					return wipd.AttachLocalAttempt{}, fmt.Errorf("recover signed attach terminal: %w", exchangeErr)
				}
				receipt, signed, err = terminalFromAttachSubmit(ctx, client, claims.DomainID, entry, frames)
				if err != nil {
					return wipd.AttachLocalAttempt{}, err
				}
			}
		} else if err = submit(ctx, wipd.CommandStartSnapshot{}, entry.Command); err != nil {
			if markerErr := handler.appendAttachMarker(attempt, "outcome-unknown", wipdseed.EnvironmentAttachOutcomeUnknown); markerErr != nil {
				return wipd.AttachLocalAttempt{}, errors.Join(err, markerErr)
			}
			return wipd.AttachLocalAttempt{AttemptID: attempt.CommandID, State: wipdseed.EnvironmentAttachOutcomeUnknown}, nil
		}
	}
	identity := wipdjournal.Identity{
		RepoID: claims.RepoID, DomainID: claims.DomainID, AuthorityEpoch: claims.Epoch,
		EnvironmentID: attempt.EnvironmentID, OwnerRootSPKI: connection.AuthorityProfile.OwnerRootSPKI(),
	}
	trust := wipdjournal.ClaimGrantTrust{
		OwnerRootPublicKey: connection.OwnerRoot,
		OwnerRootSPKI:      identity.OwnerRootSPKI, VerifiedAt: time.Now().UTC(),
	}
	verified, err := wipdjournal.VerifyPortableTerminalReceipt(identity, trust, connection.ArtifactKeyCertificate, receipt, signed)
	if err != nil || verified.CommandID != entry.Command.ID || verified.RequestHash != entry.RequestHash ||
		verified.Operation != operation.EnvironmentAttachOnceV1.Metadata().Operation.Name {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach terminal receipt failed contract verification")
	}
	if handler.runCanonical == nil {
		if err = installPrebindingAuthorityOutcome(ctx, connection.AuthorityProfile, client, journal, entry, verified.ResultCode, receipt); err != nil {
			return wipd.AttachLocalAttempt{}, fmt.Errorf("install prebinding attach terminal in shared journal: %w", err)
		}
	}
	if verified.ResultCode != operation.ResultSucceeded {
		if verified.Output != nil || verified.ProblemCode == "" || verified.EventCount != 0 || verified.FirstEvent != "" || verified.LastEvent != "" {
			return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach refusal receipt contains an effect or invalid problem")
		}
		terminalBytes, encodeErr := wipdwire.EncodeCanonical(wipdwire.SignedCommandTerminal{
			Schema: "wipd.signed-command-terminal/1", CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
			Receipt: receipt, SignedReceipt: signed,
		})
		if encodeErr != nil {
			return wipd.AttachLocalAttempt{}, encodeErr
		}
		if _, err = handler.ledger.PersistResult(attempt.EnvironmentID, attempt.GrantDigest, terminalBytes, wipdseed.EnvironmentAttachRefused); err != nil {
			return wipd.AttachLocalAttempt{}, err
		}
		return wipd.AttachLocalAttempt{
			AttemptID: attempt.CommandID, State: wipdseed.EnvironmentAttachRefused,
			Receipt: receipt, SignedReceipt: signed,
		}, nil
	}
	if verified.EventCount != 1 || verified.FirstEvent == "" || verified.FirstEvent != verified.LastEvent {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: successful attach receipt has an invalid event range")
	}
	outputFields, outputErr := wipdwire.DecodeCanonicalMap(verified.Output,
		"repo_id", "domain_id", "authority_epoch", "environment_id", "clone_id", "worktree_id")
	repoID, repoOK := outputFields["repo_id"].(string)
	domainID, domainOK := outputFields["domain_id"].(string)
	epoch, epochOK := outputFields["authority_epoch"].(uint64)
	environmentID, environmentOK := outputFields["environment_id"].(string)
	cloneID, cloneOK := outputFields["clone_id"].(string)
	worktreeID, worktreeOK := outputFields["worktree_id"].(string)
	if outputErr != nil || !repoOK || !domainOK || !epochOK || !environmentOK || !cloneOK || !worktreeOK ||
		repoID != claims.RepoID || domainID != claims.DomainID || epoch != claims.Epoch || environmentID != attempt.EnvironmentID {
		return wipd.AttachLocalAttempt{}, errors.New("wipdremote: attach receipt output conflicts with pinned intent")
	}
	output := operation.EnvironmentAttachOnceOutput{
		RepoID: repoID, DomainID: domainID, Epoch: epoch,
		EnvironmentID: environmentID, CloneID: cloneID, WorktreeID: worktreeID,
	}
	terminalBytes, err := wipdwire.EncodeCanonical(wipdwire.SignedCommandTerminal{
		Schema: "wipd.signed-command-terminal/1", CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
		Receipt: receipt, SignedReceipt: signed,
	})
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	if _, err = handler.ledger.PersistResult(attempt.EnvironmentID, attempt.GrantDigest, terminalBytes, wipdseed.EnvironmentAttachTerminal); err != nil {
		// A retained result may already have advanced to install-pending or
		// installed; exact bytes are immutable and the subsequent transition
		// logic below handles those recovery states.
		if !errors.Is(err, wipdseed.ErrInvalidClientState) {
			return wipd.AttachLocalAttempt{}, err
		}
	}
	current, err = handler.ledger.Load(attempt.EnvironmentID, attempt.GrantDigest)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	if current.Disposition == wipdseed.EnvironmentAttachTerminal {
		if err = handler.appendAttachMarker(attempt, "local-install-pending", wipdseed.EnvironmentAttachLocalInstallPending); err != nil {
			return wipd.AttachLocalAttempt{}, err
		}
	}
	registryPath := filepath.Join(handler.profileRoot, "local-registry.db")
	err = installAttachedCheckout(ctx, registryPath, output.RepoID, output.CloneID, output.WorktreeID, checkout)
	if err != nil {
		return wipd.AttachLocalAttempt{
			AttemptID: attempt.CommandID, State: wipdseed.EnvironmentAttachLocalInstallPending,
			Receipt: receipt, SignedReceipt: signed,
		}, nil
	}
	current, err = handler.ledger.Load(attempt.EnvironmentID, attempt.GrantDigest)
	if err != nil {
		return wipd.AttachLocalAttempt{}, err
	}
	if current.Disposition == wipdseed.EnvironmentAttachLocalInstallPending {
		if err = handler.appendAttachMarker(attempt, "local-installed", wipdseed.EnvironmentAttachInstalled); err != nil {
			return wipd.AttachLocalAttempt{}, err
		}
	}
	return wipd.AttachLocalAttempt{
		AttemptID: attempt.CommandID, State: wipdseed.EnvironmentAttachInstalled,
		Receipt: receipt, SignedReceipt: signed,
	}, nil
}

func (handler *attachLifecycle) appendAttachMarker(attempt wipdseed.EnvironmentAttachAttempt, kind, disposition string) error {
	current, err := handler.ledger.Load(attempt.EnvironmentID, attempt.GrantDigest)
	if err != nil {
		return err
	}
	_, err = handler.ledger.AppendMarker(attempt.EnvironmentID, attempt.GrantDigest,
		wipdseed.EnvironmentAttachMarker{Sequence: uint64(len(current.Markers) + 1), Kind: kind}, disposition)
	return err
}

func makeAttachCheckoutProof(checkout wipd.GitCheckout, route operation.RouteResolveOutput,
	claims authoritystore.EnvironmentAttachGrantClaims, credential wipdseed.EnvironmentCredential,
) (operation.AttachCheckoutProof, []byte, error) {
	var proof operation.AttachCheckoutProof
	if !filepath.IsAbs(checkout.CommonDir) || !filepath.IsAbs(checkout.GitDir) || checkout.NormalizedRemote == "" || route.DomainID == "" || route.Epoch == 0 {
		return proof, nil, errors.New("wipdremote: incomplete attach checkout evidence")
	}
	common := filepath.Clean(checkout.CommonDir)
	gitDir := filepath.Clean(checkout.GitDir)
	commonDigest := attachPathDigest("wipd/attach-common-dir/v1", common)
	gitDigest := attachPathDigest("wipd/attach-git-dir/v1", gitDir)
	peerBytes, err := wipdwire.EncodeCanonical(map[string]any{
		"normalized_repo_key": checkout.NormalizedRemote,
		"common_dir_digest":   commonDigest, "git_dir_digest": gitDigest, "worktree_name": checkout.WorktreeName,
	})
	if err != nil {
		return proof, nil, err
	}
	routeBytes, err := wipdwire.EncodeCanonical(map[string]any{
		"domain_id": route.DomainID, "authority_epoch": route.Epoch,
		"repo_id": claims.RepoID, "normalized_repo_key": checkout.NormalizedRemote,
	})
	if err != nil {
		return proof, nil, err
	}
	proof = operation.AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: checkout.NormalizedRemote,
		CommonDirDigest: commonDigest, GitDirDigest: gitDigest, WorktreeName: checkout.WorktreeName,
		PeerSnapshotDigest:  attachBytesDigest([]byte("wipd/attach-peer-snapshot/v1\x00"), peerBytes),
		RouteSnapshotDigest: attachBytesDigest([]byte("wipd/attach-route-snapshot/v1\x00"), routeBytes),
		EnvironmentID:       credential.EnvironmentID, EnvironmentSPKIDigest: credential.SPKIDigest,
	}
	encoded, err := wipdwire.EncodeCanonical(proof)
	return proof, encoded, err
}

func attachPathDigest(prefix, path string) string {
	return attachBytesDigest([]byte(prefix+"\x00"), []byte(path))
}

func attachBytesDigest(prefix, data []byte) string {
	hash := sha256.Sum256(append(bytes.Clone(prefix), data...))
	return "sha256:" + hex.EncodeToString(hash[:])
}
func digestAttachBytes(data []byte) string { return attachBytesDigest(nil, data) }
func connectionName(connections map[string]wipdseed.PrebindingConnection, selected wipdseed.PrebindingConnection) string {
	for name, connection := range connections {
		if connection.AuthorityProfile.DomainID() == selected.AuthorityProfile.DomainID() &&
			connection.AuthorityProfile.AuthoritySPKI() == selected.AuthorityProfile.AuthoritySPKI() {
			return name
		}
	}
	return ""
}

func checkoutMatchesAttachProof(checkout wipd.GitCheckout, proof operation.AttachCheckoutProof) bool {
	return checkout.WorktreeName == proof.WorktreeName &&
		attachPathDigest("wipd/attach-common-dir/v1", filepath.Clean(checkout.CommonDir)) == proof.CommonDirDigest &&
		attachPathDigest("wipd/attach-git-dir/v1", filepath.Clean(checkout.GitDir)) == proof.GitDirDigest
}

func d115DecodeBase64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}
