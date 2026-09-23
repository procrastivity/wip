package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const step5VectorsPath = "../../docs/wipd/result-receipt-idempotency-vectors.json"

var (
	step5ULIDPattern   = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	step5DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type step5Fixture struct {
	Notation       string                  `json:"notation"`
	IdentitySchema string                  `json:"identity_schema"`
	ReceiptSchema  string                  `json:"receipt_schema"`
	ResultMappings []step5ResultMapping    `json:"result_mappings"`
	Receipts       map[string]step5Receipt `json:"receipts"`
	Vectors        []step5Vector           `json:"vectors"`
}

type step5ResultMapping struct {
	ResultCode      string   `json:"result_code"`
	ProtocolOutcome string   `json:"protocol_outcome"`
	Output          string   `json:"output"`
	ProblemPrefixes []string `json:"problem_prefixes"`
	AcceptedEvents  string   `json:"accepted_events"`
	Effects         string   `json:"effects"`
}

type step5Receipt struct {
	Schema         string           `json:"schema"`
	DomainID       string           `json:"domain_id"`
	AuthorityEpoch uint64           `json:"authority_epoch"`
	IdentitySchema string           `json:"identity_schema"`
	CommandID      string           `json:"command_id"`
	RequestHash    string           `json:"request_hash"`
	Operation      step5Operation   `json:"operation"`
	Environment    step5Environment `json:"environment"`
	Result         step5Result      `json:"result"`
	AcceptedEvents *step5EventRange `json:"accepted_events"`
}

type step5Operation struct {
	Name    string `json:"name"`
	Version uint16 `json:"version"`
}

type step5Environment struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}

type step5Result struct {
	Code          string  `json:"code"`
	OutputCBORHex *string `json:"output_cbor_hex"`
	ProblemCode   *string `json:"problem_code"`
}

type step5EventRange struct {
	FirstEventID string `json:"first_event_id"`
	LastEventID  string `json:"last_event_id"`
	EventCount   uint64 `json:"event_count"`
}

type step5Vector struct {
	Name        string         `json:"name"`
	CommandID   *string        `json:"command_id"`
	RequestHash *string        `json:"request_hash"`
	Events      []string       `json:"events"`
	Expected    step5Expected  `json:"expected"`
	Recovery    *step5Recovery `json:"recovery"`
}

type step5Expected struct {
	Outcome            string  `json:"outcome"`
	Code               string  `json:"code"`
	SubmissionKnown    *bool   `json:"submission_known"`
	SemanticExecutions uint64  `json:"semantic_executions"`
	Receipt            *string `json:"receipt"`
	Effects            string  `json:"effects"`
	DependentState     string  `json:"dependent_state"`
}

type step5Recovery struct {
	Action             string            `json:"action"`
	Request            step5ReceiptQuery `json:"request"`
	ResponseKind       string            `json:"response_kind"`
	Outcome            string            `json:"outcome"`
	Receipt            string            `json:"receipt"`
	SemanticExecutions uint64            `json:"semantic_executions"`
	DependentState     string            `json:"dependent_state"`
}

type step5ReceiptQuery struct {
	Schema      string `json:"schema"`
	DomainID    string `json:"domain_id"`
	CommandID   string `json:"command_id"`
	RequestHash string `json:"request_hash"`
}

