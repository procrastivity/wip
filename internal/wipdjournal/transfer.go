package wipdjournal

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	maxVerifiedEvents       = 32
	maxVerifiedEventBytes   = 4 << 20
	maxVerifiedManifestRows = 64
)

var (
	transferULID        = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	transferHash        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	transferStepLocator = regexp.MustCompile(`^step-[0-9]{2,}$`)
	// ErrInvalidTransfer means transfer records do not prove the advertised
	// complete prefix delta and manifest.
	ErrInvalidTransfer = errors.New("wipdjournal: invalid verified transfer")
)

// VerifiedTransfer is an immutable proof of one complete, contiguous event
// delta and the complete blob manifest at its end anchor. Its fields are
// private; consumers obtain data through copying accessors.
type VerifiedTransfer struct {
	domainID string
	epoch    uint64
	start    wipdwire.PrefixAnchor
	end      wipdwire.PrefixAnchor
	manifest wipdwire.BlobManifest
	records  []wipdwire.EventRecord
	verified bool
}

// VerifyTransfer constructs a transfer proof only when every record is
// canonical, the event-prefix chain reaches end, and the complete manifest
// digest is valid and anchored to that same end.
func VerifyTransfer(domainID string, epoch uint64, start, end wipdwire.PrefixAnchor, records []wipdwire.EventRecord, manifest wipdwire.BlobManifest) (VerifiedTransfer, error) {
	if !transferULID.MatchString(domainID) || epoch == 0 || !validTransferAnchor(start) || !validTransferAnchor(end) ||
		manifest.Schema != "wipd.blob-manifest/1" || manifest.DomainID != domainID || manifest.Epoch != epoch ||
		!sameTransferAnchor(manifest.AsOf, end) || !transferHash.MatchString(manifest.Digest) || len(records) > maxVerifiedEvents ||
		len(manifest.Entries) > maxVerifiedManifestRows || end.EventCount < start.EventCount ||
		end.EventCount-start.EventCount != uint64(len(records)) {
		return VerifiedTransfer{}, ErrInvalidTransfer
	}
	if err := verifyManifest(manifest); err != nil {
		return VerifiedTransfer{}, err
	}
	chain, err := hex.DecodeString(start.Digest[len("sha256:"):])
	if err != nil || len(chain) != sha256.Size {
		return VerifiedTransfer{}, ErrInvalidTransfer
	}
	previousID := ""
	if start.EventID != nil {
		previousID = *start.EventID
	}
	cloned := make([]wipdwire.EventRecord, len(records))
	var byteCount uint64
	for index, event := range records {
		if !transferULID.MatchString(event.EventID) || event.EventID <= previousID || len(event.Record) == 0 || len(event.Record) > wipdwire.FrameLimit ||
			uint64(len(event.Record)) > maxVerifiedEventBytes-byteCount || !validAuthorityEvent(event.Record, domainID, event.EventID) {
			return VerifiedTransfer{}, ErrInvalidTransfer
		}
		byteCount += uint64(len(event.Record))
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(event.Record)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(chain)
		_, _ = h.Write(length[:])
		_, _ = h.Write(event.Record)
		chain = h.Sum(nil)
		cloned[index] = wipdwire.EventRecord{EventID: event.EventID, Record: append([]byte(nil), event.Record...)}
		previousID = event.EventID
	}
	if end.EventCount == 0 {
		if end.EventID != nil {
			return VerifiedTransfer{}, ErrInvalidTransfer
		}
	} else if end.EventID == nil || *end.EventID != previousID {
		return VerifiedTransfer{}, ErrInvalidTransfer
	}
	if "sha256:"+hex.EncodeToString(chain) != end.Digest {
		return VerifiedTransfer{}, ErrInvalidTransfer
	}
	return VerifiedTransfer{
		domainID: domainID, epoch: epoch, start: cloneTransferAnchor(start), end: cloneTransferAnchor(end),
		manifest: cloneTransferManifest(manifest), records: cloned, verified: true,
	}, nil
}

// Valid reports whether the value was produced by VerifyTransfer.
func (transfer VerifiedTransfer) Valid() bool { return transfer.verified }

// DomainID returns the transfer's authority domain.
func (transfer VerifiedTransfer) DomainID() string { return transfer.domainID }

// Epoch returns the transfer's authority epoch.
func (transfer VerifiedTransfer) Epoch() uint64 { return transfer.epoch }

// Start returns a copy of the exact start prefix anchor.
func (transfer VerifiedTransfer) Start() wipdwire.PrefixAnchor {
	return cloneTransferAnchor(transfer.start)
}

// End returns a copy of the exact end prefix anchor.
func (transfer VerifiedTransfer) End() wipdwire.PrefixAnchor {
	return cloneTransferAnchor(transfer.end)
}

// Manifest returns a deep copy of the complete blob manifest.
func (transfer VerifiedTransfer) Manifest() wipdwire.BlobManifest {
	return cloneTransferManifest(transfer.manifest)
}

// Records returns deep copies of the exact ordered event records.
func (transfer VerifiedTransfer) Records() []wipdwire.EventRecord {
	result := make([]wipdwire.EventRecord, len(transfer.records))
	for index, record := range transfer.records {
		result[index] = wipdwire.EventRecord{EventID: record.EventID, Record: append([]byte(nil), record.Record...)}
	}
	return result
}

