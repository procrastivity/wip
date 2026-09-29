package wipdjournal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/wipdwire"
)

var errInvalidClaimGrant = errors.New("wipdjournal: invalid verified claim grant")

// ClaimGrantTrust identifies the owner key pinned by the installed client
// profile. VerifiedAt is the trusted time used for certificate validity.
type ClaimGrantTrust struct {
	OwnerRootPublicKey ed25519.PublicKey
	OwnerRootSPKI      string
	VerifiedAt         time.Time
}

// ClaimGrantEvidence is the signed start/end product and complete verified
// transfer returned for one successful claim acquisition.
type ClaimGrantEvidence struct {
	ArtifactKeyCertificate []byte
	Wrapper                []byte
	Start                  []byte
	End                    []byte
	Transfer               VerifiedTransfer
}

// VerifiedClaimGrant can only be created after verifying the owner-certified
// authority key, signed grant, acquisition receipt/range, transfer and end.
// Its fields are private so hydration cannot be attached to caller IDs.
type VerifiedClaimGrant struct {
	grantID, acquireCommandID, acquireRequestHash string
	claimID, matterID, batchID, dispatchID        string
	worktreeID                                    string
	ownerRootSPKI, verifiedAt                     string
	claimEpoch                                    uint64
	start, end                                    wipdwire.PrefixAnchor
	manifest                                      wipdwire.BlobManifest
	ownerRootPublicKey, artifactKeyCertificate    []byte
	wrapper, startBytes, endBytes, receipt        []byte
	transfer                                      VerifiedTransfer
	verified                                      bool
}

// TerminalReceipt returns the canonical authority receipt authenticated by
// the verified grant's signed start record.
func (grant VerifiedClaimGrant) TerminalReceipt() []byte {
	return bytes.Clone(grant.receipt)
}

// WorktreeID returns the Worktree bound by the verified acquisition range.
func (grant VerifiedClaimGrant) WorktreeID() string { return grant.worktreeID }

