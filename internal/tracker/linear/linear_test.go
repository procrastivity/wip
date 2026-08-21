package linear

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const (
	testTeam      = "11111111-1111-4111-8111-111111111111"
	startedState  = "22222222-2222-4222-8222-222222222222"
	startedState2 = "22222222-2222-4222-8222-222222222223"
	completeState = "33333333-3333-4333-8333-333333333333"
	canceledState = "44444444-4444-4444-8444-444444444444"
)

func TestCreateUsesDeterministicUUIDAndConvergesOnReplay(t *testing.T) {
	var created bool
	var operations []string
	mutations := 0
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		operations = append(operations, request.OperationName)
		switch request.OperationName {
		case "FindIssueByClientID":
			id := request.stringVariable("id")
			assertUUIDv4(t, id)
			if created {
				return foundIssueResponse(id)
			}
			return gqlData(map[string]any{"issues": map[string]any{"nodes": []any{}}})
		case "WorkflowStates":
			return workflowResponse(
				workflowState{ID: startedState2, Name: "Doing two", Type: "started", Position: 2},
				workflowState{ID: startedState, Name: "Doing", Type: "started", Position: 1},
			)
		case "CreateIssue":
			mutations++
			input := request.input(t)
			assertUUIDv4(t, input["id"].(string))
			if input["teamId"] != testTeam || input["stateId"] != startedState || input["title"] != "Keep the cursor visible" {
				t.Fatalf("create input = %#v", input)
			}
			if input["description"] != "Show in-progress work.\n\nSource: agent" {
				t.Fatalf("description = %q", input["description"])
			}
			created = true
			return gqlData(map[string]any{"issueCreate": map[string]any{"success": true, "issue": map[string]any{"identifier": "BDS-124"}}})
		default:
			t.Fatalf("unexpected operation %q", request.OperationName)
			return testResponse{}
		}
	})
	entry := store.OutboxEntry{
		Kind: "create", IdempotencyKey: "backlog:01ABC",
		Payload: json.RawMessage(`{"title":"Keep the cursor visible","detail":"Show in-progress work.","provenance":"agent"}`),
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := adapter.Deliver(context.Background(), entry)
		want := tracker.Delivered
		if attempt == 1 {
			want = tracker.Converged
		}
		if err != nil || result.Outcome != want || result.Ref != "BDS-124" {
			t.Fatalf("attempt %d = %+v, %v; want %s", attempt+1, result, err, want)
		}
	}
	if mutations != 1 {
		t.Fatalf("create mutations = %d, want 1", mutations)
	}
	wantOperations := []string{"FindIssueByClientID", "WorkflowStates", "CreateIssue", "FindIssueByClientID"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", operations, wantOperations)
	}
}

func TestCreateReconcilesAnAmbiguousMutationFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		reconcile bool
		want      tracker.Outcome
		wantRef   string
	}{
		{name: "post-read hit converges", reconcile: true, want: tracker.Converged, wantRef: "BDS-124"},
		{name: "post-read miss preserves retry", want: tracker.RetryableFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookups := 0
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				switch request.OperationName {
				case "FindIssueByClientID":
					lookups++
					if lookups == 2 && test.reconcile {
						return foundIssueResponse(request.stringVariable("id"))
					}
					return gqlData(map[string]any{"issues": map[string]any{"nodes": []any{}}})
				case "WorkflowStates":
					return workflowResponse(workflowState{ID: startedState, Name: "Doing", Type: "started", Position: 1})
				case "CreateIssue":
					return testResponse{status: http.StatusServiceUnavailable, body: `{}`}
				default:
					t.Fatalf("operation = %q", request.OperationName)
					return testResponse{}
				}
			})
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`),
			})
			if err != nil || result.Outcome != test.want || result.Ref != test.wantRef || lookups != 2 {
				t.Fatalf("result = %+v, err = %v, lookups = %d", result, err, lookups)
			}
		})
	}
}

func TestCreateReportsAFailedReconciliationRead(t *testing.T) {
	lookups := 0
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		switch request.OperationName {
		case "FindIssueByClientID":
			lookups++
			if lookups == 2 {
				return gqlErrors(http.StatusOK, "RATELIMITED", "reconciliation throttled")
			}
			return gqlData(map[string]any{"issues": map[string]any{"nodes": []any{}}})
		case "WorkflowStates":
			return workflowResponse(workflowState{ID: startedState, Name: "Doing", Type: "started", Position: 1})
		case "CreateIssue":
			return gqlErrors(http.StatusOK, "AUTHENTICATION_ERROR", "mutation token failed")
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
		Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`),
	})
	if err != nil || result.Outcome != tracker.RetryableFailure || !strings.Contains(result.Reason, "reconciliation throttled") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestCreateRejectsAMismatchedReplayIdentity(t *testing.T) {
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		if request.OperationName != "FindIssueByClientID" {
			t.Fatalf("operation = %q", request.OperationName)
		}
		return gqlData(map[string]any{"issues": map[string]any{"nodes": []any{map[string]any{
			"id": request.stringVariable("id"), "identifier": "BDS-124",
			"team": map[string]any{"id": "99999999-9999-4999-8999-999999999999"},
		}}}})
	})
	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
		Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`),
	})
	if err != nil || result.Outcome != tracker.PermanentRefusal || !strings.Contains(result.Reason, "unique valid identifier") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestCommentUsesDeterministicUUIDAndReconcilesFailure(t *testing.T) {
	lookups := 0
	mutations := 0
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		switch request.OperationName {
		case "FindCommentByClientID":
			lookups++
			id := request.stringVariable("id")
			assertUUIDv4(t, id)
			if lookups == 2 {
				return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{map[string]any{"id": id}}}})
			}
			return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{}}})
		case "CreateComment":
			mutations++
			input := request.input(t)
			if input["issueId"] != "BDS-124" || input["body"] != `Stage "Provider API" closed.` {
				t.Fatalf("comment input = %#v", input)
			}
			return gqlErrors(http.StatusOK, "AUTHENTICATION_ERROR", "token expired")
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
		Kind: "comment", Ref: "BDS-124", IdempotencyKey: "comment-key",
		Payload: json.RawMessage(`{"stage":"01ABC","title":"Provider API","action":"closed"}`),
	})
	if err != nil || result.Outcome != tracker.Converged || lookups != 2 || mutations != 1 {
		t.Fatalf("result = %+v, err = %v, lookups = %d, mutations = %d", result, err, lookups, mutations)
	}
}

func TestCommentConvergesOnSecondDeliveryWithoutAnotherMutation(t *testing.T) {
	var created bool
	var clientID string
	lookups := 0
	mutations := 0
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		switch request.OperationName {
		case "FindCommentByClientID":
			lookups++
			observedID := request.stringVariable("id")
			assertUUIDv4(t, observedID)
			if clientID == "" {
				clientID = observedID
			} else if observedID != clientID {
				t.Fatalf("replay UUID = %q, want %q", observedID, clientID)
			}
			if created {
				return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{map[string]any{"id": observedID}}}})
			}
			return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{}}})
		case "CreateComment":
			mutations++
			input := request.input(t)
			if input["id"] != clientID || input["issueId"] != "BDS-124" {
				t.Fatalf("comment input = %#v, want id %q and issue BDS-124", input, clientID)
			}
			created = true
			return gqlData(map[string]any{"commentCreate": map[string]any{
				"success": true, "comment": map[string]any{"id": clientID},
			}})
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	entry := store.OutboxEntry{
		Kind: "comment", Ref: "BDS-124", IdempotencyKey: "comment-replay-key",
		Payload: json.RawMessage(`{"stage":"01ABC","title":"Provider API","action":"closed"}`),
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

func TestCommentRejectsAMismatchedReplayResult(t *testing.T) {
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		if request.OperationName != "FindCommentByClientID" {
			t.Fatalf("operation = %q", request.OperationName)
		}
		return gqlData(map[string]any{"comments": map[string]any{"nodes": []any{
			map[string]any{"id": "99999999-9999-4999-8999-999999999999"},
		}}})
	})
	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
		Kind: "comment", Ref: "BDS-124", IdempotencyKey: "comment-key",
		Payload: json.RawMessage(`{"title":"Provider API","action":"closed"}`),
	})
	if err != nil || result.Outcome != tracker.PermanentRefusal || !strings.Contains(result.Reason, "matching identity") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestGraphQLErrorsAndHTTPStatusesAreClassified(t *testing.T) {
	for _, test := range []struct {
		name     string
		response testResponse
		want     tracker.Outcome
	}{
		{name: "HTTP 200 rate limited", response: gqlErrors(http.StatusOK, "RATELIMITED", "slow down"), want: tracker.RetryableFailure},
		{name: "HTTP 400 rate limited", response: gqlErrors(http.StatusBadRequest, "RATELIMITED", "slow down"), want: tracker.RetryableFailure},
		{name: "HTTP 408", response: testResponse{status: http.StatusRequestTimeout, body: `{}`}, want: tracker.RetryableFailure},
		{name: "HTTP 429", response: testResponse{status: http.StatusTooManyRequests, body: `{}`}, want: tracker.RetryableFailure},
		{name: "HTTP 503", response: testResponse{status: http.StatusServiceUnavailable, body: `{}`}, want: tracker.RetryableFailure},
		{name: "request limit header", response: testResponse{status: http.StatusBadRequest, body: `{}`, header: http.Header{"X-RateLimit-Requests-Remaining": {"0"}}}, want: tracker.RetryableFailure},
		{name: "authentication", response: gqlErrors(http.StatusOK, "AUTHENTICATION_ERROR", "bad token"), want: tracker.PermanentRefusal},
		{name: "authorization", response: gqlErrors(http.StatusOK, "FORBIDDEN", "denied"), want: tracker.PermanentRefusal},
		{name: "invalid input", response: gqlErrors(http.StatusOK, "BAD_USER_INPUT", "invalid"), want: tracker.PermanentRefusal},
		{name: "unknown code", response: gqlErrors(http.StatusOK, "FUTURE_CODE", "unknown"), want: tracker.PermanentRefusal},
		{name: "complexity exhausted", response: gqlErrors(http.StatusOK, "COMPLEXITY_LIMIT_EXCEEDED", "too complex"), want: tracker.PermanentRefusal},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, func(*testing.T, gqlTestRequest) testResponse { return test.response })
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`),
			})
			if err != nil || result.Outcome != test.want || result.Reason == "" {
				t.Fatalf("result = %+v, err = %v; want %s", result, err, test.want)
			}
		})
	}
}

