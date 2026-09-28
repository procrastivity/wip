package wipdseed

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
	"golang.org/x/sys/unix"
)

const (
	maxClientTransferEvents  = 32
	maxClientManifestEntries = 64
	maxClientTransferBytes   = 4 << 20
	maxClientStateBytes      = 8 << 20
)

type sessionLimits struct {
	frameBody   int
	streamBytes int
}

type eventProjection struct {
	ID           string `json:"id"`
	RepoID       string `json:"repo_id"`
	Locator      string `json:"locator"`
	Title        string `json:"title"`
	BirthEventID string `json:"birth_event_id"`
}

type stepProjection struct {
	ID           string `json:"id"`
	RepoID       string `json:"repo_id"`
	MatterID     string `json:"matter_id"`
	Locator      string `json:"locator"`
	Title        string `json:"title"`
	SortKey      int64  `json:"sort_key"`
	State        string `json:"state"`
	BirthEventID string `json:"birth_event_id"`
}

type matterCreatedEvent struct {
	Schema      string `cbor:"schema"`
	EventID     string `cbor:"event_id"`
	DomainID    string `cbor:"domain_id"`
	CommandID   string `cbor:"command_id"`
	Hash        string `cbor:"request_hash"`
	Environment struct {
		ID       string `cbor:"id"`
		Sequence uint64 `cbor:"sequence"`
	} `cbor:"environment"`
	ActedAt    string `cbor:"acted_at"`
	OccurredAt string `cbor:"occurred_at"`
	Kind       string `cbor:"kind"`
	SubjectID  string `cbor:"subject_id"`
	RepoID     string `cbor:"repo_id"`
	Payload    struct {
		ID      string `cbor:"id"`
		Locator string `cbor:"locator"`
		Title   string `cbor:"title"`
	} `cbor:"payload"`
}

type stepCreatedEvent struct {
	Schema      string `cbor:"schema"`
	EventID     string `cbor:"event_id"`
	DomainID    string `cbor:"domain_id"`
	CommandID   string `cbor:"command_id"`
	Hash        string `cbor:"request_hash"`
	Environment struct {
		ID       string `cbor:"id"`
		Sequence uint64 `cbor:"sequence"`
	} `cbor:"environment"`
	ActedAt    string `cbor:"acted_at"`
	OccurredAt string `cbor:"occurred_at"`
	Kind       string `cbor:"kind"`
	SubjectID  string `cbor:"subject_id"`
	RepoID     string `cbor:"repo_id"`
	Payload    struct {
		Title   string `cbor:"title"`
		Locator string `cbor:"locator"`
		Parent  string `cbor:"parent"`
		SortKey int64  `cbor:"sort_key"`
	} `cbor:"payload"`
}

// PullAndInstall fetches one bounded complete delta from the installed anchor,
// validates the authority's event and manifest chains, folds the supported M1
// projections, and atomically replaces the local base only after PullEnd.
func PullAndInstall(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, directory string) (ClientState, error) {
	var empty ClientState
	if ctx == nil {
		return empty, ErrInvalidClientState
	}
	previous, original, err := loadInstalledState(directory)
	if err != nil {
		return empty, err
	}
	defer clear(original)
	defer clear(previous.PrivateKeyPKCS8)
	if err = validateInstalledState(previous, profile); err != nil {
		return empty, err
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(previous.PrivateKeyPKCS8)
	if err != nil {
		return empty, ErrInvalidClientState
	}
	if _, ok := privateKey.(crypto.Signer); !ok {
		return empty, ErrInvalidClientState
	}
	certificate, err := validateStoredCertificate(previous, privateKey, time.Now().UTC())
	if err != nil {
		return empty, err
	}
	client, err := profile.HTTPClientWithCertificate(roots, &certificate)
	if err != nil {
		return empty, err
	}
	defer client.CloseIdleConnections()
	limits, err := negotiateRemote(ctx, client, profile.Origin())
	if err != nil {
		return empty, err
	}
	request := wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: previous.DomainID, Epoch: previous.Epoch,
		Installed: previous.Prefix,
	}
	requestFrame, requestID, err := encodeRequestFrame("pull.request", request)
	if err != nil {
		return empty, err
	}
	if !withinSessionFrame(requestFrame, limits) {
		return empty, ErrInvalidClientState
	}
	frames, err := postFramesWithinSession(ctx, client, profile.Origin()+"/wipd/v1/exchange", requestFrame, requestID, maxClientTransferEvents+3, limits)
	if err != nil {
		return empty, err
	}
	updated, err := verifyTransferFrames(frames, "pull", profile, previous.RepoID, previous.EnvironmentID,
		previous.SPKIDigest, previous.Prefix, previous.EventRecords)
	if err != nil {
		return empty, err
	}
	updated.PrivateKeyPKCS8 = bytes.Clone(previous.PrivateKeyPKCS8)
	updated.CertificateDER = clone2D(previous.CertificateDER)
	if err = replaceInstalledState(directory, original, updated); err != nil {
		clear(updated.PrivateKeyPKCS8)
		return empty, err
	}
	return updated, nil
}

