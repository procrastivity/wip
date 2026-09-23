package conformance_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const step9FixturePath = "../../../docs/wipd/design-security-privacy-vectors.json"

type step9Fixture struct {
	Notation            string                 `json:"notation"`
	Review              string                 `json:"review"`
	PortableSignature   step9PortableSignature `json:"portable_signature"`
	OwnerCeremonies     []step9Ceremony        `json:"owner_ceremonies"`
	CertificateSecurity step9Certificate       `json:"certificate_security"`
	D127                step9D127              `json:"d127"`
	Privacy             step9Privacy           `json:"privacy"`
	Migration           step9Migration         `json:"migration"`
	Implementability    step9Implementability  `json:"implementability"`
}

type step9PortableSignature struct {
	Name                    string                   `json:"name"`
	ReceiptSource           string                   `json:"receipt_source"`
	DomainSeparator         string                   `json:"domain_separator"`
	ArtifactDigestSeparator string                   `json:"artifact_digest_separator"`
	PublicKeyHex            string                   `json:"public_key_hex"`
	SignerKeyID             string                   `json:"signer_key_id"`
	Envelope                step9SignatureEnvelope   `json:"envelope"`
	PayloadDigest           string                   `json:"payload_digest"`
	SignatureHex            string                   `json:"signature_hex"`
	ArtifactDigest          string                   `json:"artifact_digest"`
	NegativeCases           []step9SignatureNegative `json:"negative_cases"`
}

type step9SignatureEnvelope struct {
	Schema                 string `json:"schema"`
	Kind                   string `json:"kind"`
	DomainID               string `json:"domain_id"`
	AuthorityEpoch         uint64 `json:"authority_epoch"`
	SignerRole             string `json:"signer_role"`
	KeyGeneration          uint64 `json:"key_generation"`
	ArtifactSequence       uint64 `json:"artifact_sequence"`
	PreviousArtifactDigest string `json:"previous_artifact_digest"`
	IssuedAt               string `json:"issued_at"`
	PayloadSchema          string `json:"payload_schema"`
}

type step9SignatureNegative struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
}

type step9Ceremony struct {
	Name         string   `json:"name"`
	Required     []string `json:"required"`
	LossAccepted bool     `json:"loss_accepted"`
	Negative     string   `json:"negative"`
}

type step9Certificate struct {
	AuthorityURI   string   `json:"authority_uri"`
	EnvironmentURI string   `json:"environment_uri"`
	Required       []string `json:"required"`
	Refused        []string `json:"refused"`
	DelegatedCA    struct {
		OwnerAnchor      string   `json:"owner_anchor"`
		DelegationSchema string   `json:"delegation_schema"`
		FenceSchema      string   `json:"fence_schema"`
		TrustAnchor      string   `json:"trust_anchor"`
		ChainOrder       []string `json:"chain_order"`
		MaxLeafHours     int      `json:"max_leaf_hours"`
		ClockSkewSeconds int      `json:"clock_skew_seconds"`
		Refused          []string `json:"refused"`
	} `json:"delegated_ca"`
}

type step9D127 struct {
	LocatorCollision        step9LocatorCollision `json:"locator_collision"`
	CursorProvisionalTarget step9CursorTarget     `json:"cursor_provisional_target"`
	CursorNoop              step9CursorNoop       `json:"cursor_noop"`
}

type step9LocatorCollision struct {
	Operation string   `json:"operation"`
	Requested string   `json:"requested"`
	Identity  string   `json:"identity"`
	Assigned  string   `json:"assigned"`
	Events    []string `json:"events"`
	Output    struct {
		ID                    string `json:"id"`
		Title                 string `json:"title"`
		RequestedLocator      string `json:"requested_locator"`
		AssignedLocator       string `json:"assigned_locator"`
		LocatorRepairRequired bool   `json:"locator_repair_required"`
	} `json:"output"`
	RepairRequired bool     `json:"repair_required"`
	ClearOnly      []string `json:"clear_only"`
}

