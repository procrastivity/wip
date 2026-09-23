package protocol

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const step6VectorsPath = "../../docs/wipd/read-transfer-snapshot-vectors.json"

var (
	step6ULIDPattern   = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	step6DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type step6Fixture struct {
	Notation        string                 `json:"notation"`
	Schemas         step6Schemas           `json:"schemas"`
	Limits          step6Limits            `json:"limits"`
	Events          []step6Event           `json:"events"`
	Anchors         map[string]step6Anchor `json:"anchors"`
	Manifest        step6Manifest          `json:"manifest"`
	ReadSnapshot    step6ReadSnapshot      `json:"read_snapshot"`
	Pagination      step6Pagination        `json:"pagination"`
	Seed            step6Seed              `json:"seed"`
	PullWithPending step6Pull              `json:"pull_with_pending"`
	ReturnRefusal   step6ReturnRefusal     `json:"return_refusal"`
	Blob            step6Blob              `json:"blob"`
	Hydration       step6Hydration         `json:"hydration"`
	Reseed          step6Reseed            `json:"reseed"`
}

type step6Schemas struct {
	Identity string `json:"identity"`
	Receipt  string `json:"receipt"`
	Read     string `json:"read"`
	Manifest string `json:"manifest"`
}

type step6Limits struct {
	DefaultPageSize        uint64 `json:"default_page_size"`
	MaxPageSize            uint64 `json:"max_page_size"`
	DefaultSnapshotSeconds uint64 `json:"default_snapshot_seconds"`
	MaxTokenSeconds        uint64 `json:"max_token_seconds"`
	DefaultStreamBytes     uint64 `json:"default_stream_bytes"`
	AbsoluteStreamBytes    uint64 `json:"absolute_stream_bytes"`
	MaxChunkData           uint64 `json:"max_chunk_data"`
}

type step6Event struct {
	ID           string `json:"id"`
	BytesHex     string `json:"bytes_hex"`
	ByteLength   uint64 `json:"byte_length"`
	PrefixDigest string `json:"prefix_digest"`
}

type step6Anchor struct {
	EventCount       uint64  `json:"event_count"`
	HighWaterEventID *string `json:"high_water_event_id"`
	PrefixDigest     string  `json:"prefix_digest"`
}

type step6Manifest struct {
	Schema         string               `json:"schema"`
	DomainID       string               `json:"domain_id"`
	AuthorityEpoch uint64               `json:"authority_epoch"`
	AsOfAnchor     string               `json:"as_of_anchor"`
	Entries        []step6ManifestEntry `json:"entries"`
	ManifestDigest string               `json:"manifest_digest"`
}

type step6ManifestEntry struct {
	Digest      string `json:"digest"`
	ByteLength  uint64 `json:"byte_length"`
	Requirement string `json:"requirement"`
}

type step6ReadSnapshot struct {
	Name                          string          `json:"name"`
	Query                         step6ReadQuery  `json:"query"`
	Snapshot                      step6Snapshot   `json:"snapshot"`
	Provenance                    step6Provenance `json:"provenance"`
	Items                         []step6ReadItem `json:"items"`
	UnderlyingChangeAfterSnapshot string          `json:"underlying_change_after_snapshot"`
	Expected                      string          `json:"expected"`
}

type step6ReadQuery struct {
	Name          string `json:"name"`
	Version       uint64 `json:"version"`
	Source        string `json:"source"`
	OverlayPolicy string `json:"overlay_policy"`
}

type step6Snapshot struct {
	ID             string `json:"id"`
	DomainID       string `json:"domain_id"`
	AuthorityEpoch uint64 `json:"authority_epoch"`
	AsOfAnchor     string `json:"as_of_anchor"`
	OverlayPolicy  string `json:"overlay_policy"`
	ExpiresAt      string `json:"expires_at"`
}

type step6Provenance struct {
	Source                string  `json:"source"`
	AuthorityReachability string  `json:"authority_reachability"`
	AuthorityAsOfAnchor   *string `json:"authority_as_of_anchor"`
	LocalBaseAsOfAnchor   *string `json:"local_base_as_of_anchor"`
	PendingJournalCount   uint64  `json:"pending_journal_count"`
	ProvisionalItemCount  uint64  `json:"provisional_item_count"`
	QuarantinedItemCount  uint64  `json:"quarantined_item_count"`
	HistoryState          string  `json:"history_state"`
}

type step6ReadItem struct {
	ID                  string  `json:"id"`
	State               string  `json:"state"`
	EnvironmentSequence *uint64 `json:"environment_sequence"`
}

type step6Pagination struct {
	Name                          string               `json:"name"`
	Query                         step6PageQuery       `json:"query"`
	SnapshotID                    string               `json:"snapshot_id"`
	AsOfAnchor                    string               `json:"as_of_anchor"`
	TestHMACKeyHex                string               `json:"test_hmac_key_hex"`
	TokenPayloadHex               string               `json:"token_payload_hex"`
	Token                         string               `json:"token"`
	Claims                        step6TokenClaims     `json:"claims"`
	PageOneIDs                    []string             `json:"page_one_ids"`
	AuthorityStateAfterPageOneIDs []string             `json:"authority_state_after_page_one_ids"`
	PageTwoIDs                    []string             `json:"page_two_ids"`
	ScopeMutations                []step6ScopeMutation `json:"scope_mutations"`
	Expected                      string               `json:"expected"`
}

type step6PageQuery struct {
	Name          string `json:"name"`
	Version       uint64 `json:"version"`
	Source        string `json:"source"`
	OverlayPolicy string `json:"overlay_policy"`
	FilterCBORHex string `json:"filter_cbor_hex"`
	FilterHash    string `json:"filter_hash"`
	PageSize      uint64 `json:"page_size"`
}

type step6TokenClaims struct {
	Schema           string `json:"schema"`
	Issuer           string `json:"issuer"`
	DomainID         string `json:"domain_id"`
	AuthorityEpoch   uint64 `json:"authority_epoch"`
	QueryName        string `json:"query_name"`
	QueryVersion     uint64 `json:"query_version"`
	FilterHash       string `json:"filter_hash"`
	SnapshotID       string `json:"snapshot_id"`
	AsOfEventCount   uint64 `json:"as_of_event_count"`
	AsOfEventID      string `json:"as_of_event_id"`
	AsOfPrefixDigest string `json:"as_of_prefix_digest"`
	OverlayPolicy    string `json:"overlay_policy"`
	PageSize         uint64 `json:"page_size"`
	CursorCBORHex    string `json:"cursor_cbor_hex"`
	ExpiresAt        string `json:"expires_at"`
}

type step6ScopeMutation struct {
	Field        string `json:"field"`
	ExpectedCode string `json:"expected_code"`
}

type step6Seed struct {
	Name            string   `json:"name"`
	MessageOrder    []string `json:"message_order"`
	StartAnchor     string   `json:"start_anchor"`
	EndAnchor       string   `json:"end_anchor"`
	EventCount      uint64   `json:"event_count"`
	EventByteLength uint64   `json:"event_byte_length"`
	ManifestDigest  string   `json:"manifest_digest"`
	Complete        bool     `json:"complete"`
	Install         string   `json:"install"`
}

type step6Pull struct {
	Name                 string             `json:"name"`
	InstalledAnchor      string             `json:"installed_anchor"`
	EligibleJournalHeads []step6JournalHead `json:"eligible_journal_heads"`
	ExpectedOrder        []string           `json:"expected_order"`
	Forbidden            []string           `json:"forbidden"`
}

type step6JournalHead struct {
	JournalID           string `json:"journal_id"`
	Position            uint64 `json:"position"`
	EnvironmentSequence uint64 `json:"environment_sequence"`
	Delivery            string `json:"delivery"`
}

type step6ReturnRefusal struct {
	Name                      string               `json:"name"`
	JournalID                 string               `json:"journal_id"`
	Commands                  []step6ReturnCommand `json:"commands"`
	InstalledReceiptPositions []uint64             `json:"installed_receipt_positions"`
	QuarantinedPositions      []uint64             `json:"quarantined_positions"`
	Blocked                   []string             `json:"blocked"`
	RepairBoundary            step6RepairBoundary  `json:"repair_boundary"`
}

type step6ReturnCommand struct {
	Position            uint64  `json:"position"`
	EnvironmentSequence uint64  `json:"environment_sequence"`
	CommandID           string  `json:"command_id"`
	RequestHash         string  `json:"request_hash"`
	TerminalResult      *string `json:"terminal_result"`
	FoldContinue        bool    `json:"fold_continue"`
}

type step6RepairBoundary struct {
	Owner     string   `json:"owner"`
	Allowed   []string `json:"allowed"`
	Forbidden []string `json:"forbidden"`
}

type step6Blob struct {
	Name       string             `json:"name"`
	ContentHex string             `json:"content_hex"`
	Digest     string             `json:"digest"`
	ByteLength uint64             `json:"byte_length"`
	Attempts   []step6BlobAttempt `json:"attempts"`
	Promotion  string             `json:"promotion"`
}

type step6BlobAttempt struct {
	Name           string `json:"name"`
	DeclaredDigest string `json:"declared_digest"`
	DeclaredLength uint64 `json:"declared_length"`
	BytesAccepted  uint64 `json:"bytes_accepted"`
	ExpectedCode   string `json:"expected_code"`
	VerifiedStaged bool   `json:"verified_staged"`
	OrdinaryCache  bool   `json:"ordinary_cache"`
}

type step6Hydration struct {
	Name                        string                `json:"name"`
	ManifestDigest              string                `json:"manifest_digest"`
	BlobDigest                  string                `json:"blob_digest"`
	ByteLength                  uint64                `json:"byte_length"`
	InitialRequirement          string                `json:"initial_requirement"`
	Ranges                      []step6HydrationRange `json:"ranges"`
	ClaimRequirement            string                `json:"claim_requirement"`
	AfterDurablePinOfflineReady bool                  `json:"after_durable_pin_offline_ready"`
	OutOfRangeExpectedCode      string                `json:"out_of_range_expected_code"`
}

type step6HydrationRange struct {
	Offset          uint64 `json:"offset"`
	Length          uint64 `json:"length"`
	BytesHex        string `json:"bytes_hex"`
	RangeDigest     string `json:"range_digest"`
	CacheStateAfter string `json:"cache_state_after"`
	OfflineReady    bool   `json:"offline_ready"`
}

type step6Reseed struct {
	Name               string        `json:"name"`
	SelectedProtocol   string        `json:"selected_protocol"`
	Signal             string        `json:"signal"`
	OldBaseAnchor      string        `json:"old_base_anchor"`
	RequiredBaseAnchor string        `json:"required_base_anchor"`
	PreservedBefore    step6Evidence `json:"preserved_before"`
	Steps              []string      `json:"steps"`
	PreservedAfter     step6Evidence `json:"preserved_after"`
	NewBaseAnchor      string        `json:"new_base_anchor"`
	Effects            string        `json:"effects"`
}

type step6Evidence struct {
	JournalCommandIDs []string `json:"journal_command_ids"`
	ReceiptCommandIDs []string `json:"receipt_command_ids"`
	StagedBlobDigests []string `json:"staged_blob_digests"`
	LocalEvidenceIDs  []string `json:"local_evidence_ids"`
}

func loadStep6Fixture(t *testing.T) step6Fixture {
	t.Helper()

	f, err := os.Open(step6VectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var got step6Fixture
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("unexpected trailing JSON: %v", err)
	}
	return got
}

func TestStep6PrefixManifestAndSeedAgreement(t *testing.T) {
	fixture := loadStep6Fixture(t)
	if fixture.Notation != "wipd.read-transfer-vector/1" || fixture.Schemas != (step6Schemas{
		Identity: "wipd.command/1", Receipt: "wipd.terminal-receipt/1",
		Read: "wipd.read-response/1", Manifest: "wipd.blob-manifest/1",
	}) {
		t.Fatalf("fixture headers = %#v / %#v", fixture.Notation, fixture.Schemas)
	}
	if fixture.Limits != (step6Limits{100, 1000, 300, 900, 8589934592, 1099511627776, 65536}) {
		t.Fatalf("limits = %#v", fixture.Limits)
	}

	wantAnchorNames := []string{"empty", "one", "three", "two"}
	if got := sortedMapKeys(fixture.Anchors); !reflect.DeepEqual(got, wantAnchorNames) {
		t.Fatalf("anchors = %v", got)
	}
	prefix := domainHash("wipd/event-prefix/v1", nil)
	assertDigest(t, fixture.Anchors["empty"].PrefixDigest, prefix)
	if fixture.Anchors["empty"].EventCount != 0 || fixture.Anchors["empty"].HighWaterEventID != nil {
		t.Fatalf("empty anchor = %#v", fixture.Anchors["empty"])
	}
	var total uint64
	for i, event := range fixture.Events {
		if !step6ULIDPattern.MatchString(event.ID) || (i > 0 && event.ID <= fixture.Events[i-1].ID) {
			t.Fatalf("event order/ID at %d = %q", i, event.ID)
		}
		body := mustDecodeHex(t, event.BytesHex)
		if uint64(len(body)) != event.ByteLength {
			t.Fatalf("event %s length = %d/%d", event.ID, len(body), event.ByteLength)
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], event.ByteLength)
		prefix = domainHash("wipd/event-prefix-step/v1", bytes.Join([][]byte{prefix, length[:], body}, nil))
		assertDigest(t, event.PrefixDigest, prefix)
		anchor := fixture.Anchors[[]string{"one", "two", "three"}[i]]
		if anchor.EventCount != uint64(i+1) || anchor.HighWaterEventID == nil ||
			*anchor.HighWaterEventID != event.ID || anchor.PrefixDigest != event.PrefixDigest {
			t.Fatalf("anchor after %s = %#v", event.ID, anchor)
		}
		total += event.ByteLength
	}

	manifest := fixture.Manifest
	if manifest.Schema != fixture.Schemas.Manifest || !step6ULIDPattern.MatchString(manifest.DomainID) ||
		manifest.AuthorityEpoch == 0 || manifest.AsOfAnchor != "three" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	manifestHash := domainHash("wipd/blob-manifest/v1", nil)
	var prior []byte
	for _, entry := range manifest.Entries {
		digest := digestBytes(t, entry.Digest)
		if prior != nil && bytes.Compare(prior, digest) >= 0 {
			t.Fatalf("manifest digest order is not strictly increasing")
		}
		requirement := byte(0)
		if entry.Requirement == "pin-before-use" {
			requirement = 1
		} else if entry.Requirement != "lazy" {
			t.Fatalf("manifest requirement = %q", entry.Requirement)
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], entry.ByteLength)
		manifestHash = domainHash("wipd/blob-manifest-step/v1",
			bytes.Join([][]byte{manifestHash, digest, length[:], {requirement}}, nil))
		prior = digest
	}
	assertDigest(t, manifest.ManifestDigest, manifestHash)

	seed := fixture.Seed
	if seed.StartAnchor != "empty" || seed.EndAnchor != manifest.AsOfAnchor ||
		seed.EventCount != uint64(len(fixture.Events)) || seed.EventByteLength != total ||
		seed.ManifestDigest != manifest.ManifestDigest || !seed.Complete ||
		seed.Install != "atomic-after-prefix-and-manifest-verification" {
		t.Fatalf("seed/prefix agreement = %#v", seed)
	}
	wantOrder := []string{"seed.start"}
	for _, event := range fixture.Events {
		wantOrder = append(wantOrder, "event:"+event.ID)
	}
	wantOrder = append(wantOrder, "blob.manifest", "seed.end")
	if !reflect.DeepEqual(seed.MessageOrder, wantOrder) {
		t.Fatalf("seed order\n got: %v\nwant: %v", seed.MessageOrder, wantOrder)
	}
}

