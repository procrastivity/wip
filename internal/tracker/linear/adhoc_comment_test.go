package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const freeBody = "Rolled the migration back by hand.\n\nSee the incident note for the sequence."

func TestCommentDeliversAFreeBodyVerbatimUnderTheDeterministicUUID(t *testing.T) {
	var operations []string
	mutations := 0
	wantID := deterministicUUID("comment", "adhoc-key")
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		operations = append(operations, request.OperationName)
		switch request.OperationName {
		case "FindCommentByClientID":
			if got := request.stringVariable("id"); got != wantID {
				t.Fatalf("lookup id = %q, want %q", got, wantID)
			}
			return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{}}})
		case "ReadIssue":
			return issueResponse(workflowState{ID: startedState, Name: "Doing", Type: "started"}, "lease")
		case "CreateComment":
			mutations++
			input := request.input(t)
			assertUUIDv4(t, input["id"].(string))
			if input["id"] != wantID {
				t.Fatalf("comment id = %v, want %q", input["id"], wantID)
			}
			if input["issueId"] != "BDS-124" {
				t.Fatalf("comment issueId = %v", input["issueId"])
			}
			if input["body"] != freeBody {
				t.Fatalf("comment body = %q, want %q", input["body"], freeBody)
			}
			body := input["body"].(string)
			if strings.Contains(body, "Stage ") || strings.Contains(body, "wip-idempotency") {
				t.Fatalf("comment body = %q, want no lifecycle rendering and no marker", body)
			}
			return gqlData(map[string]any{"commentCreate": map[string]any{
				"success": true, "comment": map[string]any{"id": wantID},
			}})
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
		Kind: "comment", Ref: "BDS-124", IdempotencyKey: "adhoc-key",
		Payload: json.RawMessage(mustEncode(t, map[string]any{"body": freeBody})),
	})
	if err != nil || result.Outcome != tracker.Delivered || mutations != 1 {
		t.Fatalf("result = %+v, err = %v, mutations = %d", result, err, mutations)
	}
	wantOperations := []string{"FindCommentByClientID", "ReadIssue", "CreateComment"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", operations, wantOperations)
	}
}

func TestCommentFallsBackToLifecycleRenderingWhenTheBodyIsEmpty(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "no body field", payload: `{"stage":"01ABC","title":"Provider API","action":"closed"}`},
		{name: "blank body field", payload: `{"stage":"01ABC","title":"Provider API","action":"closed","body":"   "}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutations := 0
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				switch request.OperationName {
				case "FindCommentByClientID":
					return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{}}})
				case "ReadIssue":
					return issueResponse(workflowState{ID: startedState, Name: "Doing", Type: "started"}, "lease")
				case "CreateComment":
					mutations++
					input := request.input(t)
					if input["body"] != `Stage "Provider API" closed.` {
						t.Fatalf("comment body = %q, want the lifecycle rendering", input["body"])
					}
					return gqlData(map[string]any{"commentCreate": map[string]any{
						"success": true, "comment": map[string]any{"id": input["id"]},
					}})
				default:
					t.Fatalf("operation = %q", request.OperationName)
					return testResponse{}
				}
			})
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "comment", Ref: "BDS-124", IdempotencyKey: "lifecycle-key",
				Payload: json.RawMessage(test.payload),
			})
			if err != nil || result.Outcome != tracker.Delivered || mutations != 1 {
				t.Fatalf("result = %+v, err = %v, mutations = %d", result, err, mutations)
			}
		})
	}
}

func TestCommentRefusesAPayloadWithNeitherBodyNorTitle(t *testing.T) {
	called := false
	adapter := newTestAdapter(t, func(*testing.T, gqlTestRequest) testResponse {
		called = true
		return testResponse{}
	})
	for _, payload := range []string{
		`{}`,
		`{"body":"   "}`,
		`{"stage":"01ABC","action":"closed"}`,
		`{"title":"   ","body":""}`,
	} {
		result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
			Kind: "comment", Ref: "BDS-124", IdempotencyKey: "key",
			Payload: json.RawMessage(payload),
		})
		if err != nil || result.Outcome != tracker.PermanentRefusal || !strings.Contains(result.Reason, "malformed comment payload") {
			t.Fatalf("payload %s: result = %+v, err = %v", payload, result, err)
		}
	}
	if called {
		t.Fatal("a refused comment payload made a request")
	}
}

func TestFreeBodyCommentConvergesOnRetryWithoutASecondMutation(t *testing.T) {
	created := false
	lookups := 0
	mutations := 0
	wantID := deterministicUUID("comment", "adhoc-replay-key")
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		switch request.OperationName {
		case "FindCommentByClientID":
			lookups++
			if got := request.stringVariable("id"); got != wantID {
				t.Fatalf("replay lookup id = %q, want %q", got, wantID)
			}
			if created {
				return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{map[string]any{"id": wantID}}}})
			}
			return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{}}})
		case "ReadIssue":
			return issueResponse(workflowState{ID: startedState, Name: "Doing", Type: "started"}, "lease")
		case "CreateComment":
			mutations++
			input := request.input(t)
			if input["id"] != wantID || input["body"] != freeBody {
				t.Fatalf("comment input = %#v", input)
			}
			created = true
			return gqlData(map[string]any{"commentCreate": map[string]any{
				"success": true, "comment": map[string]any{"id": wantID},
			}})
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	entry := store.OutboxEntry{
		Kind: "comment", Ref: "BDS-124", IdempotencyKey: "adhoc-replay-key",
		Payload: json.RawMessage(mustEncode(t, map[string]any{"body": freeBody})),
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := adapter.Deliver(context.Background(), entry)
		want := tracker.Delivered
		if attempt == 1 {
			want = tracker.Converged
		}
		if err != nil || result.Outcome != want {
			t.Fatalf("attempt %d result = %+v, err = %v; want %s", attempt+1, result, err, want)
		}
	}
	if mutations != 1 || lookups != 2 {
		t.Fatalf("lookups = %d, mutations = %d; want 2 and 1", lookups, mutations)
	}
}

func TestFreeBodyCommentReconcilesAnAmbiguousMutationFailure(t *testing.T) {
	lookups := 0
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		switch request.OperationName {
		case "FindCommentByClientID":
			lookups++
			if lookups == 2 {
				return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{
					map[string]any{"id": request.stringVariable("id")},
				}}})
			}
			return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{}}})
		case "ReadIssue":
			return issueResponse(workflowState{ID: startedState, Name: "Doing", Type: "started"}, "lease")
		case "CreateComment":
			return gqlErrors(http.StatusOK, "AUTHENTICATION_ERROR", "token expired")
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
		Kind: "comment", Ref: "BDS-124", IdempotencyKey: "adhoc-key",
		Payload: json.RawMessage(mustEncode(t, map[string]any{"body": freeBody})),
	})
	if err != nil || result.Outcome != tracker.Converged || lookups != 2 {
		t.Fatalf("result = %+v, err = %v, lookups = %d", result, err, lookups)
	}
}

func mustEncode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