func negotiateRemote(ctx context.Context, client *http.Client, origin string) (sessionLimits, error) {
	return negotiateRemoteOperations(ctx, client, origin, nil)
}

func negotiateRemoteOperations(ctx context.Context, client *http.Client, origin string, operations []operation.ID) (sessionLimits, error) {
	var limits sessionLimits
	capabilities := make([]any, 0, len(operations))
	previous := operation.ID{}
	for index, id := range operations {
		if id.Name == "" || id.Version == 0 || index > 0 && (id.Name < previous.Name || id.Name == previous.Name && id.Version <= previous.Version) {
			return limits, ErrInvalidClientState
		}
		capabilities = append(capabilities, map[string]any{
			"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{"wipd.command/1"},
		})
		previous = id
	}
	hello := map[string]any{
		"protocol_min":     []any{uint64(1), uint64(0)},
		"protocol_max":     []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"},
		"operations":       capabilities,
		"store_schemas":    []any{"wipd.store/1"},
		"features":         []any{"wipd.frame/1"},
	}
	requestFrame, requestID, err := encodeRequestFrame("client.hello", hello)
	if err != nil {
		return limits, err
	}
	frames, err := postFrames(ctx, client, origin+"/wipd/v1/negotiate", requestFrame, requestID, 2)
	if err != nil {
		return limits, err
	}
	if len(frames) == 1 && frames[0].Kind == "problem" {
		return limits, decodeProblem(frames[0].Payload)
	}
	if len(frames) != 2 || frames[0].Sequence != 0 || frames[1].Sequence != 1 ||
		frames[0].Kind != "server.hello" || frames[1].Kind != "session.parameters" {
		return limits, ErrInvalidClientState
	}
	selection, err := wipdwire.DecodeCanonicalMap(frames[0].Payload,
		"selected_protocol", "identity_schemas", "operations", "store_schemas", "features")
	if err != nil || !equalVersion(selection["selected_protocol"], 1, 0) ||
		!equalStringsValue(selection["identity_schemas"], "wipd.command/1") || !equalNegotiatedOperations(selection["operations"], operations) ||
		!equalStringsValue(selection["store_schemas"], "wipd.store/1") || !equalStringsValue(selection["features"], "wipd.frame/1") {
		return limits, ErrInvalidClientState
	}
	parameters, err := wipdwire.DecodeCanonicalMap(frames[1].Payload,
		"frame_schema", "max_frame_body", "max_chunk_data", "max_stream_bytes", "max_concurrent_exchanges", "receive_window_bytes")
	if err != nil || parameters["frame_schema"] != "wipd.frame/1" ||
		!boundedUint(parameters["max_frame_body"], 1, 1_048_576) ||
		!boundedUint(parameters["max_chunk_data"], 1, 65_536) ||
		!boundedUint(parameters["max_stream_bytes"], 1, 1_099_511_627_776) ||
		!boundedUint(parameters["max_concurrent_exchanges"], 1, 64) ||
		!boundedUint(parameters["receive_window_bytes"], 1, 16_777_216) {
		return limits, ErrInvalidClientState
	}
	frameBody, _ := parameters["max_frame_body"].(uint64)
	streamBytes, _ := parameters["max_stream_bytes"].(uint64)
	if frameBody > wipdwire.FrameLimit {
		frameBody = wipdwire.FrameLimit
	}
	if streamBytes > maxClientTransferBytes {
		streamBytes = maxClientTransferBytes
	}
	limits = sessionLimits{frameBody: int(frameBody), streamBytes: int(streamBytes)}
	return limits, nil
}