func TestStep6ReadSnapshotAndPinnedPagination(t *testing.T) {
	fixture := loadStep6Fixture(t)
	read := fixture.ReadSnapshot
	if read.Query.Source != "environment" || read.Query.OverlayPolicy != "folded-and-provisional" ||
		read.Snapshot.AsOfAnchor != "two" || read.Snapshot.OverlayPolicy != read.Query.OverlayPolicy ||
		read.Provenance.AuthorityReachability != "unavailable" || read.Provenance.AuthorityAsOfAnchor != nil ||
		read.Provenance.LocalBaseAsOfAnchor == nil || *read.Provenance.LocalBaseAsOfAnchor != read.Snapshot.AsOfAnchor ||
		read.Provenance.PendingJournalCount != 1 || read.Provenance.ProvisionalItemCount != 1 ||
		read.Provenance.HistoryState != "behind" || read.Expected != "same-items-order-anchor-and-provenance" {
		t.Fatalf("read snapshot/provenance = %#v", read)
	}
	if len(read.Items) != 2 || read.Items[0].State != "folded" || read.Items[0].EnvironmentSequence != nil ||
		read.Items[1].State != "provisional" || read.Items[1].EnvironmentSequence == nil ||
		*read.Items[1].EnvironmentSequence != 50 || read.UnderlyingChangeAfterSnapshot == "" {
		t.Fatalf("read overlay items = %#v", read.Items)
	}

	page := fixture.Pagination
	filterBytes := mustDecodeHex(t, page.Query.FilterCBORHex)
	assertDigest(t, page.Query.FilterHash, domainHash("wipd/query-filter/v1", filterBytes))
	payload := mustDecodeHex(t, page.TokenPayloadHex)
	key := mustDecodeHex(t, page.TestHMACKeyHex)
	parts := strings.Split(page.Token, ".")
	if len(parts) != 3 || parts[0] != "pt1" {
		t.Fatalf("token shape = %q", page.Token)
	}
	if got, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("token payload mismatch: %v", err)
	}
	wantMAC := hmac.New(sha256.New, key)
	_, _ = wantMAC.Write(payload)
	gotMAC, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(gotMAC, wantMAC.Sum(nil)) {
		t.Fatalf("token MAC mismatch: %v", err)
	}

	claims, ok := decodeDeterministicCBOR(t, payload).(map[string]any)
	if !ok {
		t.Fatal("page token claims are not a deterministic CBOR map")
	}
	anchor := fixture.Anchors[page.AsOfAnchor]
	for field, want := range map[string]any{
		"schema": "wipd.page-token/1", "issuer": "authority",
		"domain_id": page.Claims.DomainID, "authority_epoch": page.Claims.AuthorityEpoch,
		"query_name": page.Query.Name, "query_version": page.Query.Version,
		"filter_hash": page.Query.FilterHash, "snapshot_id": page.SnapshotID,
		"as_of_event_count": anchor.EventCount, "as_of_event_id": *anchor.HighWaterEventID,
		"as_of_prefix_digest": anchor.PrefixDigest, "overlay_policy": page.Query.OverlayPolicy,
		"page_size":  page.Query.PageSize,
		"expires_at": page.Claims.ExpiresAt,
	} {
		if !reflect.DeepEqual(claims[field], want) {
			t.Errorf("token claim %s = %#v, want %#v", field, claims[field], want)
		}
	}
	if !bytes.Equal(claims["cursor"].([]byte), mustDecodeHex(t, page.Claims.CursorCBORHex)) {
		t.Fatal("token cursor differs from diagnostic claims")
	}

	combined := append(append([]string(nil), page.PageOneIDs...), page.PageTwoIDs...)
	if !reflect.DeepEqual(combined, page.AuthorityStateAfterPageOneIDs[:3]) ||
		contains(combined, page.AuthorityStateAfterPageOneIDs[3]) ||
		page.Expected != "pages-one-and-two-equal-pinned-three-item-result" {
		t.Fatalf("pagination moved snapshots: combined=%v authority=%v", combined, page.AuthorityStateAfterPageOneIDs)
	}
	wantMutations := map[string]string{
		"domain_id": "query.page-token-scope", "filter_hash": "query.page-token-scope",
		"snapshot_id": "query.page-token-scope", "token_mac": "query.invalid-page-token",
		"expires_at": "query.snapshot-expired",
	}
	for _, mutation := range page.ScopeMutations {
		if wantMutations[mutation.Field] != mutation.ExpectedCode {
			t.Fatalf("token mutation = %#v", mutation)
		}
		delete(wantMutations, mutation.Field)
	}
	if len(wantMutations) != 0 {
		t.Fatalf("missing token mutation vectors: %v", wantMutations)
	}
}