func loadStep5Fixture(t *testing.T) step5Fixture {
	t.Helper()

	f, err := os.Open(step5VectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var got step5Fixture
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

func TestStep5ResultMappingsAndReceiptInvariants(t *testing.T) {
	fixture := loadStep5Fixture(t)
	if fixture.Notation != "wipd.result-receipt-vector/1" ||
		fixture.IdentitySchema != "wipd.command/1" ||
		fixture.ReceiptSchema != "wipd.terminal-receipt/1" {
		t.Fatalf("fixture headers = %#v", fixture)
	}

	wantMappings := []step5ResultMapping{
		{ResultCode: "result.succeeded", ProtocolOutcome: "terminal", Output: "required", ProblemPrefixes: []string{}, AcceptedEvents: "required-nonempty", Effects: "events-projections-receipt-atomic"},
		{ResultCode: "result.rejected", ProtocolOutcome: "terminal", Output: "null", ProblemPrefixes: []string{"not-found", "operation", "validation"}, AcceptedEvents: "null", Effects: "receipt-only-no-model-effect"},
		{ResultCode: "result.refused", ProtocolOutcome: "terminal", Output: "null", ProblemPrefixes: []string{"refusal"}, AcceptedEvents: "null", Effects: "receipt-only-no-model-effect"},
		{ResultCode: "result.failed", ProtocolOutcome: "terminal", Output: "null", ProblemPrefixes: []string{"internal"}, AcceptedEvents: "null", Effects: "receipt-only-no-model-effect"},
	}
	if !reflect.DeepEqual(fixture.ResultMappings, wantMappings) {
		t.Fatalf("result mappings\n got: %#v\nwant: %#v", fixture.ResultMappings, wantMappings)
	}

	if got := sortedMapKeys(fixture.Receipts); !reflect.DeepEqual(got, []string{"accepted", "refused", "rejected"}) {
		t.Fatalf("receipt names = %v", got)
	}
	for name, receipt := range fixture.Receipts {
		validateStep5Receipt(t, fixture, name, receipt)
	}

	accepted := fixture.Receipts["accepted"]
	outputHex := requireString(t, accepted.Result.OutputCBORHex, "accepted output")
	outputBytes, err := hex.DecodeString(outputHex)
	if err != nil {
		t.Fatalf("accepted output hex: %v", err)
	}
	gotOutput := decodeDeterministicCBOR(t, outputBytes)
	wantOutput := map[string]any{
		"id":      "01K6C000000000000000000001",
		"locator": "protocol-identity",
		"title":   "Café protocol identity",
	}
	if !reflect.DeepEqual(gotOutput, wantOutput) {
		t.Fatalf("accepted output\n got: %#v\nwant: %#v", gotOutput, wantOutput)
	}
	if accepted.AcceptedEvents == nil || accepted.AcceptedEvents.EventCount != 2 ||
		accepted.AcceptedEvents.FirstEventID == accepted.AcceptedEvents.LastEventID {
		t.Fatalf("accepted event range = %#v", accepted.AcceptedEvents)
	}
}

func TestStep5DeterministicOutcomesRetryAndRecovery(t *testing.T) {
	fixture := loadStep5Fixture(t)
	vectors := make(map[string]step5Vector, len(fixture.Vectors))
	for _, vector := range fixture.Vectors {
		if _, exists := vectors[vector.Name]; exists {
			t.Fatalf("duplicate vector %q", vector.Name)
		}
		vectors[vector.Name] = vector
		validateStep5Vector(t, fixture, vector)
	}
	wantNames := []string{
		"accepted-command",
		"definitely-unsent-unavailable",
		"dependent-blocked-on-outcome-unknown",
		"lost-response-receipt-query",
		"malformed-command",
		"same-id-different-hash-refusal",
		"same-id-same-hash-retry",
		"semantic-rejected-command",
		"valid-guard-refusal",
	}
	if got := sortedMapKeys(vectors); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("vector names\n got: %v\nwant: %v", got, wantNames)
	}

	accepted := vectors["accepted-command"]
	assertStep5Events(t, accepted,
		"submission.committed", "semantic.executed", "terminal.committed", "response.observed")
	if accepted.Expected.SubmissionKnown == nil || !*accepted.Expected.SubmissionKnown ||
		accepted.Expected.SemanticExecutions != 1 {
		t.Fatalf("accepted execution = %#v", accepted.Expected)
	}

	rejected := vectors["semantic-rejected-command"]
	assertStep5Events(t, rejected,
		"canonical-command.valid", "submission.committed", "semantic.rejected", "terminal.committed")
	refused := vectors["valid-guard-refusal"]
	assertStep5Events(t, refused,
		"canonical-command.valid", "submission.committed", "guard.refused", "terminal.committed")
	for _, vector := range []step5Vector{rejected, refused} {
		if vector.Expected.SubmissionKnown == nil || !*vector.Expected.SubmissionKnown ||
			vector.Expected.SemanticExecutions != 1 || vector.Expected.Effects != "receipt-only-no-model-effect" {
			t.Fatalf("non-success terminal execution %s = %#v", vector.Name, vector.Expected)
		}
	}

	retry := vectors["same-id-same-hash-retry"]
	assertStep5Events(t, retry, "terminal.lookup", "id-and-hash.match", "stored-receipt.replayed")
	if *accepted.CommandID != *retry.CommandID || *accepted.RequestHash != *retry.RequestHash {
		t.Fatal("same-ID/same-hash retry changed command identity")
	}
	if retry.Expected.SemanticExecutions != 0 || requireString(t, retry.Expected.Receipt, "retry receipt") != "accepted" {
		t.Fatalf("retry executed or did not replay: %#v", retry.Expected)
	}

	conflict := vectors["same-id-different-hash-refusal"]
	assertStep5Events(t, conflict, "submission.lookup", "id-match-hash-mismatch", "conflict.returned")
	if *conflict.CommandID != *accepted.CommandID || *conflict.RequestHash == *accepted.RequestHash {
		t.Fatal("different-hash vector does not isolate ID reuse")
	}
	assertNoSubmissionOrReceipt(t, conflict)

	malformed := vectors["malformed-command"]
	assertStep5Events(t, malformed, "frame.decoded", "canonical-command.malformed", "submission.not-crossed")
	if malformed.CommandID != nil || malformed.RequestHash != nil {
		t.Fatal("malformed command acquired canonical identity")
	}
	assertNoSubmissionOrReceipt(t, malformed)

	unavailable := vectors["definitely-unsent-unavailable"]
	assertStep5Events(t, unavailable, "connect.failed", "command-bytes-written=false", "submission.not-crossed")
	assertNoSubmissionOrReceipt(t, unavailable)
	if unavailable.Expected.Outcome != "unavailable" || !strings.Contains(unavailable.Expected.Effects, "no-queue") {
		t.Fatalf("unavailable vector = %#v", unavailable.Expected)
	}

	lost := vectors["lost-response-receipt-query"]
	assertStep5Events(t, lost, "command.bytes-written", "terminal.committed", "response.lost")
	if lost.Expected.Outcome != "outcome-unknown" || lost.Expected.SubmissionKnown != nil ||
		lost.Expected.SemanticExecutions != 1 || lost.Expected.DependentState != "blocked-before-submission" {
		t.Fatalf("lost-response initial outcome = %#v", lost.Expected)
	}
	if lost.Recovery == nil {
		t.Fatal("lost-response vector has no recovery")
	}
	recovery := lost.Recovery
	if recovery.Action != "receipt.query" || recovery.Request.Schema != "wipd.receipt-query/1" ||
		recovery.ResponseKind != "command.terminal" || recovery.Outcome != "terminal" ||
		recovery.SemanticExecutions != 0 || recovery.Receipt != "accepted" {
		t.Fatalf("lost-response recovery = %#v", recovery)
	}
	if recovery.Request.CommandID != *lost.CommandID || recovery.Request.RequestHash != *lost.RequestHash {
		t.Fatal("receipt query changed command identity")
	}
	receipt := fixture.Receipts[recovery.Receipt]
	if receipt.DomainID != recovery.Request.DomainID || receipt.CommandID != recovery.Request.CommandID ||
		receipt.RequestHash != recovery.Request.RequestHash {
		t.Fatal("receipt query returned a receipt for another identity")
	}

	dependent := vectors["dependent-blocked-on-outcome-unknown"]
	assertStep5Events(t, dependent,
		"predecessor.outcome-unknown", "dependent.causation-bound", "dependent.submission-blocked")
	if dependent.Expected.SemanticExecutions != 0 || dependent.Expected.SubmissionKnown == nil ||
		*dependent.Expected.SubmissionKnown || dependent.Expected.DependentState != "blocked-before-submission" {
		t.Fatalf("dependent blocking = %#v", dependent.Expected)
	}
}

func TestStep5ContractMapsDecisionsQuestionsAndBoundaries(t *testing.T) {
	contract, err := os.ReadFile("../../docs/wipd/result-receipt-idempotency-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"wipd.command/1", "request_hash", "result.succeeded", "result.rejected",
		"result.refused", "result.failed", "submission.accepted", "receipt.query",
		"command.id-conflict", "outcome-unknown", "pending-return",
		"D120", "D121", "D122", "D123", "D126", "D128", "D129", "D130", "D131",
		"Q06", "Q07", "Q08", "Q20", "Q21", "Q24", "Q25",
		"There are no unresolved Step 5 decisions",
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

func validateStep5Receipt(t *testing.T, fixture step5Fixture, name string, receipt step5Receipt) {
	t.Helper()
	if receipt.Schema != fixture.ReceiptSchema || receipt.IdentitySchema != fixture.IdentitySchema {
		t.Fatalf("receipt %s schemas = %q/%q", name, receipt.Schema, receipt.IdentitySchema)
	}
	for field, value := range map[string]string{
		"domain_id": receipt.DomainID, "command_id": receipt.CommandID, "environment.id": receipt.Environment.ID,
	} {
		if !step5ULIDPattern.MatchString(value) {
			t.Errorf("receipt %s %s = %q", name, field, value)
		}
	}
	if receipt.AuthorityEpoch == 0 || receipt.Environment.Sequence == 0 ||
		receipt.Operation.Name != "matter.create" || receipt.Operation.Version != 1 ||
		!step5DigestPattern.MatchString(receipt.RequestHash) {
		t.Errorf("receipt %s identity fields = %#v", name, receipt)
	}

	mapping := mappingByCode(t, fixture.ResultMappings, receipt.Result.Code)
	if receipt.Result.Code == "result.succeeded" {
		if receipt.Result.OutputCBORHex == nil || receipt.Result.ProblemCode != nil || receipt.AcceptedEvents == nil {
			t.Fatalf("succeeded receipt %s = %#v", name, receipt)
		}
		if !step5ULIDPattern.MatchString(receipt.AcceptedEvents.FirstEventID) ||
			!step5ULIDPattern.MatchString(receipt.AcceptedEvents.LastEventID) || receipt.AcceptedEvents.EventCount == 0 {
			t.Fatalf("succeeded receipt %s range = %#v", name, receipt.AcceptedEvents)
		}
		return
	}
	if mapping.Effects != "receipt-only-no-model-effect" || receipt.Result.OutputCBORHex != nil ||
		receipt.Result.ProblemCode == nil || receipt.AcceptedEvents != nil {
		t.Fatalf("non-success receipt %s has effects/output/events = %#v", name, receipt)
	}
	prefix, _, ok := strings.Cut(*receipt.Result.ProblemCode, ".")
	if !ok || !contains(mapping.ProblemPrefixes, prefix) {
		t.Fatalf("receipt %s problem %q outside %v", name, *receipt.Result.ProblemCode, mapping.ProblemPrefixes)
	}
}

func validateStep5Vector(t *testing.T, fixture step5Fixture, vector step5Vector) {
	t.Helper()
	if vector.CommandID != nil && !step5ULIDPattern.MatchString(*vector.CommandID) {
		t.Errorf("vector %s command ID = %q", vector.Name, *vector.CommandID)
	}
	if vector.RequestHash != nil && !step5DigestPattern.MatchString(*vector.RequestHash) {
		t.Errorf("vector %s request hash = %q", vector.Name, *vector.RequestHash)
	}
	if len(vector.Events) == 0 {
		t.Errorf("vector %s has no ordering evidence", vector.Name)
	}
	if vector.Expected.Receipt != nil {
		receipt, ok := fixture.Receipts[*vector.Expected.Receipt]
		if !ok {
			t.Fatalf("vector %s references missing receipt %q", vector.Name, *vector.Expected.Receipt)
		}
		if vector.CommandID == nil || vector.RequestHash == nil || receipt.CommandID != *vector.CommandID || receipt.RequestHash != *vector.RequestHash {
			t.Fatalf("vector %s receipt identity mismatch", vector.Name)
		}
		if vector.Expected.Outcome != "terminal" || vector.Expected.Code != receipt.Result.Code {
			t.Fatalf("vector %s terminal mapping mismatch", vector.Name)
		}
	} else if vector.Expected.Outcome == "terminal" {
		t.Errorf("terminal vector %s has no receipt", vector.Name)
	}
}

func mappingByCode(t *testing.T, mappings []step5ResultMapping, code string) step5ResultMapping {
	t.Helper()
	for _, mapping := range mappings {
		if mapping.ResultCode == code {
			return mapping
		}
	}
	t.Fatalf("no result mapping for %q", code)
	return step5ResultMapping{}
}

func assertNoSubmissionOrReceipt(t *testing.T, vector step5Vector) {
	t.Helper()
	if vector.Expected.SubmissionKnown == nil || *vector.Expected.SubmissionKnown ||
		vector.Expected.SemanticExecutions != 0 || vector.Expected.Receipt != nil ||
		!strings.Contains(vector.Expected.Effects, "no-model-effect") {
		t.Fatalf("vector %s has submission, execution, receipt, or model effect: %#v", vector.Name, vector.Expected)
	}
}

func assertStep5Events(t *testing.T, vector step5Vector, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(vector.Events, want) {
		t.Fatalf("vector %s events\n got: %v\nwant: %v", vector.Name, vector.Events, want)
	}
}

func requireString(t *testing.T, value *string, field string) string {
	t.Helper()
	if value == nil {
		t.Fatalf("%s is null", field)
	}
	return *value
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