func equalNegotiatedOperations(value any, expected []operation.ID) bool {
	items, ok := value.([]any)
	if !ok || len(items) != len(expected) {
		return false
	}
	for index, item := range items {
		fields, ok := item.(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(fields, "name", "versions", "identity_schemas") {
			return false
		}
		name, nameOK := fields["name"].(string)
		versions, versionsOK := fields["versions"].([]any)
		schemas, schemaOK := fields["identity_schemas"].([]any)
		if !nameOK || !versionsOK || len(versions) != 1 || versions[0] != uint64(expected[index].Version) ||
			!schemaOK || len(schemas) != 1 || schemas[0] != "wipd.command/1" || name != expected[index].Name {
			return false
		}
	}
	return true
}

// VerifyPullTransfer verifies a complete M2 pull against the exact previously
// installed event lineage and returns the concrete journal installation proof.
func VerifyPullTransfer(profile wipdauthority.Profile, previous ClientState, installed wipdwire.PrefixAnchor, frames []wipdwire.Frame) (wipdjournal.VerifiedTransfer, wipdwire.BlobManifest, error) {
	updated, err := verifyTransferFrames(frames, "pull", profile, previous.RepoID, previous.EnvironmentID,
		previous.SPKIDigest, installed, previous.EventRecords)
	if err != nil {
		return wipdjournal.VerifiedTransfer{}, wipdwire.BlobManifest{}, err
	}
	if len(updated.EventRecords) < len(previous.EventRecords) {
		return wipdjournal.VerifiedTransfer{}, wipdwire.BlobManifest{}, ErrInvalidClientState
	}
	manifest := wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: profile.DomainID(), Epoch: profile.Epoch(),
		AsOf: updated.Prefix, Entries: append([]wipdwire.BlobManifestEntry(nil), updated.ManifestEntries...),
		Digest: updated.ManifestDigest,
	}
	delta := cloneEventRecords(updated.EventRecords[len(previous.EventRecords):])
	transfer, err := wipdjournal.VerifyTransfer(profile.DomainID(), profile.Epoch(), installed, updated.Prefix, delta, manifest)
	if err != nil {
		return wipdjournal.VerifiedTransfer{}, wipdwire.BlobManifest{}, err
	}
	return transfer, manifest, nil
}

func withinSessionFrame(frame []byte, limits sessionLimits) bool {
	return len(frame) >= 4 && limits.frameBody > 0 && limits.streamBytes >= 4 &&
		len(frame) <= limits.streamBytes && int(binary.BigEndian.Uint32(frame[:4])) == len(frame)-4 &&
		len(frame)-4 <= limits.frameBody
}

func equalVersion(value any, major, minor uint64) bool {
	parts, ok := value.([]any)
	return ok && len(parts) == 2 && parts[0] == major && parts[1] == minor
}

func equalStringsValue(value any, expected string) bool {
	values, ok := value.([]any)
	return ok && len(values) == 1 && values[0] == expected
}

func boundedUint(value any, minimum, maximum uint64) bool {
	number, ok := value.(uint64)
	return ok && number >= minimum && number <= maximum
}