type step9CursorTarget struct {
	Operation           string `json:"operation"`
	Delivery            string `json:"delivery"`
	OrderingKey         string `json:"ordering_key"`
	EnvironmentSequence uint64 `json:"environment_sequence"`
	Target              string `json:"target"`
	TargetBirthSequence uint64 `json:"target_birth_sequence"`
	Causation           string `json:"causation"`
	Eligibility         string `json:"eligibility"`
	BirthRefusal        string `json:"birth_refusal"`
}

type step9CursorNoop struct {
	Operation        string         `json:"operation"`
	CanonicalCommand map[string]any `json:"canonical_command"`
	Output           map[string]any `json:"output"`
	AcceptedEvents   any            `json:"accepted_events"`
	Effects          string         `json:"effects"`
}

type step9Privacy struct {
	RoutineLogDays                uint64   `json:"routine_log_days"`
	SecurityAuditDays             uint64   `json:"security_audit_days"`
	RestrictedHashDays            uint64   `json:"restricted_hash_days"`
	DomainLifetime                []string `json:"domain_lifetime"`
	CompactableAfterTerminalAudit []string `json:"compactable_after_terminal_audit"`
	RetainUntilResolution         []string `json:"retain_until_resolution"`
	NeverLog                      []string `json:"never_log"`
}

type step9Migration struct {
	Name                               string                `json:"name"`
	CouplingSources                    []string              `json:"coupling_sources"`
	UnassignableFact                   string                `json:"unassignable_fact"`
	ComponentRule                      string                `json:"component_rule"`
	Group                              step9MigrationGroup   `json:"group"`
	SyntheticBinding                   step9SyntheticBinding `json:"synthetic_binding"`
	ProofBinds                         []string              `json:"proof_binds"`
	RollbackAfterDestinationSubmission string                `json:"rollback_after_destination_submission"`
}

type step9MigrationGroup struct {
	CorrelationOrigin   string `json:"correlation_origin"`
	CommandID           string `json:"command_id"`
	EnvironmentID       string `json:"environment_id"`
	EnvironmentSequence uint64 `json:"environment_sequence"`
	ActedAt             string `json:"acted_at"`
	Sort                string `json:"sort"`
}

type step9SyntheticBinding struct {
	Schema   string `json:"schema"`
	Actor    string `json:"actor"`
	Event    string `json:"event"`
	Position string `json:"position"`
}

type step9Implementability struct {
	M3                           []string `json:"m3"`
	M4                           []string `json:"m4"`
	ForbiddenStep9Implementation []string `json:"forbidden_step9_implementation"`
}

func loadStep9Fixture(t *testing.T) step9Fixture {
	t.Helper()
	f, err := os.Open(step9FixturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var fixture step9Fixture
	decoder := json.NewDecoder(f)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode Step 9 vectors: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("unexpected trailing Step 9 JSON: %v", err)
	}
	return fixture
}