func TestStep6ReturnFoldOrderingAndRefusalBoundary(t *testing.T) {
	fixture := loadStep6Fixture(t)
	pull := fixture.PullWithPending
	if pull.InstalledAnchor != "two" || len(pull.EligibleJournalHeads) != 2 ||
		pull.EligibleJournalHeads[0].EnvironmentSequence >= pull.EligibleJournalHeads[1].EnvironmentSequence ||
		!strings.HasPrefix(pull.ExpectedOrder[0], "return:"+pull.EligibleJournalHeads[0].JournalID) ||
		pull.ExpectedOrder[len(pull.ExpectedOrder)-1] != "admit-new-command" ||
		!contains(pull.Forbidden, "pull-before-eligible-return") ||
		!contains(pull.Forbidden, "parallel-pull-return-write") {
		t.Fatalf("pull/return order = %#v", pull)
	}
	firstPull := indexOf(pull.ExpectedOrder, "pull-complete-prefix-delta")
	secondReturn := indexOfPrefix(pull.ExpectedOrder, "return:"+pull.EligibleJournalHeads[1].JournalID)
	if firstPull < 0 || secondReturn < 0 || firstPull < secondReturn {
		t.Fatalf("pull occurred before all eligible returns: %v", pull.ExpectedOrder)
	}

	refusal := fixture.ReturnRefusal
	if !step6ULIDPattern.MatchString(refusal.JournalID) || len(refusal.Commands) != 3 {
		t.Fatalf("return refusal = %#v", refusal)
	}
	for i, command := range refusal.Commands {
		if command.Position != uint64(i+1) || !step6ULIDPattern.MatchString(command.CommandID) ||
			!step6DigestPattern.MatchString(command.RequestHash) ||
			(i > 0 && command.EnvironmentSequence != refusal.Commands[i-1].EnvironmentSequence+1) {
			t.Fatalf("return command %d = %#v", i, command)
		}
	}
	if result := *refusal.Commands[0].TerminalResult; result != "result.succeeded" || !refusal.Commands[0].FoldContinue {
		t.Fatalf("successful head = %#v", refusal.Commands[0])
	}
	if result := *refusal.Commands[1].TerminalResult; result != "result.refused" || refusal.Commands[1].FoldContinue {
		t.Fatalf("refused head = %#v", refusal.Commands[1])
	}
	if refusal.Commands[2].TerminalResult != nil || refusal.Commands[2].FoldContinue ||
		!reflect.DeepEqual(refusal.InstalledReceiptPositions, []uint64{1, 2}) ||
		!reflect.DeepEqual(refusal.QuarantinedPositions, []uint64{2, 3}) ||
		refusal.RepairBoundary.Owner != "step-7" ||
		!contains(refusal.RepairBoundary.Allowed, "replacement-new-command-id-and-hash") ||
		!contains(refusal.RepairBoundary.Forbidden, "skip-to-position-3") ||
		!contains(refusal.Blocked, "pull") {
		t.Fatalf("quarantine/repair boundary = %#v", refusal)
	}
}

