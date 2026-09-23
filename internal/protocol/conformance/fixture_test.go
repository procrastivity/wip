package conformance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	fixturePath = "../../../docs/wipd/conformance-vectors.json"
	schemaPath  = "../../../docs/wipd/conformance-schemas.cddl"
)

type conformanceFixture struct {
	Notation         string              `json:"notation"`
	ProtocolSchema   string              `json:"protocol_schema"`
	SourceVectors    []sourceVector      `json:"source_vectors"`
	CanonicalVectors []canonicalVector   `json:"canonical_vectors"`
	AgreementVectors []agreementVector   `json:"agreement_vectors"`
	MigrationVector  migrationVector     `json:"migration_vector"`
	Exits            []exitVector        `json:"exits"`
	DecisionCoverage map[string][]string `json:"decision_coverage"`
	QuestionCoverage map[string][]string `json:"question_coverage"`
	DeferredStep9    []string            `json:"deferred_step9"`
	Step9Seal        step9Seal           `json:"step9_seal"`
}

type sourceVector struct {
	Path     string `json:"path"`
	Notation string `json:"notation"`
	SHA256   string `json:"sha256"`
}

type canonicalVector struct {
	Name           string `json:"name"`
	Source         string `json:"source"`
	Schema         string `json:"schema"`
	RequestHash    string `json:"request_hash"`
	BodySHA256     string `json:"body_sha256"`
	ArtifactDigest string `json:"artifact_digest"`
}

type step9Seal struct {
	Status             string   `json:"status"`
	Review             string   `json:"review"`
	ReviewSHA256       string   `json:"review_sha256"`
	Vectors            string   `json:"vectors"`
	UnresolvedFindings []string `json:"unresolved_findings"`
}

type agreementVector struct {
	Name     string   `json:"name"`
	Family   string   `json:"family"`
	Script   []action `json:"script"`
	Expected []string `json:"expected"`
}

type action struct {
	Op          string   `json:"op"`
	ID          string   `json:"id"`
	Hash        string   `json:"hash"`
	Result      string   `json:"result"`
	Items       []string `json:"items"`
	Content     string   `json:"content"`
	Length      uint64   `json:"length"`
	Epoch       uint64   `json:"epoch"`
	Offset      uint64   `json:"offset"`
	ClientMajor uint64   `json:"client_major"`
	ServerMajor uint64   `json:"server_major"`
	Requested   uint64   `json:"requested"`
	Supported   uint64   `json:"supported"`
	Preserve    []string `json:"preserve"`
}

type migrationVector struct {
	Name                  string `json:"name"`
	CorrelationOrigin     string `json:"correlation_origin"`
	CommandID             string `json:"command_id"`
	EnvironmentSequence   uint64 `json:"environment_sequence"`
	CanonicalCBORHex      string `json:"canonical_cbor_hex"`
	RequestHash           string `json:"request_hash"`
	RollbackAfterNewWrite string `json:"rollback_after_new_write"`
}

type exitVector struct {
	ID          string   `json:"id"`
	Family      string   `json:"family"`
	Title       string   `json:"title"`
	Sources     []string `json:"sources"`
	Assertions  []string `json:"assertions"`
	Step9Review []string `json:"step9_review"`
}

func loadConformanceFixture(t testing.TB) conformanceFixture {
	t.Helper()

	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var got conformanceFixture
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode conformance vectors: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("unexpected trailing JSON: %v", err)
	}
	return got
}