func TestStateDeliveryImplementsTheLeaseMatrix(t *testing.T) {
	const oldLease = "2026-08-20T12:34:56.000Z"
	const changedLease = "2026-08-20T12:35:00.000Z"
	const writtenLease = "2026-08-20T12:36:00.000Z"
	for _, test := range []struct {
		name        string
		disposition store.TrackerDisposition
		lease       string
		observed    workflowState
		observedAt  string
		want        tracker.Outcome
		wantLease   string
		wantWrite   bool
	}{
		{name: "blank active from backlog", disposition: store.TrackerActive, observed: workflowState{ID: startedState2, Name: "Backlog", Type: "backlog"}, observedAt: changedLease, want: tracker.Delivered, wantLease: writtenLease, wantWrite: true},
		{name: "blank active from triage", disposition: store.TrackerActive, observed: workflowState{ID: startedState2, Name: "Triage", Type: "triage"}, observedAt: changedLease, want: tracker.Delivered, wantLease: writtenLease, wantWrite: true},
		{name: "blank active from unstarted", disposition: store.TrackerActive, observed: workflowState{ID: startedState2, Name: "Todo", Type: "unstarted"}, observedAt: changedLease, want: tracker.Delivered, wantLease: writtenLease, wantWrite: true},
		{name: "blank active converges", disposition: store.TrackerActive, observed: workflowState{ID: startedState2, Name: "In review", Type: "started"}, observedAt: changedLease, want: tracker.Converged, wantLease: changedLease},
		{name: "blank terminal from active", disposition: store.TrackerCompleted, observed: workflowState{ID: startedState2, Name: "In review", Type: "started"}, observedAt: changedLease, want: tracker.Delivered, wantLease: writtenLease, wantWrite: true},
		{name: "blank terminal from backlog refuses", disposition: store.TrackerCompleted, observed: workflowState{ID: startedState2, Name: "Backlog", Type: "backlog"}, observedAt: changedLease, want: tracker.LeaseMismatch},
		{name: "blank matching terminal still converges", disposition: store.TrackerCompleted, observed: workflowState{ID: completeState, Name: "Done", Type: "completed"}, observedAt: changedLease, want: tracker.Converged, wantLease: changedLease},
		{name: "blank competing terminal refuses", disposition: store.TrackerCompleted, observed: workflowState{ID: canceledState, Name: "Canceled", Type: "canceled"}, observedAt: changedLease, want: tracker.LeaseMismatch},
		{name: "matching lease writes", disposition: store.TrackerCompleted, lease: oldLease, observed: workflowState{ID: startedState2, Name: "Doing", Type: "started"}, observedAt: oldLease, want: tracker.Delivered, wantLease: writtenLease, wantWrite: true},
		{name: "changed lease elsewhere refuses", disposition: store.TrackerCompleted, lease: oldLease, observed: workflowState{ID: startedState2, Name: "Doing", Type: "started"}, observedAt: changedLease, want: tracker.LeaseMismatch},
		{name: "changed lease at candidate converges", disposition: store.TrackerCompleted, lease: oldLease, observed: workflowState{ID: completeState, Name: "Done", Type: "completed"}, observedAt: changedLease, want: tracker.Converged, wantLease: changedLease},
		{name: "terminal is beyond active with lease", disposition: store.TrackerActive, lease: oldLease, observed: workflowState{ID: completeState, Name: "Done", Type: "completed"}, observedAt: changedLease, want: tracker.Converged, wantLease: changedLease},
		{name: "canceled competes with active", disposition: store.TrackerActive, lease: oldLease, observed: workflowState{ID: canceledState, Name: "Canceled", Type: "canceled"}, observedAt: oldLease, want: tracker.LeaseMismatch},
		{name: "duplicate competes", disposition: store.TrackerCompleted, lease: oldLease, observed: workflowState{ID: canceledState, Name: "Duplicate", Type: "duplicate"}, observedAt: oldLease, want: tracker.LeaseMismatch},
		{name: "unknown type competes", disposition: store.TrackerCompleted, lease: oldLease, observed: workflowState{ID: canceledState, Name: "Future terminal", Type: "future"}, observedAt: oldLease, want: tracker.LeaseMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			var operations []string
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				operations = append(operations, request.OperationName)
				switch request.OperationName {
				case "WorkflowStates":
					typeName, err := workflowType(test.disposition)
					if err != nil {
						t.Fatal(err)
					}
					id := startedState
					switch typeName {
					case "completed":
						id = completeState
					case "canceled":
						id = canceledState
					}
					return workflowResponse(workflowState{ID: id, Name: typeName, Type: typeName, Position: 1})
				case "ReadIssue":
					return issueResponse(test.observed, test.observedAt)
				case "UpdateIssueState":
					input := request.input(t)
					if input["stateId"] == "" {
						t.Fatalf("state update input = %#v", input)
					}
					typeName, _ := workflowType(test.disposition)
					id := input["stateId"].(string)
					return gqlData(map[string]any{"issueUpdate": map[string]any{
						"success": true, "issue": issueMap(workflowState{ID: id, Name: typeName, Type: typeName}, writtenLease),
					}})
				default:
					t.Fatalf("operation = %q", request.OperationName)
					return testResponse{}
				}
			})
			payload, _ := json.Marshal(statePayload{Disposition: test.disposition})
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "state", Ref: "BDS-124", Lease: test.lease, Payload: payload,
			})
			if err != nil || result.Outcome != test.want || result.Lease != test.wantLease {
				t.Fatalf("result = %+v, err = %v; want %s lease %q", result, err, test.want, test.wantLease)
			}
			wrote := len(operations) == 3 && operations[2] == "UpdateIssueState"
			if wrote != test.wantWrite {
				t.Fatalf("operations = %v, wantWrite = %t", operations, test.wantWrite)
			}
		})
	}
}