func verifyTransferFrames(frames []wipdwire.Frame, kind string, profile wipdauthority.Profile, repoID, environmentID, spkiDigest string, installed wipdwire.PrefixAnchor, prior []wipdwire.EventRecord) (ClientState, error) {
	var zero ClientState
	if (kind != "seed" && kind != "pull") || !clientULIDPattern.MatchString(repoID) || !clientULIDPattern.MatchString(environmentID) || !validDigest(spkiDigest) ||
		profile.DomainID() == "" || profile.Epoch() == 0 || len(frames) == 0 || len(frames) > maxClientTransferEvents+3 {
		return zero, ErrInvalidClientState
	}
	if frames[0].Kind == "problem" {
		return zero, decodeProblem(frames[0].Payload)
	}
	if len(frames) < 3 {
		return zero, ErrInvalidClientState
	}
	if kind == "seed" && (!anchorEqual(installed, emptyWireAnchor()) || len(prior) != 0) {
		return zero, ErrInvalidClientState
	}
	for sequence, frame := range frames {
		if frame.Sequence != uint64(sequence) || frame.RequestID != frames[0].RequestID {
			return zero, ErrInvalidClientState
		}
	}
	var transferID, domainID string
	var epoch, eventCount, eventBytes uint64
	var startAnchor, endAnchor wipdwire.PrefixAnchor
	var declaredManifest string
	if kind == "seed" {
		var start wipdwire.SeedStart
		if frames[0].Kind != "seed.start" || decodeClosedRecord(frames[0].Payload, &start,
			"schema", "transfer_id", "domain_id", "authority_epoch", "store_schema", "snapshot_id", "prefix", "event_count", "event_byte_length", "blob_manifest_digest") != nil ||
			start.Schema != "wipd.seed-start/1" || start.StoreSchema != "wipd.store/1" || !clientULIDPattern.MatchString(start.SnapshotID) {
			return zero, ErrInvalidClientState
		}
		transferID, domainID, epoch = start.TransferID, start.DomainID, start.Epoch
		startAnchor, endAnchor = start.Prefix.Start, start.Prefix.End
		eventCount, eventBytes, declaredManifest = start.EventCount, start.EventByteLength, start.ManifestDigest
	} else {
		var start wipdwire.PullStart
		if frames[0].Kind != "pull.start" || decodeClosedRecord(frames[0].Payload, &start,
			"schema", "transfer_id", "domain_id", "authority_epoch", "prefix", "event_count", "event_byte_length", "blob_manifest_digest") != nil ||
			start.Schema != "wipd.pull-start/1" {
			return zero, ErrInvalidClientState
		}
		transferID, domainID, epoch = start.TransferID, start.DomainID, start.Epoch
		startAnchor, endAnchor = start.Prefix.Start, start.Prefix.End
		eventCount, eventBytes, declaredManifest = start.EventCount, start.EventByteLength, start.ManifestDigest
	}
	if !clientULIDPattern.MatchString(transferID) || domainID != profile.DomainID() || epoch != profile.Epoch() ||
		!validAnchor(startAnchor) || !validAnchor(endAnchor) || !anchorEqual(startAnchor, installed) ||
		eventCount > maxClientTransferEvents || eventBytes > maxClientTransferBytes || eventCount != uint64(len(frames)-3) ||
		endAnchor.EventCount < startAnchor.EventCount || endAnchor.EventCount-startAnchor.EventCount != eventCount ||
		!validDigest(declaredManifest) || len(prior) != int(startAnchor.EventCount) {
		return zero, ErrInvalidClientState
	}
	records := cloneEventRecords(prior)
	var transferredBytes uint64
	for index := uint64(0); index < eventCount; index++ {
		frame := frames[index+1]
		if frame.Kind != "event.record" {
			return zero, ErrInvalidClientState
		}
		var event wipdwire.EventRecord
		if wipdwire.DecodeCanonical(frame.Payload, &event, "event_id", "record") != nil ||
			!clientULIDPattern.MatchString(event.EventID) || len(event.Record) == 0 || len(event.Record) > wipdwire.FrameLimit ||
			uint64(len(event.Record)) > maxClientTransferBytes-transferredBytes {
			return zero, ErrInvalidClientState
		}
		transferredBytes += uint64(len(event.Record))
		records = append(records, event)
	}
	if transferredBytes != eventBytes {
		return zero, ErrInvalidClientState
	}
	manifestIndex := int(eventCount) + 1
	if frames[manifestIndex].Kind != "blob.manifest" {
		return zero, ErrInvalidClientState
	}
	manifest, err := decodeManifest(frames[manifestIndex].Payload)
	if err != nil || manifest.Schema != "wipd.blob-manifest/1" || manifest.DomainID != domainID || manifest.Epoch != epoch ||
		!anchorEqual(manifest.AsOf, endAnchor) || len(manifest.Entries) > maxClientManifestEntries || manifest.Digest != declaredManifest {
		return zero, ErrInvalidClientState
	}
	manifestDigest, err := clientManifestChain(manifest.Entries)
	if err != nil || manifestDigest != manifest.Digest {
		return zero, ErrInvalidClientState
	}
	endIndex := manifestIndex + 1
	if kind == "seed" {
		var end wipdwire.SeedEnd
		if frames[endIndex].Kind != "seed.end" || decodeClosedRecord(frames[endIndex].Payload, &end,
			"schema", "transfer_id", "verified_prefix", "verified_blob_manifest_digest", "complete") != nil ||
			end.Schema != "wipd.seed-end/1" || end.TransferID != transferID || !anchorEqual(end.VerifiedPrefix, endAnchor) ||
			end.ManifestDigest != declaredManifest || !end.Complete {
			return zero, ErrInvalidClientState
		}
	} else {
		var end wipdwire.PullEnd
		if frames[endIndex].Kind != "pull.end" || decodeClosedRecord(frames[endIndex].Payload, &end,
			"transfer_id", "verified_prefix", "verified_blob_manifest_digest", "complete") != nil ||
			end.TransferID != transferID || !anchorEqual(end.VerifiedPrefix, endAnchor) || end.ManifestDigest != declaredManifest || !end.Complete {
			return zero, ErrInvalidClientState
		}
	}
	anchor, projections, stepProjections, err := foldEventRecords(records, domainID)
	if err != nil || !anchorEqual(anchor, endAnchor) {
		return zero, ErrInvalidClientState
	}
	return ClientState{
		Schema: "wipd.m5-client-state/1", RepoID: repoID, DomainID: domainID, Epoch: epoch,
		EnvironmentID: environmentID, OwnerKeyID: profile.OwnerRootSPKI(), SPKIDigest: spkiDigest,
		Prefix: endAnchor, EventRecords: records, ManifestDigest: manifest.Digest,
		ManifestEntries: append([]wipdwire.BlobManifestEntry{}, manifest.Entries...), Projections: projections,
		StepProjections: stepProjections,
	}, nil
}