func TestPortableArtifactGoldenSignature(t *testing.T) {
	vector := loadStep9Fixture(t).PortableSignature
	if vector.DomainSeparator != "wipd/signed-artifact/v1" ||
		vector.ArtifactDigestSeparator != "wipd/artifact-digest/v1" ||
		vector.ReceiptSource != "docs/wipd/result-receipt-idempotency-vectors.json#/receipts/accepted" {
		t.Fatalf("portable signature domains/source = %#v", vector)
	}

	payload := loadAcceptedReceiptCanonical(t)
	payloadDigest := sha256.Sum256(payload)
	if got := "sha256:" + hex.EncodeToString(payloadDigest[:]); got != vector.PayloadDigest {
		t.Fatalf("portable payload digest = %s, want %s", got, vector.PayloadDigest)
	}
	publicKey := mustHex(t, vector.PublicKeyHex)
	if len(publicKey) != ed25519.PublicKeySize {
		t.Fatalf("Ed25519 public key length = %d", len(publicKey))
	}
	spki, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(spki)
	if got := "sha256:" + hex.EncodeToString(keyDigest[:]); got != vector.SignerKeyID {
		t.Fatalf("signer key ID = %s, want %s", got, vector.SignerKeyID)
	}

	unsigned := unsignedArtifactMap(vector, payload)
	clientBytes, err := clientCanonical(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	serverBytes, err := serverCanonical(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clientBytes, serverBytes) {
		t.Fatal("independent canonical encoders disagree on signed artifact")
	}
	preimage := append([]byte(vector.DomainSeparator+"\x00"), clientBytes...)
	signature := mustHex(t, vector.SignatureHex)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), preimage, signature) {
		t.Fatal("portable receipt golden signature did not verify")
	}

	complete := cloneMap(unsigned)
	complete["signature"] = signature
	completeBytes, err := serverCanonical(complete)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte(vector.ArtifactDigestSeparator+"\x00"), completeBytes...))
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != vector.ArtifactDigest {
		t.Fatalf("artifact digest = %s, want %s", got, vector.ArtifactDigest)
	}

	for _, mutate := range []func(map[string]any){
		func(value map[string]any) { value["payload"] = append([]byte(nil), payload[:len(payload)-1]...) },
		func(value map[string]any) { value["domain_id"] = "01K6B000000000000000000001" },
		func(value map[string]any) { value["signer_role"] = "owner" },
	} {
		changed := cloneMap(unsigned)
		mutate(changed)
		changedBytes, err := clientCanonical(changed)
		if err != nil {
			t.Fatal(err)
		}
		if ed25519.Verify(ed25519.PublicKey(publicKey), append([]byte(vector.DomainSeparator+"\x00"), changedBytes...), signature) {
			t.Fatal("signature accepted a covered-field mutation")
		}
	}
	if got := signatureNegativeNames(vector.NegativeCases); !reflect.DeepEqual(got,
		[]string{"domain-changed", "parallel-chain-after-fence", "payload-byte-changed", "wrong-signer-role"}) {
		t.Fatalf("portable signature negative cases = %v", got)
	}
}