func TestReadStateMapsWorkflowTypes(t *testing.T) {
	for _, test := range []struct {
		stateType string
		want      tracker.LiveClass
	}{
		{stateType: "triage", want: tracker.LiveBacklog},
		{stateType: "backlog", want: tracker.LiveBacklog},
		{stateType: "unstarted", want: tracker.LiveBacklog},
		{stateType: "started", want: tracker.LiveActive},
		{stateType: "completed", want: tracker.LiveCompleted},
		{stateType: "canceled", want: tracker.LiveCanceled},
		{stateType: "duplicate", want: tracker.LiveTerminal},
		{stateType: "future", want: tracker.LiveTerminal},
	} {
		t.Run(test.stateType, func(t *testing.T) {
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				if request.OperationName != "ReadIssue" {
					t.Fatalf("operation = %q", request.OperationName)
				}
				return issueResponse(workflowState{ID: startedState, Name: "Visible name", Type: test.stateType}, "lease")
			})
			got, err := adapter.ReadState(context.Background(), "BDS-124")
			want := tracker.LiveState{Class: test.want, Display: "Visible name", Lease: "lease"}
			if err != nil || got != want {
				t.Fatalf("ReadState = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestReadStateRejectsMalformedOrDifferentTeams(t *testing.T) {
	for _, test := range []struct {
		name   string
		teamID string
	}{
		{name: "malformed team UUID", teamID: "not-a-uuid"},
		{name: "different team", teamID: "99999999-9999-4999-8999-999999999999"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				if request.OperationName != "ReadIssue" {
					t.Fatalf("operation = %q", request.OperationName)
				}
				item := issueMap(workflowState{ID: startedState, Name: "Doing", Type: "started"}, "lease")
				item["team"] = map[string]any{"id": test.teamID}
				return gqlData(map[string]any{"issue": item})
			})
			got, err := adapter.ReadState(context.Background(), "BDS-124")
			if got != (tracker.LiveState{}) || err == nil {
				t.Fatalf("ReadState = %+v, %v", got, err)
			}
		})
	}
}