// VerifyClaimGrant verifies the signed authority grant and binds it to the
// authenticated client's pinned owner root and exact complete transfer.
func VerifyClaimGrant(identity Identity, trust ClaimGrantTrust, evidence ClaimGrantEvidence) (VerifiedClaimGrant, error) {
	var result VerifiedClaimGrant
	if !validIdentity(identity) || len(trust.OwnerRootPublicKey) != ed25519.PublicKeySize ||
		!transferHash.MatchString(trust.OwnerRootSPKI) || identity.OwnerRootSPKI != trust.OwnerRootSPKI || trust.VerifiedAt.IsZero() ||
		len(evidence.ArtifactKeyCertificate) == 0 || len(evidence.ArtifactKeyCertificate) > 1<<20 ||
		len(evidence.Wrapper) == 0 || len(evidence.Wrapper) > 1<<20 || len(evidence.Start) == 0 || len(evidence.Start) > wipdwire.FrameLimit ||
		len(evidence.End) == 0 || len(evidence.End) > wipdwire.FrameLimit ||
		!evidence.Transfer.Valid() || evidence.Transfer.domainID != identity.DomainID || evidence.Transfer.epoch != identity.AuthorityEpoch {
		return result, errInvalidClaimGrant
	}
	ownerDER, err := x509.MarshalPKIXPublicKey(trust.OwnerRootPublicKey)
	if err != nil || digestBytes(ownerDER) != trust.OwnerRootSPKI {
		return result, errInvalidClaimGrant
	}
	artifactKey, artifactKeyID, generation, notBefore, notAfter, err := verifyClaimArtifactCertificate(identity, trust, trust.OwnerRootSPKI, evidence.ArtifactKeyCertificate)
	if err != nil {
		return result, errInvalidClaimGrant
	}
	wrapper, err := decodeClosed(evidence.Wrapper,
		"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence",
		"previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature")
	if err != nil || wrapper["schema"] != "wipd.signed-artifact/1" || wrapper["kind"] != "claim-grant" ||
		wrapper["domain_id"] != identity.DomainID || wrapper["authority_epoch"] != identity.AuthorityEpoch || wrapper["signer_role"] != "authority" ||
		wrapper["signer_key_id"] != artifactKeyID || wrapper["payload_schema"] != "wipd.claim-grant-start/1" ||
		wrapper["payload_digest"] != digestBytes(evidence.Start) || !bytes.Equal(asBytes(wrapper["payload"]), evidence.Start) ||
		!validOptionalDigest(wrapper["previous_artifact_digest"]) {
		return result, errInvalidClaimGrant
	}
	artifactGeneration, ok := wrapper["key_generation"].(uint64)
	sequence, sequenceOK := wrapper["artifact_sequence"].(uint64)
	issuedAt, timeOK := canonicalArtifactTime(wrapper["issued_at"])
	if !ok || artifactGeneration != generation || !sequenceOK || sequence == 0 || !timeOK ||
		issuedAt.Before(notBefore) || !issuedAt.Before(notAfter) || issuedAt.After(trust.VerifiedAt) ||
		!verifySignedArtifact(wrapper, artifactKey) {
		return result, errInvalidClaimGrant
	}

	start, err := decodeClosed(evidence.Start,
		"schema", "grant_id", "acquire_command_id", "acquire_request_hash", "domain_id", "authority_epoch", "owner_environment_id",
		"claim", "matter_id", "batch_id", "dispatch_id", "receipt", "prefix", "blob_manifest_digest")
	if err != nil || start["schema"] != "wipd.claim-grant-start/1" || start["domain_id"] != identity.DomainID ||
		start["authority_epoch"] != identity.AuthorityEpoch || start["owner_environment_id"] != identity.EnvironmentID ||
		start["blob_manifest_digest"] != evidence.Transfer.manifest.Digest {
		return result, errInvalidClaimGrant
	}
	grantID, grantOK := start["grant_id"].(string)
	acquireID, acquireOK := start["acquire_command_id"].(string)
	requestHash, hashOK := start["acquire_request_hash"].(string)
	matterID, matterOK := start["matter_id"].(string)
	batchID, batchOK := start["batch_id"].(string)
	dispatchID, dispatchOK := start["dispatch_id"].(string)
	if !grantOK || !transferULID.MatchString(grantID) || !acquireOK || !transferULID.MatchString(acquireID) ||
		!hashOK || !transferHash.MatchString(requestHash) || !matterOK || !transferULID.MatchString(matterID) ||
		!batchOK || !transferULID.MatchString(batchID) || !dispatchOK || !transferULID.MatchString(dispatchID) {
		return result, errInvalidClaimGrant
	}
	claim, ok := start["claim"].(map[string]any)
	claimID, claimIDOK := claim["id"].(string)
	claimEpoch, claimEpochOK := claim["epoch"].(uint64)
	if !ok || !wipdwire.ExactMapKeys(claim, "id", "epoch") || !claimIDOK || !transferULID.MatchString(claimID) || !claimEpochOK || claimEpoch == 0 {
		return result, errInvalidClaimGrant
	}
	prefix, ok := start["prefix"].(map[string]any)
	startAnchor, startOK := decodeAnchor(prefix["start"])
	endAnchor, endOK := decodeAnchor(prefix["end"])
	if !ok || !wipdwire.ExactMapKeys(prefix, "start", "end") || !startOK || !endOK ||
		!sameTransferAnchor(startAnchor, evidence.Transfer.start) || !sameTransferAnchor(endAnchor, evidence.Transfer.end) {
		return result, errInvalidClaimGrant
	}
	if err = verifyClaimGrantEnd(evidence.End, grantID, evidence.Transfer.end, evidence.Transfer.manifest.Digest); err != nil {
		return result, errInvalidClaimGrant
	}
	receiptMap, ok := start["receipt"].(map[string]any)
	if !ok {
		return result, errInvalidClaimGrant
	}
	receipt, err := wipdwire.EncodeCanonical(receiptMap)
	if err != nil || verifyAcquireReceipt(identity, acquireID, requestHash, claimID, claimEpoch, matterID, batchID, dispatchID,
		receiptMap, evidence.Transfer) != nil {
		return result, errInvalidClaimGrant
	}
	worktreeID := claimGrantWorktree(receiptMap, evidence.Transfer)
	if !transferULID.MatchString(worktreeID) {
		return result, errInvalidClaimGrant
	}
	result = VerifiedClaimGrant{
		grantID: grantID, acquireCommandID: acquireID, acquireRequestHash: requestHash,
		claimID: claimID, claimEpoch: claimEpoch, matterID: matterID, batchID: batchID, dispatchID: dispatchID, worktreeID: worktreeID,
		ownerRootSPKI: trust.OwnerRootSPKI, verifiedAt: trust.VerifiedAt.UTC().Format(time.RFC3339Nano),
		start: cloneTransferAnchor(startAnchor), end: cloneTransferAnchor(endAnchor), manifest: cloneTransferManifest(evidence.Transfer.manifest),
		ownerRootPublicKey: bytes.Clone(trust.OwnerRootPublicKey), artifactKeyCertificate: bytes.Clone(evidence.ArtifactKeyCertificate),
		wrapper: bytes.Clone(evidence.Wrapper), startBytes: bytes.Clone(evidence.Start), endBytes: bytes.Clone(evidence.End),
		receipt: receipt, transfer: evidence.Transfer, verified: true,
	}
	return result, nil
}