func TestStep9ReviewVectorsCloseSecurityPrivacyAndImplementability(t *testing.T) {
	fixture := loadStep9Fixture(t)
	if fixture.Notation != "wipd.design-security-privacy-vector/1" ||
		fixture.Review != "docs/wipd/design-security-privacy-review.md" {
		t.Fatalf("Step 9 fixture header = %q / %q", fixture.Notation, fixture.Review)
	}

	ceremonies := make(map[string]step9Ceremony, len(fixture.OwnerCeremonies))
	for _, ceremony := range fixture.OwnerCeremonies {
		ceremonies[ceremony.Name] = ceremony
		if len(ceremony.Required) < 6 || ceremony.Negative == "" {
			t.Errorf("incomplete owner ceremony %#v", ceremony)
		}
	}
	if len(ceremonies) != 3 || ceremonies["planned-handoff"].LossAccepted ||
		!ceremonies["disaster-restore"].LossAccepted || !ceremonies["cross-environment-stand-down"].LossAccepted {
		t.Fatalf("owner ceremony loss boundary = %#v", ceremonies)
	}

	cert := fixture.CertificateSecurity
	if !strings.HasPrefix(cert.AuthorityURI, "wipd://authority/") ||
		!strings.HasPrefix(cert.EnvironmentURI, "wipd://environment/") ||
		!slices.Contains(cert.Required, "critical-standard-san") ||
		!slices.Contains(cert.Required, "revocation-each-exchange") ||
		!slices.Contains(cert.Refused, "grant-reuse-with-different-csr") {
		t.Fatalf("certificate security vector = %#v", cert)
	}
	if cert.DelegatedCA.OwnerAnchor != "raw-ed25519-der-spki" ||
		cert.DelegatedCA.DelegationSchema != "wipd.environment-ca-delegation/1" ||
		cert.DelegatedCA.FenceSchema != "wipd.environment-ca-fence/1" ||
		cert.DelegatedCA.TrustAnchor != "owner-signed-self-signed-ed25519-ca" ||
		!reflect.DeepEqual(cert.DelegatedCA.ChainOrder, []string{"environment-leaf", "delegated-ca"}) ||
		cert.DelegatedCA.MaxLeafHours != 24 || cert.DelegatedCA.ClockSkewSeconds != 0 ||
		!slices.Contains(cert.DelegatedCA.Refused, "revoked-ca-on-retained-connection") {
		t.Fatalf("delegated CA vector = %#v", cert.DelegatedCA)
	}

	collision := fixture.D127.LocatorCollision
	if collision.Operation != "matter.create@v2" || collision.Requested != "alpha" ||
		collision.Assigned != "alpha-01K6M0" ||
		strings.Contains(collision.Assigned, "--") || !collision.RepairRequired ||
		!reflect.DeepEqual(collision.Events, []string{"matter.created", "matter.locator-repair-required"}) ||
		collision.Output.AssignedLocator != collision.Assigned ||
		collision.Output.RequestedLocator != collision.Requested ||
		collision.Output.ID != collision.Identity || !collision.Output.LocatorRepairRequired ||
		len(collision.ClearOnly) != 2 {
		t.Fatalf("D127 locator collision = %#v", collision)
	}
	cursor := fixture.D127.CursorProvisionalTarget
	if cursor.Delivery != "environment" || cursor.EnvironmentSequence <= cursor.TargetBirthSequence ||
		cursor.Causation != "target-birth-command" || cursor.Eligibility != "birth-terminal-success" ||
		cursor.BirthRefusal != "dependent-cursor-quarantined" {
		t.Fatalf("D127 cursor dependency = %#v", cursor)
	}
	if fixture.D127.CursorNoop.AcceptedEvents != nil || fixture.D127.CursorNoop.Effects != "none" ||
		fixture.D127.CursorNoop.Output["changed"] != false {
		t.Fatalf("D127 cursor no-op = %#v", fixture.D127.CursorNoop)
	}
	command, err := normalizeJSONNumbers(fixture.D127.CursorNoop.CanonicalCommand)
	if err != nil {
		t.Fatal(err)
	}
	commandBytes, err := clientCanonical(command)
	if err != nil {
		t.Fatal(err)
	}
	commandDigest := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), commandBytes...))
	wantNoopHash := "sha256:" + hex.EncodeToString(commandDigest[:])
	f, err := os.Open("../../../docs/wipd/result-receipt-idempotency-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var resultSource struct {
		Receipts map[string]struct {
			RequestHash string `json:"request_hash"`
		} `json:"receipts"`
	}
	if err := json.NewDecoder(f).Decode(&resultSource); err != nil {
		t.Fatal(err)
	}
	if got := resultSource.Receipts["successful_noop"].RequestHash; got != wantNoopHash {
		t.Fatalf("cursor no-op canonical request hash = %s, fixture = %s", wantNoopHash, got)
	}

	privacy := fixture.Privacy
	if privacy.RoutineLogDays != 30 || privacy.SecurityAuditDays != 90 || privacy.RestrictedHashDays != 7 ||
		!slices.Contains(privacy.DomainLifetime, "terminal-receipt-and-hash") ||
		!slices.Contains(privacy.CompactableAfterTerminalAudit, "canonical-command-bytes") ||
		!slices.Contains(privacy.RetainUntilResolution, "quarantined-command-bytes") ||
		!slices.Contains(privacy.NeverLog, "grant-or-token") {
		t.Fatalf("privacy retention vector = %#v", privacy)
	}

	migration := fixture.Migration
	if len(migration.CouplingSources) != 6 || migration.UnassignableFact != "migration.refused-unassignable-fact" ||
		migration.ComponentRule != "users-may-union-never-split" ||
		migration.Group.CommandID != migration.Group.CorrelationOrigin ||
		migration.Group.Sort != "first-event-ulid-then-correlation-origin" ||
		migration.SyntheticBinding.Schema != "wipd.migration-binding/1" ||
		migration.SyntheticBinding.Event != "clone.environment-bound" ||
		!slices.Contains(migration.ProofBinds, "owner-authorization-and-seal") ||
		len(migration.ProofBinds) != 9 ||
		migration.RollbackAfterDestinationSubmission != "migration.rollback-forbidden" {
		t.Fatalf("D130 migration closure = %#v", migration)
	}
	if len(fixture.Implementability.M3) == 0 || len(fixture.Implementability.M4) == 0 ||
		len(fixture.Implementability.ForbiddenStep9Implementation) != 7 {
		t.Fatalf("M3/M4 implementability boundary = %#v", fixture.Implementability)
	}
}