// EventIDs returns the event IDs proven by this exact delta.
func (transfer VerifiedTransfer) EventIDs() []string {
	ids := make([]string, len(transfer.records))
	for index, record := range transfer.records {
		ids[index] = record.EventID
	}
	return ids
}

func verifyManifest(manifest wipdwire.BlobManifest) error {
	chain := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	previous := ""
	for _, entry := range manifest.Entries {
		if !transferHash.MatchString(entry.Digest) || entry.ByteLength > uint64(maxBlobSize) ||
			(entry.Requirement != "lazy" && entry.Requirement != "pin-before-use") {
			return ErrInvalidTransfer
		}
		if previous != "" && previous >= entry.Digest {
			return ErrInvalidTransfer
		}
		previous = entry.Digest
		digest, err := hex.DecodeString(entry.Digest[len("sha256:"):])
		if err != nil {
			return ErrInvalidTransfer
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], entry.ByteLength)
		requirement := byte(0)
		if entry.Requirement == "pin-before-use" {
			requirement = 1
		}
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/blob-manifest-step/v1\x00"))
		_, _ = h.Write(chain[:])
		_, _ = h.Write(digest)
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte{requirement})
		copy(chain[:], h.Sum(nil))
	}
	if "sha256:"+hex.EncodeToString(chain[:]) != manifest.Digest {
		return ErrInvalidTransfer
	}
	return nil
}

func validAuthorityEvent(record []byte, domainID, eventID string) bool {
	fields, err := wipdwire.DecodeCanonicalMap(record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["schema"] != "wipd.event/1" || fields["event_id"] != eventID || fields["domain_id"] != domainID ||
		(fields["kind"] != "matter.created" && fields["kind"] != "step.created" && fields["kind"] != "claim.released") || !transferULID.MatchString(asString(fields["command_id"])) ||
		!transferHash.MatchString(asString(fields["request_hash"])) || !transferULID.MatchString(asString(fields["repo_id"])) {
		return false
	}
	if !canonicalUTC(fields["acted_at"]) || !canonicalUTC(fields["occurred_at"]) {
		return false
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || !transferULID.MatchString(asString(environment["id"])) ||
		!positiveUint(environment["sequence"]) {
		return false
	}
	payload, ok := fields["payload"].(map[string]any)
	if !ok {
		return false
	}
	switch fields["kind"] {
	case "matter.created":
		return wipdwire.ExactMapKeys(payload, "id", "locator", "title") &&
			transferULID.MatchString(asString(payload["id"])) && fields["subject_id"] == payload["id"] &&
			asString(payload["locator"]) != "" && asString(payload["title"]) != ""
	case "step.created":
		parent := asString(payload["parent"])
		locator, locatorOK := payload["locator"].(string)
		_, titleOK := payload["title"].(string)
		return wipdwire.ExactMapKeys(payload, "title", "locator", "parent", "sort_key") &&
			transferULID.MatchString(asString(fields["subject_id"])) && transferULID.MatchString(parent) &&
			locatorOK && transferStepLocator.MatchString(locator) && titleOK && positiveUint(payload["sort_key"])
	case "claim.released":
		claimID := asString(payload["claim_id"])
		epoch, epochOK := payload["claim_epoch"].(uint64)
		barrier := asString(payload["barrier_digest"])
		dispatch, dispatchPresent := payload["dispatch_id"]
		dispatchValid := dispatchPresent && (dispatch == nil || transferULID.MatchString(asString(dispatch)))
		return wipdwire.ExactMapKeys(payload, "claim_id", "claim_epoch", "dispatch_id", "barrier_digest") &&
			transferULID.MatchString(claimID) && fields["subject_id"] == claimID && epochOK && epoch > 0 &&
			dispatchValid && transferHash.MatchString(barrier)
	default:
		return false
	}
}

func canonicalUTC(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	return err == nil && parsed.UTC().Format(time.RFC3339Nano) == text
}

func positiveUint(value any) bool {
	number, ok := value.(uint64)
	return ok && number > 0
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func validTransferAnchor(anchor wipdwire.PrefixAnchor) bool {
	if !transferHash.MatchString(anchor.Digest) {
		return false
	}
	if anchor.EventCount == 0 {
		empty := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
		return anchor.EventID == nil && anchor.Digest == "sha256:"+hex.EncodeToString(empty[:])
	}
	return anchor.EventID != nil && transferULID.MatchString(*anchor.EventID)
}

func sameTransferAnchor(left, right wipdwire.PrefixAnchor) bool {
	if left.EventCount != right.EventCount || left.Digest != right.Digest || (left.EventID == nil) != (right.EventID == nil) {
		return false
	}
	return left.EventID == nil || *left.EventID == *right.EventID
}

func cloneTransferAnchor(anchor wipdwire.PrefixAnchor) wipdwire.PrefixAnchor {
	copyOf := anchor
	if anchor.EventID != nil {
		eventID := *anchor.EventID
		copyOf.EventID = &eventID
	}
	return copyOf
}

func cloneTransferManifest(manifest wipdwire.BlobManifest) wipdwire.BlobManifest {
	copyOf := manifest
	copyOf.AsOf = cloneTransferAnchor(manifest.AsOf)
	copyOf.Entries = append([]wipdwire.BlobManifestEntry(nil), manifest.Entries...)
	if copyOf.Entries == nil {
		copyOf.Entries = []wipdwire.BlobManifestEntry{}
	}
	return copyOf
}