func TestStep6BlobUploadHydrationAndReseed(t *testing.T) {
	fixture := loadStep6Fixture(t)
	blob := fixture.Blob
	content := mustDecodeHex(t, blob.ContentHex)
	if uint64(len(content)) != blob.ByteLength {
		t.Fatalf("blob length = %d/%d", len(content), blob.ByteLength)
	}
	contentHash := sha256.Sum256(content)
	assertDigest(t, blob.Digest, contentHash[:])
	attempts := make(map[string]step6BlobAttempt, len(blob.Attempts))
	for _, attempt := range blob.Attempts {
		if _, exists := attempts[attempt.Name]; exists {
			t.Fatalf("duplicate blob attempt %q", attempt.Name)
		}
		attempts[attempt.Name] = attempt
		if attempt.OrdinaryCache {
			t.Fatalf("upload attempt promoted without terminal success: %#v", attempt)
		}
	}
	if got := sortedMapKeys(attempts); !reflect.DeepEqual(got, []string{"bad-hash", "bad-length", "duplicate-upload", "verified-upload"}) {
		t.Fatalf("blob attempts = %v", got)
	}
	verified := attempts["verified-upload"]
	duplicate := attempts["duplicate-upload"]
	badHash := attempts["bad-hash"]
	badLength := attempts["bad-length"]
	if verified.ExpectedCode != "blob.staged" || !verified.VerifiedStaged || verified.BytesAccepted != blob.ByteLength ||
		duplicate.ExpectedCode != "blob.available" || !duplicate.VerifiedStaged || duplicate.BytesAccepted != 0 ||
		badHash.ExpectedCode != "blob.digest-mismatch" || badHash.VerifiedStaged || badHash.DeclaredDigest == blob.Digest ||
		badLength.ExpectedCode != "blob.length-mismatch" || badLength.VerifiedStaged || badLength.DeclaredLength == blob.ByteLength ||
		blob.Promotion != "only-successful-terminal-command-transaction" {
		t.Fatalf("blob outcomes = %#v", attempts)
	}

	hydration := fixture.Hydration
	if hydration.InitialRequirement != "lazy" || hydration.ClaimRequirement != "pin-before-use" ||
		len(hydration.Ranges) != 2 || hydration.OutOfRangeExpectedCode != "blob.range-invalid" {
		t.Fatalf("hydration headers = %#v", hydration)
	}
	assembled := make([]byte, 0, hydration.ByteLength)
	var next uint64
	for i, byteRange := range hydration.Ranges {
		part := mustDecodeHex(t, byteRange.BytesHex)
		if byteRange.Offset != next || uint64(len(part)) != byteRange.Length {
			t.Fatalf("range %d is noncontiguous or wrong length: %#v", i, byteRange)
		}
		rangeHash := sha256.Sum256(part)
		assertDigest(t, byteRange.RangeDigest, rangeHash[:])
		assembled = append(assembled, part...)
		next += byteRange.Length
	}
	assembledHash := sha256.Sum256(assembled)
	assertDigest(t, hydration.BlobDigest, assembledHash[:])
	if next != hydration.ByteLength || hydration.Ranges[0].CacheStateAfter != "hydrating" ||
		hydration.Ranges[0].OfflineReady || hydration.Ranges[1].CacheStateAfter != "verified-complete" ||
		hydration.Ranges[1].OfflineReady || !hydration.AfterDurablePinOfflineReady {
		t.Fatalf("hydration readiness = %#v", hydration)
	}

	reseed := fixture.Reseed
	if reseed.SelectedProtocol != "1.2" || reseed.Signal != "store.reseed-required" ||
		reseed.OldBaseAnchor == reseed.RequiredBaseAnchor || reseed.NewBaseAnchor != reseed.RequiredBaseAnchor ||
		!reflect.DeepEqual(reseed.PreservedBefore, reseed.PreservedAfter) ||
		!reflect.DeepEqual(reseed.Steps, []string{
			"freeze-domain-lane", "inventory-preserved-evidence", "seed-shadow-base-from-genesis",
			"verify-prefix-and-manifest", "revalidate-receipts-journals-and-staged-blobs",
			"atomic-disposable-base-swap", "reattach-unchanged-evidence", "rebuild-explicit-overlay",
		}) || !strings.Contains(reseed.Effects, "no-auto-submit") {
		t.Fatalf("reseed boundary = %#v", reseed)
	}
}