func decodeManifest(payload []byte) (wipdwire.BlobManifest, error) {
	var result wipdwire.BlobManifest
	fields, err := wipdwire.DecodeCanonicalMap(payload, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest")
	if err != nil {
		return result, ErrInvalidClientState
	}
	anchor, ok := fields["as_of"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(anchor, "event_count", "high_water_event_id", "prefix_digest") {
		return result, ErrInvalidClientState
	}
	entries, ok := fields["entries"].([]any)
	if !ok || len(entries) > maxClientManifestEntries {
		return result, ErrInvalidClientState
	}
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(entry, "digest", "byte_length", "requirement") {
			return result, ErrInvalidClientState
		}
	}
	if wipdwire.DecodeCanonical(payload, &result, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest") != nil {
		return result, ErrInvalidClientState
	}
	return result, nil
}

func foldEventRecords(records []wipdwire.EventRecord, domainID string) (wipdwire.PrefixAnchor, []json.RawMessage, []json.RawMessage, error) {
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	projections := make([]eventProjection, 0, len(records))
	stepProjections := make([]stepProjection, 0, len(records))
	seenIDs := make(map[string]struct{}, len(records))
	seenLocators := make(map[string]struct{}, len(records))
	matterRepos := make(map[string]string)
	stepCounts := make(map[string]int)
	previousEventID := ""
	for _, record := range records {
		if !clientULIDPattern.MatchString(record.EventID) || previousEventID != "" && record.EventID <= previousEventID {
			return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
		}
		fields, err := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
		}
		kind, ok := fields["kind"].(string)
		if !ok {
			return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
		}
		switch kind {
		case "matter.created":
			var event matterCreatedEvent
			if decodeM1Event(record.Record, &event) != nil || event.Schema != "wipd.event/1" || event.EventID != record.EventID ||
				event.DomainID != domainID || !clientULIDPattern.MatchString(event.CommandID) || !validDigest(event.Hash) ||
				!clientULIDPattern.MatchString(event.Environment.ID) || event.Environment.Sequence == 0 ||
				event.SubjectID != event.Payload.ID || !clientULIDPattern.MatchString(event.Payload.ID) || !clientULIDPattern.MatchString(event.RepoID) ||
				event.Payload.Locator == "" || event.Payload.Title == "" || !validUTC(event.ActedAt) || !validUTC(event.OccurredAt) {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			if _, exists := seenIDs[event.Payload.ID]; exists {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			locatorKey := event.RepoID + "\x00" + event.Payload.Locator
			if _, exists := seenLocators[locatorKey]; exists {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			seenIDs[event.Payload.ID] = struct{}{}
			seenLocators[locatorKey] = struct{}{}
			matterRepos[event.Payload.ID] = event.RepoID
			projections = append(projections, eventProjection{
				ID: event.Payload.ID, RepoID: event.RepoID, Locator: event.Payload.Locator,
				Title: event.Payload.Title, BirthEventID: event.EventID,
			})
		case "step.created":
			var event stepCreatedEvent
			if decodeStepEvent(record.Record, &event) != nil || event.Schema != "wipd.event/1" || event.EventID != record.EventID ||
				event.DomainID != domainID || !clientULIDPattern.MatchString(event.CommandID) || !validDigest(event.Hash) ||
				!clientULIDPattern.MatchString(event.Environment.ID) || event.Environment.Sequence == 0 ||
				!clientULIDPattern.MatchString(event.SubjectID) || !clientULIDPattern.MatchString(event.RepoID) ||
				!validUTC(event.ActedAt) || !validUTC(event.OccurredAt) || !clientULIDPattern.MatchString(event.Payload.Parent) ||
				event.Payload.Locator == "" || event.Payload.SortKey <= 0 {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			repo, parentExists := matterRepos[event.Payload.Parent]
			if !parentExists || repo != event.RepoID {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			if _, exists := seenIDs[event.SubjectID]; exists {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			count := stepCounts[event.Payload.Parent] + 1
			if event.Payload.Locator != fmt.Sprintf("step-%02d", count) || event.Payload.SortKey != int64(count)*1000 {
				return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
			}
			stepCounts[event.Payload.Parent] = count
			seenIDs[event.SubjectID] = struct{}{}
			stepProjections = append(stepProjections, stepProjection{
				ID: event.SubjectID, RepoID: event.RepoID, MatterID: event.Payload.Parent,
				Locator: event.Payload.Locator, Title: event.Payload.Title, SortKey: event.Payload.SortKey,
				State: "planned", BirthEventID: event.EventID,
			})
		default:
			return wipdwire.PrefixAnchor{}, nil, nil, ErrInvalidClientState
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record.Record)))
		hash := sha256.New()
		_, _ = hash.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = hash.Write(chain[:])
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(record.Record)
		copy(chain[:], hash.Sum(nil))
		previousEventID = record.EventID
	}
	sort.Slice(projections, func(i, j int) bool {
		if projections[i].Locator != projections[j].Locator {
			return projections[i].Locator < projections[j].Locator
		}
		return projections[i].ID < projections[j].ID
	})
	rawProjections := make([]json.RawMessage, 0, len(projections))
	for _, projection := range projections {
		encoded, err := json.Marshal(projection)
		if err != nil {
			return wipdwire.PrefixAnchor{}, nil, nil, err
		}
		rawProjections = append(rawProjections, encoded)
	}
	sort.Slice(stepProjections, func(i, j int) bool {
		if stepProjections[i].MatterID != stepProjections[j].MatterID {
			return stepProjections[i].MatterID < stepProjections[j].MatterID
		}
		if stepProjections[i].SortKey != stepProjections[j].SortKey {
			return stepProjections[i].SortKey < stepProjections[j].SortKey
		}
		return stepProjections[i].ID < stepProjections[j].ID
	})
	rawStepProjections := make([]json.RawMessage, 0, len(stepProjections))
	for _, projection := range stepProjections {
		encoded, err := json.Marshal(projection)
		if err != nil {
			return wipdwire.PrefixAnchor{}, nil, nil, err
		}
		rawStepProjections = append(rawStepProjections, encoded)
	}
	anchor := wipdwire.PrefixAnchor{EventCount: uint64(len(records)), Digest: "sha256:" + hex.EncodeToString(chain[:])}
	if len(records) > 0 {
		last := records[len(records)-1].EventID
		anchor.EventID = &last
	}
	return anchor, rawProjections, rawStepProjections, nil
}

func decodeM1Event(data []byte, event *matterCreatedEvent) error {
	fields, err := wipdwire.DecodeCanonicalMap(data,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil {
		return ErrInvalidClientState
	}
	for key, expected := range map[string][]string{
		"environment": {"id", "sequence"},
		"payload":     {"id", "locator", "title"},
	} {
		nested, ok := fields[key].(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(nested, expected...) {
			return ErrInvalidClientState
		}
	}
	return wipdwire.DecodeCanonical(data, event,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
}

func decodeStepEvent(data []byte, event *stepCreatedEvent) error {
	fields, err := wipdwire.DecodeCanonicalMap(data,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil {
		return ErrInvalidClientState
	}
	for key, expected := range map[string][]string{
		"environment": {"id", "sequence"},
		"payload":     {"title", "locator", "parent", "sort_key"},
	} {
		nested, ok := fields[key].(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(nested, expected...) {
			return ErrInvalidClientState
		}
	}
	if err = wipdwire.DecodeCanonical(data, event,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload"); err != nil {
		return ErrInvalidClientState
	}
	return nil
}

func clientManifestChain(entries []wipdwire.BlobManifestEntry) (string, error) {
	chain := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	var previous []byte
	for _, entry := range entries {
		if !validDigest(entry.Digest) || entry.ByteLength > maxClientTransferBytes {
			return "", ErrInvalidClientState
		}
		digest, err := hex.DecodeString(entry.Digest[len("sha256:"):])
		if err != nil || len(digest) != sha256.Size || previous != nil && bytes.Compare(previous, digest) >= 0 {
			return "", ErrInvalidClientState
		}
		previous = digest
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], entry.ByteLength)
		var requirement byte
		switch entry.Requirement {
		case "lazy":
		case "pin-before-use":
			requirement = 1
		default:
			return "", ErrInvalidClientState
		}
		hash := sha256.New()
		_, _ = hash.Write([]byte("wipd/blob-manifest-step/v1\x00"))
		_, _ = hash.Write(chain[:])
		_, _ = hash.Write(digest)
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte{requirement})
		copy(chain[:], hash.Sum(nil))
	}
	return "sha256:" + hex.EncodeToString(chain[:]), nil
}

func validateInstalledState(state ClientState, profile wipdauthority.Profile) error {
	if state.Schema != "wipd.m5-client-state/1" || state.DomainID != profile.DomainID() || state.Epoch != profile.Epoch() ||
		profile.M5LabRepoID() != state.RepoID ||
		state.OwnerKeyID != profile.OwnerRootSPKI() || !clientULIDPattern.MatchString(state.RepoID) ||
		!clientULIDPattern.MatchString(state.EnvironmentID) || !validDigest(state.SPKIDigest) || !validAnchor(state.Prefix) ||
		!validDigest(state.ManifestDigest) || state.Projections == nil {
		return ErrInvalidClientState
	}
	anchor, projections, stepProjections, err := foldEventRecords(state.EventRecords, state.DomainID)
	// Step projections are derived entirely from retained event records. Missing
	// data is a pre-Step-8 client-state shape and is rebuilt on the next pull.
	if err != nil || !anchorEqual(anchor, state.Prefix) || !equalJSONRaw(projections, state.Projections) ||
		state.StepProjections != nil && !equalJSONRaw(stepProjections, state.StepProjections) {
		return ErrInvalidClientState
	}
	digest, err := clientManifestChain(state.ManifestEntries)
	if err != nil || digest != state.ManifestDigest {
		return ErrInvalidClientState
	}
	return nil
}

func validateStoredCertificate(state ClientState, key any, now time.Time) (tls.Certificate, error) {
	var result tls.Certificate
	if len(state.CertificateDER) != 2 || key == nil {
		return result, ErrInvalidClientState
	}
	leaf, err := x509.ParseCertificate(state.CertificateDER[0])
	if err != nil {
		return result, ErrInvalidClientState
	}
	ca, err := x509.ParseCertificate(state.CertificateDER[1])
	if err != nil || !bytes.Equal(state.CertificateDER[1], ca.Raw) || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero ||
		ca.CheckSignatureFrom(ca) != nil || leaf.IsCA || leaf.CheckSignatureFrom(ca) != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || !hasCriticalSAN(leaf) {
		return result, ErrInvalidClientState
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return result, ErrInvalidClientState
	}
	publicDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicDER, leaf.RawSubjectPublicKeyInfo) {
		return result, ErrInvalidClientState
	}
	digest := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if "sha256:"+hex.EncodeToString(digest[:]) != state.SPKIDigest || len(leaf.URIs) != 1 || leaf.URIs[0] == nil ||
		leaf.URIs[0].String() != fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", state.EnvironmentID, state.DomainID, state.Epoch, state.OwnerKeyID[len("sha256:"):]) {
		return result, ErrInvalidClientState
	}
	return tls.Certificate{Certificate: clone2D(state.CertificateDER), PrivateKey: key, Leaf: leaf}, nil
}

func loadInstalledState(directory string) (ClientState, []byte, error) {
	var state ClientState
	path := filepath.Join(directory, stateName)
	info, err := os.Lstat(path)
	if err != nil {
		return state, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxClientStateBytes {
		return state, nil, ErrInvalidClientState
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF {
		clear(data)
		return ClientState{}, nil, ErrInvalidClientState
	}
	return state, data, nil
}

func replaceInstalledState(directory string, previous []byte, state ClientState) error {
	lock, err := lockInstalledState(directory)
	if err != nil {
		return err
	}
	err = replaceInstalledStateLocked(directory, previous, state)
	if closeErr := lock.Close(); err == nil {
		err = closeErr
	}
	return err
}

func replaceInstalledStateLocked(directory string, previous []byte, state ClientState) error {
	target := filepath.Join(directory, stateName)
	current, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(current, previous) {
		return ErrStateExists
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > maxClientStateBytes {
		return ErrInvalidClientState
	}
	defer clear(data)
	temp, err := os.CreateTemp(directory, ".client-state-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tempName, target); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func lockInstalledState(directory string) (*os.File, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".client-state.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), path)
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = lock.Close()
		return nil, ErrInvalidClientState
	}
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func validAnchor(anchor wipdwire.PrefixAnchor) bool {
	if !validDigest(anchor.Digest) {
		return false
	}
	if anchor.EventCount == 0 {
		return anchor.EventID == nil && anchor.Digest == emptyPrefixDigest()
	}
	return anchor.EventID != nil && clientULIDPattern.MatchString(*anchor.EventID)
}

func emptyWireAnchor() wipdwire.PrefixAnchor {
	return wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}
}

func anchorEqual(left, right wipdwire.PrefixAnchor) bool {
	if left.EventCount != right.EventCount || left.Digest != right.Digest || (left.EventID == nil) != (right.EventID == nil) {
		return false
	}
	return left.EventID == nil || *left.EventID == *right.EventID
}

func cloneEventRecords(records []wipdwire.EventRecord) []wipdwire.EventRecord {
	result := make([]wipdwire.EventRecord, len(records))
	for index, record := range records {
		result[index] = wipdwire.EventRecord{EventID: record.EventID, Record: bytes.Clone(record.Record)}
	}
	return result
}

func equalJSONRaw(left, right []json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		var leftValue, rightValue any
		if json.Unmarshal(left[index], &leftValue) != nil || json.Unmarshal(right[index], &rightValue) != nil || !reflect.DeepEqual(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func validUTC(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && len(value) > 0 && value[len(value)-1] == 'Z' && parsed.Format(time.RFC3339Nano) == value
}
