package wipdjournal

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	maxVerifiedEvents       = 32
	maxVerifiedEventBytes   = 4 << 20
	maxVerifiedManifestRows = 64
)

var (
	transferULID          = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	transferHash          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	transferMatterLocator = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	transferStepLocator   = regexp.MustCompile(`^step-[0-9]{2,}$`)
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
		(fields["kind"] != "matter.created" && fields["kind"] != "step.created" && fields["kind"] != "batch.created" && fields["kind"] != "claim.released" &&
			fields["kind"] != "batch.anonymous-created" && fields["kind"] != "claim.acquired" && fields["kind"] != "dispatch.opened" &&
			fields["kind"] != "dispatch.closed" &&
			fields["kind"] != "matter.started" && fields["kind"] != "step.started" && fields["kind"] != "step.finished" &&
			fields["kind"] != "matter.finished" && fields["kind"] != "batch.swept" && fields["kind"] != "content.created" && fields["kind"] != "content.appended" &&
			fields["kind"] != "stage.created" && fields["kind"] != "step.inserted" && fields["kind"] != "step.reordered" &&
			fields["kind"] != "step.replaced" && fields["kind"] != "step.removed" &&
			fields["kind"] != "matter.locator-repair-required" && fields["kind"] != "matter.locator-repaired" &&
			fields["kind"] != "gate.declared" && fields["kind"] != "gate.closed" && fields["kind"] != "gate.dismissed" && fields["kind"] != "gate.exemption-repaired" &&
			!step8HistoryKind(asString(fields["kind"])) &&
			!transferLifecycleEventKind(asString(fields["kind"]))) ||
		!transferULID.MatchString(asString(fields["command_id"])) || !transferHash.MatchString(asString(fields["request_hash"])) {
		return false
	}
	if fields["kind"] == "batch.created" {
		if fields["repo_id"] != nil || !transferULID.MatchString(asString(fields["subject_id"])) {
			return false
		}
	} else if !transferULID.MatchString(asString(fields["repo_id"])) {
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
	kind := asString(fields["kind"])
	if kind == "batch.created" {
		payloadName := asString(payload["name"])
		return wipdwire.ExactMapKeys(payload, "name") && payloadName != "" && payloadName == strings.TrimSpace(payloadName)
	}
	if transferLifecycleEventKind(kind) {
		return validTransferLifecycleEvent(kind, asString(fields["subject_id"]), payload)
	}
	if step8HistoryKind(kind) {
		return validStep8HistoryEvent(kind, asString(fields["subject_id"]), asString(fields["repo_id"]), payload)
	}
	switch kind {
	case "gate.exemption-repaired":
		return wipdwire.ExactMapKeys(payload, "gate") && asString(payload["gate"]) != "" && transferULID.MatchString(asString(fields["subject_id"]))
	case "gate.declared":
		if !wipdwire.ExactMapKeys(payload, "gate", "scale") && !wipdwire.ExactMapKeys(payload, "gate", "scale", "exempt") ||
			fields["subject_id"] != fields["repo_id"] || asString(payload["gate"]) == "" || !transferGateScale(asString(payload["scale"])) {
			return false
		}
		if raw, present := payload["exempt"]; present {
			exempt, ok := raw.([]any)
			if !ok || len(exempt) == 0 {
				return false
			}
			seen := make(map[string]bool, len(exempt))
			for _, node := range exempt {
				id := asString(node)
				if !transferULID.MatchString(id) || seen[id] {
					return false
				}
				seen[id] = true
			}
		}
		return true
	case "gate.closed", "gate.dismissed":
		keys := []string{"gate", "scale"}
		if kind == "gate.dismissed" {
			keys = append(keys, "reason")
			if strings.TrimSpace(asString(payload["reason"])) == "" {
				return false
			}
		}
		if level, present := payload["tracker_push_level"]; present {
			if !trackerPushLevel(asString(level)) {
				return false
			}
			keys = append(keys, "tracker_push_level")
		}
		return wipdwire.ExactMapKeys(payload, keys...) && asString(payload["gate"]) != "" &&
			transferGateScale(asString(payload["scale"])) && transferULID.MatchString(asString(fields["subject_id"]))
	case "batch.swept":
		return wipdwire.ExactMapKeys(payload) && transferULID.MatchString(asString(fields["subject_id"]))
	case "batch.anonymous-created":
		batchID := asString(payload["batch_id"])
		matterID := asString(payload["matter_id"])
		return wipdwire.ExactMapKeys(payload, "batch_id", "matter_id") &&
			transferULID.MatchString(batchID) && transferULID.MatchString(matterID) && fields["subject_id"] == batchID
	case "claim.acquired":
		claimID := asString(payload["claim_id"])
		_, epochOK := payload["claim_epoch"].(uint64)
		return wipdwire.ExactMapKeys(payload, "claim_id", "claim_epoch", "matter_id", "batch_id", "dispatch_id", "owner_environment_id", "worktree_id") &&
			transferULID.MatchString(claimID) && fields["subject_id"] == claimID && epochOK && positiveUint(payload["claim_epoch"]) &&
			transferULID.MatchString(asString(payload["matter_id"])) && transferULID.MatchString(asString(payload["batch_id"])) &&
			transferULID.MatchString(asString(payload["dispatch_id"])) && transferULID.MatchString(asString(payload["owner_environment_id"])) &&
			transferULID.MatchString(asString(payload["worktree_id"]))
	case "dispatch.opened":
		dispatchID := asString(payload["dispatch_id"])
		return wipdwire.ExactMapKeys(payload, "dispatch_id", "matter_id", "batch_id", "claim_id", "worktree_id") &&
			transferULID.MatchString(dispatchID) && fields["subject_id"] == dispatchID &&
			transferULID.MatchString(asString(payload["matter_id"])) && transferULID.MatchString(asString(payload["batch_id"])) &&
			transferULID.MatchString(asString(payload["claim_id"])) && transferULID.MatchString(asString(payload["worktree_id"]))
	case "dispatch.closed":
		dispatchID := asString(payload["dispatch_id"])
		claimEpoch, epochOK := payload["claim_epoch"].(uint64)
		return wipdwire.ExactMapKeys(payload, "dispatch_id", "claim_id", "claim_epoch") &&
			transferULID.MatchString(dispatchID) && fields["subject_id"] == dispatchID &&
			transferULID.MatchString(asString(payload["claim_id"])) && epochOK && claimEpoch > 0
	case "matter.created":
		return wipdwire.ExactMapKeys(payload, "id", "locator", "title") &&
			transferULID.MatchString(asString(payload["id"])) && fields["subject_id"] == payload["id"] &&
			asString(payload["locator"]) != "" && asString(payload["title"]) != ""
	case "stage.created":
		title := asString(payload["title"])
		return wipdwire.ExactMapKeys(payload, "matter_id", "locator", "title", "sort_key") &&
			transferULID.MatchString(asString(fields["subject_id"])) && transferULID.MatchString(asString(payload["matter_id"])) &&
			transferMatterLocator.MatchString(asString(payload["locator"])) && strings.TrimSpace(title) != "" && positiveUint(payload["sort_key"])
	case "step.created":
		parent := asString(payload["parent"])
		locator, locatorOK := payload["locator"].(string)
		title := asString(payload["title"])
		return wipdwire.ExactMapKeys(payload, "title", "locator", "parent", "sort_key") &&
			transferULID.MatchString(asString(fields["subject_id"])) && transferULID.MatchString(parent) &&
			locatorOK && transferStepLocator.MatchString(locator) && strings.TrimSpace(title) != "" && positiveUint(payload["sort_key"])
	case "step.inserted":
		parent := asString(payload["parent"])
		locator := asString(payload["locator"])
		title := asString(payload["title"])
		return wipdwire.ExactMapKeys(payload, "title", "locator", "parent", "sort_key") &&
			transferULID.MatchString(asString(fields["subject_id"])) && transferULID.MatchString(parent) &&
			transferStepLocator.MatchString(locator) && strings.TrimSpace(title) != "" && positiveUint(payload["sort_key"])
	case "step.reordered":
		order, ok := payload["order"].([]any)
		if !wipdwire.ExactMapKeys(payload, "order") || !transferULID.MatchString(asString(fields["subject_id"])) || !ok || len(order) == 0 {
			return false
		}
		seen := make(map[string]bool, len(order))
		for _, value := range order {
			id := asString(value)
			if !transferULID.MatchString(id) || seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	case "step.replaced":
		replacement := asString(payload["replacement"])
		title := asString(payload["title"])
		return wipdwire.ExactMapKeys(payload, "replacement", "title", "locator") &&
			transferULID.MatchString(asString(fields["subject_id"])) && transferULID.MatchString(replacement) && replacement != fields["subject_id"] &&
			transferStepLocator.MatchString(asString(payload["locator"])) && strings.TrimSpace(title) != ""
	case "step.removed":
		reason := asString(payload["reason"])
		return wipdwire.ExactMapKeys(payload, "reason") && transferULID.MatchString(asString(fields["subject_id"])) &&
			strings.TrimSpace(reason) != ""
	case "matter.locator-repair-required":
		requested, assigned := asString(payload["requested_locator"]), asString(payload["assigned_locator"])
		return wipdwire.ExactMapKeys(payload, "requested_locator", "assigned_locator") &&
			transferULID.MatchString(asString(fields["subject_id"])) && transferMatterLocator.MatchString(requested) &&
			transferMatterLocator.MatchString(assigned) && requested != assigned
	case "matter.locator-repaired":
		action := asString(payload["action"])
		requested := asString(payload["requested_locator"])
		previous := asString(payload["previous_locator"])
		assigned := asString(payload["assigned_locator"])
		return wipdwire.ExactMapKeys(payload, "action", "requested_locator", "previous_locator", "assigned_locator") &&
			transferULID.MatchString(asString(fields["subject_id"])) && (action == "accept" || action == "rename") &&
			transferMatterLocator.MatchString(requested) && transferMatterLocator.MatchString(previous) &&
			transferMatterLocator.MatchString(assigned) && requested != previous &&
			(action == "accept" && assigned == previous || action == "rename" && assigned != previous)
	case "claim.released":
		claimID := asString(payload["claim_id"])
		epoch, epochOK := payload["claim_epoch"].(uint64)
		barrier := asString(payload["barrier_digest"])
		dispatch, dispatchPresent := payload["dispatch_id"]
		dispatchValid := dispatchPresent && (dispatch == nil || transferULID.MatchString(asString(dispatch)))
		return wipdwire.ExactMapKeys(payload, "claim_id", "claim_epoch", "dispatch_id", "barrier_digest") &&
			transferULID.MatchString(claimID) && fields["subject_id"] == claimID && epochOK && epoch > 0 &&
			dispatchValid && transferHash.MatchString(barrier)
	case "content.created", "content.appended":
		contentID := asString(payload["content"])
		contentKind := asString(payload["kind"])
		digest := asString(payload["blob_ref"])
		sha := asString(payload["sha256"])
		length, lengthOK := payload["byte_len"].(uint64)
		wantKind := "content.created"
		kindOK := contentKind == "brief" || contentKind == "workplan" || contentKind == "body"
		if fields["kind"] == "content.appended" {
			wantKind = "content.appended"
			kindOK = contentKind == "findings"
		}
		return wipdwire.ExactMapKeys(payload, "kind", "content", "bytes", "blob_ref", "byte_len", "sha256") &&
			transferULID.MatchString(contentID) && transferULID.MatchString(asString(fields["subject_id"])) &&
			fields["kind"] == wantKind && kindOK && payload["bytes"] == nil && lengthOK && length <= uint64(maxBlobSize) &&
			transferHash.MatchString(digest) && transferHash.MatchString("sha256:"+sha) && digest == "sha256:"+sha
	default:
		return false
	}
}

func transferGateScale(scale string) bool {
	return scale == "matter" || scale == "stage" || scale == "step"
}

func transferLifecycleEventKind(kind string) bool {
	scale, verb, ok := strings.Cut(kind, ".")
	if !ok || (scale != "matter" && scale != "stage" && scale != "step") {
		return false
	}
	switch verb {
	case "started", "finished", "paused", "resumed", "canceled":
		return true
	default:
		return false
	}
}

func validTransferLifecycleEvent(kind, subject string, payload map[string]any) bool {
	if !transferULID.MatchString(subject) {
		return false
	}
	if level, present := payload["tracker_push_level"]; present {
		if !trackerPushLevel(asString(level)) {
			return false
		}
		withoutLevel := make(map[string]any, len(payload)-1)
		for key, value := range payload {
			if key != "tracker_push_level" {
				withoutLevel[key] = value
			}
		}
		return validTransferLifecycleEvent(kind, subject, withoutLevel)
	}
	scale, verb, _ := strings.Cut(kind, ".")
	if verb == "started" {
		if payload["from"] != "planned" || payload["to"] != "in-progress" {
			return false
		}
		keys := wipdwire.ExactMapKeys(payload, "from", "to")
		cascade := payload["cascade"] == true && (wipdwire.ExactMapKeys(payload, "from", "to", "cascade") ||
			wipdwire.ExactMapKeys(payload, "from", "to", "cascade", "cause_event_id"))
		cause, hasCause := payload["cause_event_id"]
		causalTarget := scale != "matter" && hasCause && transferULID.MatchString(asString(cause)) &&
			wipdwire.ExactMapKeys(payload, "from", "to", "cause_event_id")
		causalAncestor := scale != "matter" && cascade && hasCause && transferULID.MatchString(asString(cause)) &&
			wipdwire.ExactMapKeys(payload, "from", "to", "cascade", "cause_event_id")
		standaloneAncestor := cascade && wipdwire.ExactMapKeys(payload, "from", "to", "cascade")
		return keys || cascade && !hasCause && standaloneAncestor || causalTarget || causalAncestor ||
			cascade && !hasCause && standaloneAncestor && scale == "matter"
	}
	from, to := "in-progress", map[string]string{
		"finished": "done", "paused": "paused", "resumed": "in-progress", "canceled": "canceled",
	}[verb]
	if verb == "resumed" {
		from = "paused"
	}
	if payload["from"] != from || payload["to"] != to {
		return false
	}
	keys := []string{"from", "to"}
	if verb == "canceled" {
		if reason, exists := payload["reason"]; exists {
			if strings.TrimSpace(asString(reason)) == "" {
				return false
			}
			keys = append(keys, "reason")
		}
	}
	return wipdwire.ExactMapKeys(payload, keys...)
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