func TestStep6ContractMapsDecisionsQuestionsAndBoundaries(t *testing.T) {
	contract, err := os.ReadFile("../../docs/wipd/read-transfer-snapshot-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"wipd.command/1", "wipd.terminal-receipt/1", "PrefixAnchor", "BlobManifest",
		"ReadResponse", "PageTokenClaims", "SeedRequest", "PullRequest", "ReturnCommand",
		"FoldResult", "BlobUploadStart", "BlobRead", "store.reseed-required",
		"D119", "D122", "D123", "D124", "D125", "D128", "D129", "D130", "D131",
		"Q09", "Q10", "Q11", "Q12", "Q13", "Q14", "Q15", "Q16", "Q22", "Q24", "Q25",
		"There are no unresolved Step 6 decisions",
	} {
		if !bytes.Contains(contract, []byte(token)) {
			t.Errorf("contract does not map %q", token)
		}
	}
	for _, forbidden := range []string{"CREATE TABLE", "net.Listen", "cobra.Command"} {
		if bytes.Contains(contract, []byte(forbidden)) {
			t.Errorf("contract crosses implementation boundary with %q", forbidden)
		}
	}
}

func domainHash(domain string, data []byte) []byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hash.Sum(nil)
}

func digestBytes(t *testing.T, digest string) []byte {
	t.Helper()
	if !step6DigestPattern.MatchString(digest) {
		t.Fatalf("noncanonical digest %q", digest)
	}
	return mustDecodeHex(t, strings.TrimPrefix(digest, "sha256:"))
}

func assertDigest(t *testing.T, got string, want []byte) {
	t.Helper()
	wantText := "sha256:" + hex.EncodeToString(want)
	if got != wantText {
		t.Fatalf("digest = %s, want %s", got, wantText)
	}
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func indexOfPrefix(values []string, prefix string) int {
	for i, value := range values {
		if strings.HasPrefix(value, prefix) {
			return i
		}
	}
	return -1
}