func TestValidationAndTokenPrecedenceRequireNoNetwork(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	})}
	if _, err := New(tracker.FactoryInput{Target: "not-a-uuid"}, Options{Token: "token", Client: client}); err == nil || called {
		t.Fatalf("malformed target: err = %v, called = %t", err, called)
	}
	t.Setenv("WIP_LINEAR_TOKEN", "wip-token")
	t.Setenv("LINEAR_API_KEY", "linear-key")
	adapter, err := New(tracker.FactoryInput{Target: testTeam}, Options{Token: "option-token", Client: client})
	if err != nil || adapter.token != "option-token" {
		t.Fatalf("adapter token = %q, err = %v", adapter.token, err)
	}
	adapter, err = New(tracker.FactoryInput{Target: testTeam}, Options{Client: client})
	if err != nil || adapter.token != "wip-token" {
		t.Fatalf("adapter token = %q, err = %v", adapter.token, err)
	}
}

func TestInvalidReferencesAndPayloadsAreRefusedBeforeRequest(t *testing.T) {
	called := false
	adapter := newTestAdapter(t, func(*testing.T, gqlTestRequest) testResponse {
		called = true
		return testResponse{}
	})
	entries := []store.OutboxEntry{
		{Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{`)},
		{Kind: "comment", Ref: "not-an-issue", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`)},
		{Kind: "state", Ref: "not-an-issue", Payload: json.RawMessage(`{"disposition":"active"}`)},
	}
	for _, entry := range entries {
		result, err := adapter.Deliver(context.Background(), entry)
		if err != nil || result.Outcome != tracker.PermanentRefusal {
			t.Fatalf("entry = %+v, result = %+v, err = %v", entry, result, err)
		}
	}
	if called {
		t.Fatal("invalid entry made a request")
	}
}

type gqlTestRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName"`
}

func (r gqlTestRequest) stringVariable(name string) string {
	value, _ := r.Variables[name].(string)
	return value
}

func (r gqlTestRequest) input(t *testing.T) map[string]any {
	t.Helper()
	input, ok := r.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables = %#v", r.Variables)
	}
	return input
}

type testResponse struct {
	status int
	body   string
	header http.Header
}

func newTestAdapter(t *testing.T, handler func(*testing.T, gqlTestRequest) testResponse) *Adapter {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://api.linear.test/graphql" {
			t.Fatalf("request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "test-token" || strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		var decoded gqlTestRequest
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		provided := handler(t, decoded)
		status := provided.status
		if status == 0 {
			status = http.StatusOK
		}
		recorder := httptest.NewRecorder()
		for name, values := range provided.header {
			for _, value := range values {
				recorder.Header().Add(name, value)
			}
		}
		recorder.WriteHeader(status)
		_, _ = io.WriteString(recorder, provided.body)
		return recorder.Result(), nil
	})}
	adapter, err := New(tracker.FactoryInput{Target: testTeam}, Options{
		BaseURL: "https://api.linear.test/graphql", Token: "test-token", Client: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func gqlData(data any) testResponse {
	encoded, _ := json.Marshal(map[string]any{"data": data})
	return testResponse{status: http.StatusOK, body: string(encoded)}
}

func gqlErrors(status int, code, message string) testResponse {
	encoded, _ := json.Marshal(map[string]any{"errors": []any{map[string]any{
		"message": message, "extensions": map[string]any{"code": code},
	}}})
	return testResponse{status: status, body: string(encoded)}
}

func foundIssueResponse(id string) testResponse {
	return gqlData(map[string]any{"issues": map[string]any{"nodes": []any{map[string]any{
		"id": id, "identifier": "BDS-124", "team": map[string]any{"id": testTeam},
	}}}})
}

func workflowResponse(states ...workflowState) testResponse {
	return gqlData(map[string]any{"team": map[string]any{"states": map[string]any{"nodes": states}}})
}

func issueResponse(state workflowState, lease string) testResponse {
	return gqlData(map[string]any{"issue": issueMap(state, lease)})
}

func issueMap(state workflowState, lease string) map[string]any {
	return map[string]any{
		"id": startedState, "identifier": "BDS-124", "updatedAt": lease,
		"team": map[string]any{"id": testTeam}, "state": state,
	}
}

func assertUUIDv4(t *testing.T, id string) {
	t.Helper()
	if !validUUID(id) || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("id = %q, want UUIDv4 shape", id)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