func TestStep9NormativeReviewSealsEveryDeferredArea(t *testing.T) {
	review, err := os.ReadFile("../../../docs/wipd/design-security-privacy-review.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"D115–D131", "Q01–Q25", "wipd.signed-artifact/1", "Ed25519",
		"planned-handoff", "disaster-restore", "claim-stand-down",
		"wipd://authority/", "wipd://environment/", "Same-key renewal",
		"matter.locator-repair-required", "cursor.move@v1", "deterministic no-op",
		"Coupling audit", "clone.environment-bound", "wipd.migration-proof/1",
		"M3 can implement", "M4 needs only", "M2 is sealed for M3 and M4",
	} {
		if !strings.Contains(string(review), token) {
			t.Errorf("Step 9 review does not close %q", token)
		}
	}
	for _, forbidden := range []string{"CREATE TABLE", "net.Listen", "cobra.Command", "Store.Commit("} {
		if strings.Contains(string(review), forbidden) {
			t.Errorf("Step 9 review crosses production boundary with %q", forbidden)
		}
	}
}

func unsignedArtifactMap(vector step9PortableSignature, payload []byte) map[string]any {
	envelope := vector.Envelope
	return map[string]any{
		"schema": envelope.Schema, "kind": envelope.Kind, "domain_id": envelope.DomainID,
		"authority_epoch": envelope.AuthorityEpoch, "signer_role": envelope.SignerRole,
		"signer_key_id": vector.SignerKeyID, "key_generation": envelope.KeyGeneration,
		"artifact_sequence":        envelope.ArtifactSequence,
		"previous_artifact_digest": envelope.PreviousArtifactDigest,
		"issued_at":                envelope.IssuedAt, "payload_schema": envelope.PayloadSchema,
		"payload_digest": vector.PayloadDigest, "payload": payload,
	}
}

func loadAcceptedReceiptCanonical(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open("../../../docs/wipd/result-receipt-idempotency-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(f)
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	receipts := root["receipts"].(map[string]any)
	receipt := receipts["accepted"].(map[string]any)
	result := receipt["result"].(map[string]any)
	output := mustHex(t, result["output_cbor_hex"].(string))
	delete(result, "output_cbor_hex")
	result["output"] = output
	normalized, err := normalizeJSONNumbers(receipt)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := clientCanonical(normalized)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func normalizeJSONNumbers(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		return strconv.ParseUint(value.String(), 10, 64)
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := normalizeJSONNumbers(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			normalized, err := normalizeJSONNumbers(child)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, child := range value {
		result[key] = child
	}
	return result
}

func signatureNegativeNames(cases []step9SignatureNegative) []string {
	result := make([]string, 0, len(cases))
	for _, item := range cases {
		result = append(result, item.Name)
	}
	sort.Strings(result)
	return result
}