func TestPublishedPackageIsCompleteAndSelfConsistent(t *testing.T) {
	fixture := loadConformanceFixture(t)
	if fixture.Notation != "wipd.conformance-vector/1" ||
		fixture.ProtocolSchema != "docs/wipd/conformance-schemas.cddl" {
		t.Fatalf("package header = %q / %q", fixture.Notation, fixture.ProtocolSchema)
	}

	if len(fixture.SourceVectors) != 5 {
		t.Fatalf("source vector count = %d, want 5", len(fixture.SourceVectors))
	}
	for _, source := range fixture.SourceVectors {
		path := filepath.Join("../../..", source.Path)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", source.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != source.SHA256 {
			t.Errorf("%s digest = %s, want %s", source.Path, got, source.SHA256)
		}
		var header struct {
			Notation string `json:"notation"`
		}
		if err := json.Unmarshal(data, &header); err != nil || header.Notation != source.Notation {
			t.Errorf("%s notation = %q, want %q (decode error %v)", source.Path, header.Notation, source.Notation, err)
		}
	}

	wantFamilies := []string{
		"accepted command", "blob upload", "claim cycle", "incompatible versions/reseed",
		"lost-response receipt query", "read snapshot", "refusal", "same-ID retry",
	}
	gotFamilies := make([]string, 0, len(fixture.AgreementVectors))
	seenAgreement := make(map[string]struct{}, len(fixture.AgreementVectors))
	for _, vector := range fixture.AgreementVectors {
		if _, duplicate := seenAgreement[vector.Family]; duplicate {
			t.Errorf("duplicate agreement family %q", vector.Family)
		}
		seenAgreement[vector.Family] = struct{}{}
		gotFamilies = append(gotFamilies, vector.Family)
		if len(vector.Script) == 0 || len(vector.Expected) != len(vector.Script) {
			t.Errorf("agreement vector %q has %d actions and %d outcomes", vector.Name, len(vector.Script), len(vector.Expected))
		}
	}
	sort.Strings(gotFamilies)
	if !reflect.DeepEqual(gotFamilies, wantFamilies) {
		t.Fatalf("agreement families\n got: %v\nwant: %v", gotFamilies, wantFamilies)
	}

	if len(fixture.Exits) != 22 {
		t.Fatalf("exit count = %d, want 22", len(fixture.Exits))
	}
	for i, exit := range fixture.Exits {
		wantID := fmt.Sprintf("V%02d", i+1)
		if exit.ID != wantID {
			t.Errorf("exit %d ID = %q, want %q", i, exit.ID, wantID)
		}
		if exit.Family == "" || exit.Title == "" || len(exit.Sources) == 0 || len(exit.Assertions) == 0 {
			t.Errorf("exit %s is incomplete: %#v", exit.ID, exit)
		}
		if len(exit.Step9Review) != 0 {
			t.Errorf("exit %s still has Step 9 deferrals: %v", exit.ID, exit.Step9Review)
		}
	}

	assertCoverageRange(t, fixture.DecisionCoverage, "D", 115, 131)
	assertCoverageRange(t, fixture.QuestionCoverage, "Q", 1, 25)
	if len(fixture.DeferredStep9) != 0 {
		t.Fatalf("Step 9 review still has deferrals: %v", fixture.DeferredStep9)
	}
	if fixture.Step9Seal.Status != "sealed-for-m3-m4" ||
		fixture.Step9Seal.Review != "docs/wipd/design-security-privacy-review.md" ||
		fixture.Step9Seal.Vectors != "docs/wipd/design-security-privacy-vectors.json" ||
		len(fixture.Step9Seal.UnresolvedFindings) != 0 {
		t.Fatalf("Step 9 seal = %#v", fixture.Step9Seal)
	}
	review, err := os.ReadFile(filepath.Join("../../..", fixture.Step9Seal.Review))
	if err != nil {
		t.Fatal(err)
	}
	reviewDigest := sha256.Sum256(review)
	if got := hex.EncodeToString(reviewDigest[:]); got != fixture.Step9Seal.ReviewSHA256 {
		t.Fatalf("Step 9 review digest = %s, want %s", got, fixture.Step9Seal.ReviewSHA256)
	}
}

func TestPublishedCDDLNamesEveryConformanceSchema(t *testing.T) {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range []string{
		"frame = {", "client-hello = {", "server-hello = {", "command = {",
		"command-submit = {", "query-request = {", "claim-acquire-input = {",
		"claim-journal-repair-input = {", "claim-release-input = {", "claim-stand-down-input = {",
		"terminal-receipt = {", "receipt-query = {", "prefix-anchor = {",
		"blob-manifest = {", "blob-upload-start = {", "read-query = {",
		"read-response = {", "page-token-claims = {", "seed-request = {",
		"seed-start = {", "seed-end = {", "pull-request = {", "return-command = {",
		"fold-result = {", "reseed-required = {",
		"claim-acquire = {", "claim-grant-start = {", "claim-grant-end = {",
		"journal-barrier = {", "claim-release = {", "claim-stand-down = {",
		"matter-create-v1-output = {", "cursor-move-output = {", "signed-artifact = {",
		"authority-artifact-key = {", "owner-attestation = {", "enrollment-grant = {",
		"bundle-manifest = {", "authority-activation = {", "legacy-event = {",
		"migration-command = {", "migration-proof = {",
	} {
		if !strings.Contains(string(data), declaration) {
			t.Errorf("CDDL does not declare %q", declaration)
		}
	}
}

func TestDeterministicMigrationVector(t *testing.T) {
	vector := loadConformanceFixture(t).MigrationVector
	if vector.CommandID != vector.CorrelationOrigin || vector.EnvironmentSequence != 1 ||
		vector.RollbackAfterNewWrite != "migration.rollback-forbidden" {
		t.Fatalf("migration identity/rollback = %#v", vector)
	}
	canonical, err := hex.DecodeString(vector.CanonicalCBORHex)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), canonical...))
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != vector.RequestHash {
		t.Fatalf("migration request hash = %s, want %s", got, vector.RequestHash)
	}
}