func verifyClaimArtifactCertificate(identity Identity, trust ClaimGrantTrust, ownerKeyID string, wrapperBytes []byte) (ed25519.PublicKey, string, uint64, time.Time, time.Time, error) {
	var empty ed25519.PublicKey
	wrapper, err := decodeClosed(wrapperBytes,
		"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence",
		"previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature")
	if err != nil || len(wrapperBytes) > 1<<20 || wrapper["schema"] != "wipd.signed-artifact/1" || wrapper["kind"] != "authority-artifact-key" ||
		wrapper["domain_id"] != identity.DomainID || wrapper["authority_epoch"] != identity.AuthorityEpoch || wrapper["signer_role"] != "owner" ||
		wrapper["signer_key_id"] != ownerKeyID || wrapper["key_generation"] != nil || wrapper["artifact_sequence"] != nil ||
		wrapper["previous_artifact_digest"] != nil || wrapper["payload_schema"] != "wipd.authority-artifact-key/1" || !validCanonicalTime(wrapper["issued_at"]) {
		return empty, "", 0, time.Time{}, time.Time{}, errInvalidClaimGrant
	}
	payloadBytes := asBytes(wrapper["payload"])
	if wrapper["payload_digest"] != digestBytes(payloadBytes) || !verifySignedArtifact(wrapper, trust.OwnerRootPublicKey) {
		return empty, "", 0, time.Time{}, time.Time{}, errInvalidClaimGrant
	}
	payload, err := decodeClosed(payloadBytes,
		"schema", "domain_id", "authority_epoch", "key_generation", "key_id", "ed25519_public_key", "not_before", "not_after")
	if err != nil || payload["schema"] != "wipd.authority-artifact-key/1" || payload["domain_id"] != identity.DomainID ||
		payload["authority_epoch"] != identity.AuthorityEpoch {
		return empty, "", 0, time.Time{}, time.Time{}, errInvalidClaimGrant
	}
	public := asBytes(payload["ed25519_public_key"])
	generation, ok := payload["key_generation"].(uint64)
	keyID, keyOK := payload["key_id"].(string)
	if !ok || generation == 0 || !keyOK || !transferHash.MatchString(keyID) || len(public) != ed25519.PublicKeySize {
		return empty, "", 0, time.Time{}, time.Time{}, errInvalidClaimGrant
	}
	publicDER, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(public))
	if err != nil || digestBytes(publicDER) != keyID {
		return empty, "", 0, time.Time{}, time.Time{}, errInvalidClaimGrant
	}
	notBefore, beforeOK := canonicalArtifactTime(payload["not_before"])
	notAfter, afterOK := canonicalArtifactTime(payload["not_after"])
	issued, issuedOK := canonicalArtifactTime(wrapper["issued_at"])
	if !beforeOK || !afterOK || !issuedOK || !notBefore.Before(notAfter) || issued.After(trust.VerifiedAt) || keyID == ownerKeyID ||
		trust.VerifiedAt.Before(notBefore) || !trust.VerifiedAt.Before(notAfter) || issued.Before(notBefore) || !issued.Before(notAfter) {
		return empty, "", 0, time.Time{}, time.Time{}, errInvalidClaimGrant
	}
	return ed25519.PublicKey(bytes.Clone(public)), keyID, generation, notBefore, notAfter, nil
}

