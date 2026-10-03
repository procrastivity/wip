package wipd

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestBatchSweepAnonymousResultPayloadCodec(t *testing.T) {
	id := operation.BatchSweepAnonymousV1.Metadata().Operation
	if _, found := operationDefinition(id); found {
		t.Fatal("batch sweep unexpectedly entered the default semantic catalogue")
	}
	if _, found := connectedOperationDefinition(id); !found {
		t.Fatal("explicit connected Step 7 result codec omitted the batch sweep definition")
	}
	cases := []struct {
		name    string
		outcome string
		problem string
	}{
		{name: "swept", outcome: "swept"},
		{name: "already swept", outcome: "already-swept"},
		{name: "missing target", problem: "refusal.batch-sweep-target-missing"},
		{name: "claim close", problem: "refusal.batch-sweep-claim-close"},
		{name: "not eligible", problem: "refusal.batch-sweep-not-eligible"},
		{name: "unsupported state", problem: "refusal.batch-sweep-unsupported-state"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]any{"code": "result.succeeded", "output": nil, "problem_code": nil}
			want := operation.Result{Code: operation.ResultSucceeded}
			if test.outcome != "" {
				fields["output"] = batchSweepResultTestPayload(t, map[string]any{"outcome": test.outcome})
				want.Output = operation.BatchSweepAnonymousOutput{Outcome: operation.BatchSweepAnonymousOutcome(test.outcome)}
			} else {
				fields["code"] = "result.refused"
				fields["problem_code"] = test.problem
				want.Code = operation.ResultRefused
				want.Problem = &operation.Problem{Code: operation.ProblemCode(test.problem), Message: "presentation must not appear on the wire"}
			}
			wire := batchSweepResultTestPayload(t, fields)
			encoded, err := encodeOperationResultPayload(id, want)
			if err != nil || !bytes.Equal(encoded, wire) {
				t.Fatalf("encode = %x, %v; want exact payload %x", encoded, err, wire)
			}
			got, err := decodeOperationResultPayload(id, wire)
			if want.Problem != nil {
				want.Problem.Message = "local Handler returned " + test.problem
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("decode = %#v, %v; want %#v", got, err, want)
			}
			reencoded, err := encodeOperationResultPayload(id, got)
			if err != nil || !bytes.Equal(reencoded, wire) {
				t.Fatalf("reencode = %x, %v; want %x", reencoded, err, wire)
			}
		})
	}
}