func TestPublishedCanonicalProductDigests(t *testing.T) {
	fixture := loadConformanceFixture(t)
	want := make(map[string]canonicalVector, len(fixture.CanonicalVectors))
	for _, vector := range fixture.CanonicalVectors {
		want[vector.Name] = vector
	}

	frameData, err := os.ReadFile("../../../docs/wipd/frame-security-execution-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var frameSource struct {
		Frames []struct {
			Body string `json:"frame_body_hex"`
		} `json:"frame_vectors"`
	}
	if err := json.Unmarshal(frameData, &frameSource); err != nil || len(frameSource.Frames) != 1 {
		t.Fatalf("decode frame canonical product: %v", err)
	}
	assertHexDigest(t, "client-hello-frame", frameSource.Frames[0].Body, want["client-hello-frame"].BodySHA256)

	readData, err := os.ReadFile("../../../docs/wipd/read-transfer-snapshot-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var readSource struct {
		Pagination struct {
			Payload string `json:"token_payload_hex"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(readData, &readSource); err != nil {
		t.Fatalf("decode page-token canonical product: %v", err)
	}
	assertHexDigest(t, "pinned-page-token", readSource.Pagination.Payload, want["pinned-page-token"].BodySHA256)

	signed := loadStep9Fixture(t).PortableSignature
	if product, ok := want["portable-receipt-signature"]; !ok ||
		product.Schema != "wipd.signed-artifact/1" ||
		product.Source != "docs/wipd/design-security-privacy-vectors.json#/portable_signature" ||
		product.ArtifactDigest != signed.ArtifactDigest {
		t.Fatalf("portable receipt canonical product not bound to reviewed vector: %#v", product)
	}
}

func TestNegativeSecurityAndRecoveryCorpusIsExplicit(t *testing.T) {
	fixture := loadConformanceFixture(t)
	allNames := make(map[string]struct{})
	for _, source := range fixture.SourceVectors {
		data, err := os.ReadFile(filepath.Join("../../..", source.Path))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		collectNames(value, allNames)
	}
	for _, name := range []string{
		"local-peer-owner-mismatch", "remote-authority-pin-mismatch",
		"remote-environment-domain-mismatch", "remote-environment-revoked-on-retained-connection",
		"enrollment-grant-reuse-with-different-key", "application-signature-cannot-replace-mtls",
		"fragmented-length-prefix", "zero-length-frame", "oversized-frame-before-allocation",
		"oversized-chunk", "noncontiguous-stream-offset", "declared-stream-over-limit",
		"bounded-backpressure", "concurrency-overload", "cancel-wins-before-submission",
		"cancel-after-submission", "disconnect-after-bytes-before-ack", "query-cancel",
		"malformed-command", "same-id-different-hash-refusal", "definitely-unsent-unavailable",
		"lost-response-receipt-query", "dependent-blocked-on-outcome-unknown",
		"authority-pages-remain-pinned-after-new-event", "refused-head-stops-and-quarantines-dependent-suffix",
		"upload-idempotency-and-integrity", "compatible-protocol-replaces-disposable-base-only",
		"contended-matter", "cross-matter-footprint", "batch-aggregate-footprint",
		"wrong-claim-id", "wrong-claim-epoch", "wrong-holder", "missing-terminal-receipt",
		"loss-not-acknowledged", "active-claim-blocks-handoff-and-old-epoch-is-fenced-after-promotion",
	} {
		if _, ok := allNames[name]; !ok {
			t.Errorf("negative/security/recovery corpus does not name %q", name)
		}
	}
}

func assertHexDigest(t *testing.T, name, value, want string) {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("%s hex: %v", name, err)
	}
	digest := sha256.Sum256(decoded)
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("%s digest = %s, want %s", name, got, want)
	}
}

func collectNames(value any, names map[string]struct{}) {
	switch value := value.(type) {
	case map[string]any:
		if name, ok := value["name"].(string); ok {
			names[name] = struct{}{}
		}
		for _, child := range value {
			collectNames(child, names)
		}
	case []any:
		for _, child := range value {
			collectNames(child, names)
		}
	}
}

func assertCoverageRange(t *testing.T, coverage map[string][]string, prefix string, first, last int) {
	t.Helper()
	if len(coverage) != last-first+1 {
		t.Errorf("%s coverage count = %d, want %d", prefix, len(coverage), last-first+1)
	}
	for value := first; value <= last; value++ {
		width := 0
		if prefix == "Q" {
			width = 2
		}
		key := fmt.Sprintf("%s%0*d", prefix, width, value)
		if len(coverage[key]) == 0 {
			t.Errorf("missing or empty coverage for %s", key)
		}
	}
}