func verifyAcquireReceipt(identity Identity, commandID, requestHash, claimID string, claimEpoch uint64, matterID, batchID, dispatchID string,
	receipt map[string]any, transfer VerifiedTransfer,
) error {
	if !wipdwire.ExactMapKeys(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events") ||
		receipt["schema"] != "wipd.terminal-receipt/1" || receipt["domain_id"] != identity.DomainID || receipt["authority_epoch"] != identity.AuthorityEpoch ||
		receipt["identity_schema"] != "wipd.command/1" || receipt["command_id"] != commandID || receipt["request_hash"] != requestHash {
		return errInvalidClaimGrant
	}
	operation, ok := receipt["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operation, "name", "version") || operation["name"] != "claim.acquire" || operation["version"] != uint64(1) {
		return errInvalidClaimGrant
	}
	environment, ok := receipt["environment"].(map[string]any)
	sequence, sequenceOK := environment["sequence"].(uint64)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["id"] != identity.EnvironmentID || !sequenceOK || sequence == 0 {
		return errInvalidClaimGrant
	}
	result, ok := receipt["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") || result["code"] != "result.succeeded" || result["problem_code"] != nil {
		return errInvalidClaimGrant
	}
	outputBytes, ok := result["output"].([]byte)
	if !ok {
		return errInvalidClaimGrant
	}
	output, err := decodeClosed(outputBytes, "claim", "matter_id", "batch_id", "dispatch_id")
	if err != nil || output["matter_id"] != matterID || output["batch_id"] != batchID || output["dispatch_id"] != dispatchID {
		return errInvalidClaimGrant
	}
	outputClaim, ok := output["claim"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(outputClaim, "id", "epoch") || outputClaim["id"] != claimID || outputClaim["epoch"] != claimEpoch {
		return errInvalidClaimGrant
	}
	accepted, ok := receipt["accepted_events"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") {
		return errInvalidClaimGrant
	}
	first, firstOK := accepted["first_event_id"].(string)
	last, lastOK := accepted["last_event_id"].(string)
	count, countOK := accepted["event_count"].(uint64)
	if !firstOK || !lastOK || !countOK || (count != 2 && count != 3) || !transferULID.MatchString(first) || !transferULID.MatchString(last) {
		return errInvalidClaimGrant
	}
	firstIndex, lastIndex := -1, -1
	for index, record := range transfer.records {
		if record.EventID == first {
			firstIndex = index
		}
		if record.EventID == last {
			lastIndex = index
		}
	}
	if firstIndex < 0 || lastIndex-firstIndex+1 != int(count) {
		return errInvalidClaimGrant
	}
	commandEvents := transfer.records[firstIndex : lastIndex+1]
	if commandEvents[0].EventID != first || commandEvents[len(commandEvents)-1].EventID != last {
		return errInvalidClaimGrant
	}
	commonRepo := ""
	commonActedAt := ""
	worktreeID := ""
	for index, event := range commandEvents {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(event.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil || fields["command_id"] != commandID || fields["request_hash"] != requestHash || fields["domain_id"] != identity.DomainID {
			return errInvalidClaimGrant
		}
		env, ok := fields["environment"].(map[string]any)
		if !ok || env["id"] != identity.EnvironmentID || env["sequence"] != sequence {
			return errInvalidClaimGrant
		}
		actedAt, actedAtOK := fields["acted_at"].(string)
		if !actedAtOK || index > 0 && actedAt != commonActedAt {
			return errInvalidClaimGrant
		}
		if index == 0 {
			commonActedAt = actedAt
		}
		repo := asString(fields["repo_id"])
		if commonRepo == "" {
			commonRepo = repo
		} else if repo != commonRepo {
			return errInvalidClaimGrant
		}
		if commonRepo != identity.RepoID {
			return errInvalidClaimGrant
		}
		payload, ok := fields["payload"].(map[string]any)
		if !ok {
			return errInvalidClaimGrant
		}
		if count == 3 && index == 0 {
			if fields["kind"] != "batch.anonymous-created" || fields["subject_id"] != batchID ||
				!wipdwire.ExactMapKeys(payload, "batch_id", "matter_id") || payload["batch_id"] != batchID || payload["matter_id"] != matterID {
				return errInvalidClaimGrant
			}
			continue
		}
		claimIndex := index
		if count == 3 {
			claimIndex--
		}
		switch claimIndex {
		case 0:
			worktreeID = asString(payload["worktree_id"])
			if fields["kind"] != "claim.acquired" || fields["subject_id"] != claimID ||
				!wipdwire.ExactMapKeys(payload, "claim_id", "claim_epoch", "matter_id", "batch_id", "dispatch_id", "owner_environment_id", "worktree_id") ||
				payload["claim_id"] != claimID || payload["claim_epoch"] != claimEpoch || payload["matter_id"] != matterID ||
				payload["batch_id"] != batchID || payload["dispatch_id"] != dispatchID || payload["owner_environment_id"] != identity.EnvironmentID ||
				!transferULID.MatchString(asString(payload["worktree_id"])) {
				return errInvalidClaimGrant
			}
		case 1:
			if fields["kind"] != "dispatch.opened" || fields["subject_id"] != dispatchID ||
				!wipdwire.ExactMapKeys(payload, "dispatch_id", "matter_id", "batch_id", "claim_id", "worktree_id") ||
				payload["dispatch_id"] != dispatchID || payload["matter_id"] != matterID || payload["batch_id"] != batchID || payload["claim_id"] != claimID ||
				asString(payload["worktree_id"]) != worktreeID {
				return errInvalidClaimGrant
			}
		default:
			return errInvalidClaimGrant
		}
	}
	return nil
}

func claimGrantWorktree(receipt map[string]any, transfer VerifiedTransfer) string {
	accepted, ok := receipt["accepted_events"].(map[string]any)
	if !ok {
		return ""
	}
	first, firstOK := accepted["first_event_id"].(string)
	last, lastOK := accepted["last_event_id"].(string)
	if !firstOK || !lastOK {
		return ""
	}
	insideRange := false
	for _, record := range transfer.records {
		if record.EventID == first {
			insideRange = true
		}
		if !insideRange {
			continue
		}
		fields, err := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			return ""
		}
		if fields["kind"] == "claim.acquired" {
			payload, ok := fields["payload"].(map[string]any)
			if !ok {
				return ""
			}
			return asString(payload["worktree_id"])
		}
		if record.EventID == last {
			break
		}
	}
	return ""
}

func verifyClaimGrantEnd(raw []byte, grantID string, end wipdwire.PrefixAnchor, manifestDigest string) error {
	fields, err := decodeClosed(raw, "schema", "grant_id", "verified_prefix", "verified_blob_manifest_digest", "complete")
	if err != nil || fields["schema"] != "wipd.claim-grant-end/1" || fields["grant_id"] != grantID ||
		fields["verified_blob_manifest_digest"] != manifestDigest || fields["complete"] != true {
		return errInvalidClaimGrant
	}
	verified, ok := decodeAnchor(fields["verified_prefix"])
	if !ok || !sameTransferAnchor(verified, end) {
		return errInvalidClaimGrant
	}
	return nil
}

func decodeAnchor(value any) (wipdwire.PrefixAnchor, bool) {
	var anchor wipdwire.PrefixAnchor
	fields, ok := value.(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(fields, "event_count", "high_water_event_id", "prefix_digest") {
		return anchor, false
	}
	count, countOK := fields["event_count"].(uint64)
	digest, digestOK := fields["prefix_digest"].(string)
	if !countOK || !digestOK {
		return anchor, false
	}
	anchor.EventCount, anchor.Digest = count, digest
	if fields["high_water_event_id"] != nil {
		eventID, eventOK := fields["high_water_event_id"].(string)
		if !eventOK {
			return wipdwire.PrefixAnchor{}, false
		}
		anchor.EventID = &eventID
	}
	return anchor, validTransferAnchor(anchor)
}

func decodeClosed(raw []byte, keys ...string) (map[string]any, error) {
	fields, err := wipdwire.DecodeCanonicalMap(raw, keys...)
	if err != nil || !wipdwire.ExactMapKeys(fields, keys...) {
		return nil, errInvalidClaimGrant
	}
	return fields, nil
}

func verifySignedArtifact(fields map[string]any, public ed25519.PublicKey) bool {
	signature, ok := fields["signature"].([]byte)
	if !ok || len(signature) != ed25519.SignatureSize || len(public) != ed25519.PublicKeySize {
		return false
	}
	unsignedFields := make(map[string]any, len(fields)-1)
	for key, value := range fields {
		if key != "signature" {
			unsignedFields[key] = value
		}
	}
	unsigned, err := wipdwire.EncodeCanonical(unsignedFields)
	return err == nil && ed25519.Verify(public, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...), signature)
}

func canonicalArtifactTime(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || parsed.Location() != time.UTC || parsed.UTC().Format(time.RFC3339Nano) != text {
		return time.Time{}, false
	}
	return parsed, true
}

func validCanonicalTime(value any) bool {
	_, ok := canonicalArtifactTime(value)
	return ok
}

func validOptionalDigest(value any) bool {
	if value == nil {
		return true
	}
	digest, ok := value.(string)
	return ok && transferHash.MatchString(digest)
}

func asBytes(value any) []byte {
	data, _ := value.([]byte)
	return data
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