func TestBatchSweepAnonymousResultPayloadDecodeRejectsMalformed(t *testing.T) {
	id := operation.BatchSweepAnonymousV1.Metadata().Operation
	swept := batchSweepResultTestPayload(t, map[string]any{"outcome": "swept"})
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing code", func(m map[string]any) { delete(m, "code") }},
		{"missing output", func(m map[string]any) { delete(m, "output") }},
		{"missing problem code", func(m map[string]any) { delete(m, "problem_code") }},
		{"extra field", func(m map[string]any) { m["message"] = "extra" }},
		{"code type", func(m map[string]any) { m["code"] = uint64(1) }},
		{"unknown code", func(m map[string]any) { m["code"] = "result.other" }},
		{"null output", func(m map[string]any) { m["output"] = nil }},
		{"inline output map", func(m map[string]any) { m["output"] = map[string]any{"outcome": "swept"} }},
		{"malformed output bytes", func(m map[string]any) { m["output"] = []byte{0xff} }},
		{"output scalar", func(m map[string]any) { m["output"] = batchSweepResultTestPayload(t, "swept") }},
		{"missing outcome", func(m map[string]any) { m["output"] = batchSweepResultTestPayload(t, map[string]any{}) }},
		{"extra output field", func(m map[string]any) {
			m["output"] = batchSweepResultTestPayload(t, map[string]any{"outcome": "already-swept", "event_count": uint64(0)})
		}},
		{"outcome type", func(m map[string]any) { m["output"] = batchSweepResultTestPayload(t, map[string]any{"outcome": true}) }},
		{"empty outcome", func(m map[string]any) { m["output"] = batchSweepResultTestPayload(t, map[string]any{"outcome": ""}) }},
		{"unknown outcome", func(m map[string]any) {
			m["output"] = batchSweepResultTestPayload(t, map[string]any{"outcome": "already_swept"})
		}},
		{"success with problem", func(m map[string]any) { m["problem_code"] = "refusal.batch-sweep-not-eligible" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fields := map[string]any{"code": "result.succeeded", "output": swept, "problem_code": nil}
			test.mutate(fields)
			if _, err := decodeOperationResultPayload(id, batchSweepResultTestPayload(t, fields)); !errors.Is(err, errMalformedMessage) {
				t.Fatalf("decode malformed result error = %v", err)
			}
		})
	}
	for _, test := range []struct {
		name    string
		code    string
		output  any
		problem any
	}{
		{"refusal with output", "result.refused", swept, "refusal.batch-sweep-claim-close"},
		{"null refusal", "result.refused", nil, nil},
		{"empty refusal", "result.refused", nil, ""},
		{"refusal type", "result.refused", nil, uint64(2)},
		{"other operation refusal", "result.refused", nil, "refusal.unknown-clone"},
		{"unknown sweep refusal", "result.refused", nil, "refusal.batch-sweep-other"},
		{"rejected refusal", "result.rejected", nil, "refusal.batch-sweep-target-missing"},
		{"failed refusal", "result.failed", nil, "refusal.batch-sweep-unsupported-state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := batchSweepResultTestPayload(t, map[string]any{"code": test.code, "output": test.output, "problem_code": test.problem})
			if _, err := decodeOperationResultPayload(id, wire); !errors.Is(err, errMalformedMessage) {
				t.Fatalf("decode malformed problem error = %v", err)
			}
		})
	}
	for _, payload := range [][]byte{{0xff}, batchSweepResultTestPayload(t, "not a result map")} {
		if _, err := decodeOperationResultPayload(id, payload); !errors.Is(err, errMalformedMessage) {
			t.Fatalf("decode malformed payload error = %v", err)
		}
	}
}

func TestBatchSweepAnonymousResultPayloadEncodeRejectsInvalid(t *testing.T) {
	id := operation.BatchSweepAnonymousV1.Metadata().Operation
	output := operation.BatchSweepAnonymousOutput{Outcome: operation.BatchSweepAnonymousAlreadySwept}
	problem := &operation.Problem{Code: operation.ProblemBatchSweepClaimClose, Message: "refused"}
	for _, result := range []operation.Result{
		{Code: operation.ResultSucceeded},
		{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{Title: "wrong type"}},
		{Code: operation.ResultSucceeded, Output: &output},
		{Code: operation.ResultSucceeded, Output: operation.BatchSweepAnonymousOutput{}},
		{Code: operation.ResultSucceeded, Output: operation.BatchSweepAnonymousOutput{Outcome: "other"}},
		{Code: operation.ResultSucceeded, Output: output, Problem: problem},
		{Code: "result.other", Problem: problem},
		{Code: operation.ResultRefused, Output: output, Problem: problem},
		{Code: operation.ResultRefused},
		{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemBatchSweepNotEligible}},
		{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemUnknownClone, Message: "other operation"}},
		{Code: operation.ResultRejected, Problem: problem},
		{Code: operation.ResultFailed, Problem: problem},
	} {
		if _, err := encodeOperationResultPayload(id, result); err == nil {
			t.Errorf("encoded invalid typed result %#v", result)
		}
	}
	wire := batchSweepResultTestPayload(t, map[string]any{
		"code": "result.succeeded", "output": batchSweepResultTestPayload(t, map[string]any{"outcome": "already-swept"}), "problem_code": nil,
	})
	for _, unknown := range []operation.ID{{Name: id.Name, Version: 2}, {Name: "batch.sweep-other", Version: 1}} {
		if _, err := encodeOperationResultPayload(unknown, operation.Result{Code: operation.ResultSucceeded, Output: output}); err == nil {
			t.Errorf("encoded unknown operation %s", unknown)
		}
		if _, err := decodeOperationResultPayload(unknown, wire); !errors.Is(err, errMalformedMessage) {
			t.Errorf("decode unknown operation %s error = %v", unknown, err)
		}
	}
}

func batchSweepResultTestPayload(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := encodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
