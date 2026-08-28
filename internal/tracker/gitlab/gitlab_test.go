package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const testRemote = "gitlab.test/group/sub/project"

const testRef = "https://gitlab.test/group/sub/project/-/work_items/7"

const projectPath = "/api/v4/projects/group%2Fsub%2Fproject"

func TestCreateDeduplicatesHalfFailedReplay(t *testing.T) {
	var created []issue
	posts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.EscapedPath() != projectPath+"/issues" {
			t.Fatalf("path = %q", r.URL.String())
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("per_page") != "100" {
				t.Fatalf("query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(created)
		case http.MethodPost:
			posts++
			var request struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Title != "Keep the cursor visible" ||
				!strings.Contains(request.Description, "Source: agent") ||
				!strings.Contains(request.Description, "<!-- wip-idempotency:") {
				t.Fatalf("create request = %+v", request)
			}
			created = append(created, issue{Description: request.Description, WebURL: testRef})
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(created[0])
		default:
			t.Fatalf("method = %s", r.Method)
		}
	})

	adapter := newTestAdapter(t, handler)
	entry := store.OutboxEntry{
		Kind: "create", IdempotencyKey: "backlog:01ABC",
		Payload: json.RawMessage(`{"title":"Keep the cursor visible","detail":"Show in-progress work.","provenance":"agent"}`),
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := adapter.Deliver(context.Background(), entry)
		want := tracker.Delivered
		if attempt > 0 {
			want = tracker.Converged
		}
		if err != nil || result.Outcome != want || result.Ref != testRef {
			t.Fatalf("attempt %d result = %+v, err = %v; want outcome %s", attempt+1, result, err, want)
		}
	}
	if posts != 1 {
		t.Fatalf("create POSTs = %d, want 1", posts)
	}
}

func TestCommentDeduplicatesHalfFailedReplay(t *testing.T) {
	notes := []note{{Body: idempotencyMarker("tracker:event:comment:ref:stage"), System: true}}
	posts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.EscapedPath() != projectPath+"/issues/7/notes" {
			t.Fatalf("path = %q", r.URL.String())
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(notes)
		case http.MethodPost:
			posts++
			var request struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(request.Body, `Stage "Provider API" closed.`) || !strings.Contains(request.Body, "<!-- wip-idempotency:") {
				t.Fatalf("comment body = %q", request.Body)
			}
			notes = append(notes, note{Body: request.Body})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	})

	adapter := newTestAdapter(t, handler)
	entry := store.OutboxEntry{
		Kind: "comment", Ref: testRef, IdempotencyKey: "tracker:event:comment:ref:stage",
		Payload: json.RawMessage(`{"stage":"01ABC","title":"Provider API","action":"closed"}`),
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := adapter.Deliver(context.Background(), entry)
		want := tracker.Delivered
		if attempt > 0 {
			want = tracker.Converged
		}
		if err != nil || result.Outcome != want {
			t.Fatalf("attempt %d result = %+v, err = %v; want outcome %s", attempt+1, result, err, want)
		}
	}
	if posts != 1 {
		t.Fatalf("comment POSTs = %d, want 1", posts)
	}
}

func TestStateDeliveryGuardsEveryWriteWithAnImmediateRead(t *testing.T) {
	const oldLease = "2026-08-20T12:34:56Z"
	const observedLease = "2026-08-20T12:35:00Z"
	const writtenLease = "2026-08-20T12:36:00Z"
	for _, test := range []struct {
		name        string
		disposition store.TrackerDisposition
		lease       string
		observed    issue
		wantOutcome tracker.Outcome
		wantLease   string
		wantMethods string
		wantBody    string
		wantReason  []string
	}{
		{
			name: "matching lease advances active to completed", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "opened", UpdatedAt: oldLease},
			wantOutcome: tracker.Delivered, wantLease: writtenLease, wantMethods: "GET,PUT",
			wantBody: `{"state_event":"close"}`,
		},
		{
			name: "matching lease already active", disposition: store.TrackerActive,
			lease: oldLease, observed: issue{State: "opened", UpdatedAt: oldLease},
			wantOutcome: tracker.Converged, wantLease: oldLease, wantMethods: "GET",
		},
		{
			name: "matching lease already completed", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "closed", UpdatedAt: oldLease},
			wantOutcome: tracker.Converged, wantLease: oldLease, wantMethods: "GET",
		},
		{
			name: "matching lease already canceled", disposition: store.TrackerCanceled,
			lease: oldLease, observed: issue{State: "closed", UpdatedAt: oldLease},
			wantOutcome: tracker.Converged, wantLease: oldLease, wantMethods: "GET",
		},
		{
			name: "changed lease already at candidate", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "closed", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "changed lease beyond active candidate", disposition: store.TrackerActive,
			lease: oldLease, observed: issue{State: "closed", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "matching lease never reopens terminal issue", disposition: store.TrackerActive,
			lease: observedLease, observed: issue{State: "closed", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "changed lease elsewhere", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "opened", UpdatedAt: observedLease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{`state "opened"`, observedLease},
		},
		{
			name: "first push converges on active", disposition: store.TrackerActive,
			observed:    issue{State: "opened", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "first push advances active", disposition: store.TrackerCanceled,
			observed:    issue{State: "opened", UpdatedAt: observedLease},
			wantOutcome: tracker.Delivered, wantLease: writtenLease, wantMethods: "GET,PUT",
			wantBody: `{"state_event":"close"}`,
		},
		{
			name: "first push never asserts over terminal", disposition: store.TrackerCompleted,
			observed:    issue{State: "closed", UpdatedAt: observedLease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{`state "closed"`, observedLease},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.URL.EscapedPath() != projectPath+"/issues/7" {
					t.Fatalf("path = %q", r.URL.String())
				}
				methods = append(methods, r.Method)
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(test.observed)
				case http.MethodPut:
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if string(body) != test.wantBody {
						t.Fatalf("PUT body = %s, want %s", body, test.wantBody)
					}
					update, err := stateUpdate(test.disposition, "")
					if err != nil {
						t.Fatal(err)
					}
					state := "closed"
					if update.StateEvent == "reopen" {
						state = "opened"
					}
					_ = json.NewEncoder(w).Encode(issue{State: state, UpdatedAt: writtenLease})
				default:
					t.Fatalf("method = %s", r.Method)
				}
			})
			payload, err := json.Marshal(statePayload{Disposition: test.disposition})
			if err != nil {
				t.Fatal(err)
			}
			result, err := newTestAdapter(t, handler).Deliver(context.Background(), store.OutboxEntry{
				Kind: "state", Ref: testRef, Lease: test.lease, Payload: payload,
			})
			if err != nil || result.Outcome != test.wantOutcome || result.Lease != test.wantLease {
				t.Fatalf("result = %+v, err = %v; want outcome %s lease %q", result, err, test.wantOutcome, test.wantLease)
			}
			if got := strings.Join(methods, ","); got != test.wantMethods {
				t.Fatalf("request methods = %q, want %q", got, test.wantMethods)
			}
			for _, fragment := range test.wantReason {
				if !strings.Contains(result.Reason, fragment) {
					t.Fatalf("reason = %q, want fragment %q", result.Reason, fragment)
				}
			}
		})
	}
}

func TestStateDeliveryWithCanceledLabel(t *testing.T) {
	const lease = "2026-08-20T12:34:56Z"
	const writtenLease = "2026-08-20T12:36:00Z"
	const canceledLabel = "wf::canceled"

	for _, test := range []struct {
		name        string
		disposition store.TrackerDisposition
		observed    issue
		labels      []label
		wantOutcome tracker.Outcome
		wantLease   string
		wantMethods string
		wantPUTBody string
		wantWritten issue
		wantReason  []string
	}{
		{
			name: "canceled delivers close with label when it exists", disposition: store.TrackerCanceled,
			observed:    issue{State: "opened", UpdatedAt: lease},
			labels:      []label{{Name: "wf::in-progress"}, {Name: canceledLabel}},
			wantOutcome: tracker.Delivered, wantLease: writtenLease, wantMethods: "GET,GET,PUT",
			wantPUTBody: `{"state_event":"close","add_labels":"wf::canceled"}`,
			wantWritten: issue{State: "closed", Labels: []string{canceledLabel}, UpdatedAt: writtenLease},
		},
		{
			name: "canceled refuses when label search has no exact match", disposition: store.TrackerCanceled,
			observed:    issue{State: "opened", UpdatedAt: lease},
			labels:      []label{{Name: "wf::canceled-old"}},
			wantOutcome: tracker.PermanentRefusal, wantMethods: "GET,GET",
			wantReason: []string{canceledLabel, "tracker.canceled-label"},
		},
		{
			name: "completed candidate does not match closed with label", disposition: store.TrackerCompleted,
			observed:    issue{State: "closed", Labels: []string{canceledLabel}, UpdatedAt: lease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{canceledLabel},
		},
		{
			name: "canceled candidate does not match closed without label", disposition: store.TrackerCanceled,
			observed:    issue{State: "closed", UpdatedAt: lease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
		},
		{
			name: "canceled candidate converges on closed with label", disposition: store.TrackerCanceled,
			observed:    issue{State: "closed", Labels: []string{canceledLabel}, UpdatedAt: lease},
			wantOutcome: tracker.Converged, wantLease: lease, wantMethods: "GET",
		},
		{
			name: "completed candidate converges on closed without label", disposition: store.TrackerCompleted,
			observed:    issue{State: "closed", UpdatedAt: lease},
			wantOutcome: tracker.Converged, wantLease: lease, wantMethods: "GET",
		},
		{
			name: "completed delivers close without add_labels", disposition: store.TrackerCompleted,
			observed:    issue{State: "opened", UpdatedAt: lease},
			wantOutcome: tracker.Delivered, wantLease: writtenLease, wantMethods: "GET,PUT",
			wantPUTBody: `{"state_event":"close"}`,
			wantWritten: issue{State: "closed", UpdatedAt: writtenLease},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				switch r.URL.EscapedPath() {
				case projectPath + "/issues/7":
					methods = append(methods, r.Method)
					switch r.Method {
					case http.MethodGet:
						_ = json.NewEncoder(w).Encode(test.observed)
					case http.MethodPut:
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Fatal(err)
						}
						if string(body) != test.wantPUTBody {
							t.Fatalf("PUT body = %s, want %s", body, test.wantPUTBody)
						}
						_ = json.NewEncoder(w).Encode(test.wantWritten)
					default:
						t.Fatalf("method = %s", r.Method)
					}
				case projectPath + "/labels":
					methods = append(methods, r.Method)
					if r.URL.Query().Get("search") != canceledLabel {
						t.Fatalf("search = %q", r.URL.Query().Get("search"))
					}
					_ = json.NewEncoder(w).Encode(test.labels)
				default:
					t.Fatalf("path = %q", r.URL.String())
				}
			})
			payload, err := json.Marshal(statePayload{Disposition: test.disposition})
			if err != nil {
				t.Fatal(err)
			}
			result, err := newTestAdapterWithLabel(t, handler, canceledLabel).Deliver(context.Background(), store.OutboxEntry{
				Kind: "state", Ref: testRef, Lease: lease, Payload: payload,
			})
			if err != nil || result.Outcome != test.wantOutcome || result.Lease != test.wantLease {
				t.Fatalf("result = %+v, err = %v; want outcome %s lease %q", result, err, test.wantOutcome, test.wantLease)
			}
			if got := strings.Join(methods, ","); got != test.wantMethods {
				t.Fatalf("request methods = %q, want %q", got, test.wantMethods)
			}
			for _, fragment := range test.wantReason {
				if !strings.Contains(result.Reason, fragment) {
					t.Fatalf("reason = %q, want fragment %q", result.Reason, fragment)
				}
			}
		})
	}
}

func TestStateDeliveryRejectsMalformedCandidatesBeforeReading(t *testing.T) {
	for _, entry := range []store.OutboxEntry{
		{Kind: "state", Ref: testRef, Payload: json.RawMessage(`{"disposition":`)},
		{Kind: "state", Ref: testRef, Payload: json.RawMessage(`{"disposition":"unknown"}`)},
		{Kind: "state", Ref: "https://other.test/group/sub/project/-/issues/7", Payload: json.RawMessage(`{"disposition":"active"}`)},
		{Kind: "state", Ref: "https://gitlab.test/other/project/-/issues/7", Payload: json.RawMessage(`{"disposition":"active"}`)},
		{Kind: "state", Ref: "https://gitlab.test/group/sub/project/-/issues/0", Payload: json.RawMessage(`{"disposition":"active"}`)},
	} {
		called := false
		adapter := newTestAdapter(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		result, err := adapter.Deliver(context.Background(), entry)
		if err != nil || result.Outcome != tracker.PermanentRefusal || called {
			t.Fatalf("entry = %+v, result = %+v, err = %v, called = %t", entry, result, err, called)
		}
	}
}

func TestStateDeliveryHTTPFailuresUseExistingClassification(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		headers http.Header
		want    tracker.Outcome
	}{
		{name: "ordinary unprocessable", status: http.StatusUnprocessableEntity, want: tracker.PermanentRefusal},
		{name: "retry after forbidden", status: http.StatusForbidden, headers: http.Header{"Retry-After": {"60"}}, want: tracker.RetryableFailure},
		{name: "rate limit remaining zero forbidden", status: http.StatusForbidden, headers: http.Header{"RateLimit-Remaining": {"0"}}, want: tracker.RetryableFailure},
		{name: "request timeout", status: http.StatusRequestTimeout, want: tracker.RetryableFailure},
		{name: "too many requests", status: http.StatusTooManyRequests, want: tracker.RetryableFailure},
		{name: "internal server failure", status: http.StatusInternalServerError, want: tracker.RetryableFailure},
		{name: "service unavailable", status: http.StatusServiceUnavailable, want: tracker.RetryableFailure},
	} {
		for _, failureMethod := range []string{http.MethodGet, http.MethodPut} {
			t.Run(test.name+"/"+failureMethod, func(t *testing.T) {
				var methods []string
				adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assertHeaders(t, r)
					if r.URL.EscapedPath() != projectPath+"/issues/7" {
						t.Fatalf("path = %q", r.URL.String())
					}
					methods = append(methods, r.Method)
					if failureMethod == http.MethodPut && r.Method == http.MethodGet {
						_, _ = w.Write([]byte(`{"state":"opened","updated_at":"old-lease"}`))
						return
					}
					if r.Method != failureMethod {
						t.Fatalf("method = %s, want failure on %s", r.Method, failureMethod)
					}
					for name, values := range test.headers {
						for _, value := range values {
							w.Header().Add(name, value)
						}
					}
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(`{"message":"provider response"}`))
				}))

				result, err := adapter.Deliver(context.Background(), stateTestEntry())
				if err != nil || result.Outcome != test.want || !strings.Contains(result.Reason, "HTTP "+strconv.Itoa(test.status)) {
					t.Fatalf("result = %+v, err = %v; want outcome %s", result, err, test.want)
				}
				wantMethods := http.MethodGet
				if failureMethod == http.MethodPut {
					wantMethods += "," + http.MethodPut
				}
				if got := strings.Join(methods, ","); got != wantMethods {
					t.Fatalf("request methods = %q, want %q", got, wantMethods)
				}
			})
		}
	}
}

func TestStateDeliveryTransportAndDecodeErrorsRemainReturnedErrors(t *testing.T) {
	transportFailure := errors.New("connection reset")
	for _, test := range []struct {
		name          string
		failureMethod string
		transport     bool
	}{
		{name: "read transport error", failureMethod: http.MethodGet, transport: true},
		{name: "write transport error", failureMethod: http.MethodPut, transport: true},
		{name: "malformed read JSON", failureMethod: http.MethodGet},
		{name: "malformed write JSON", failureMethod: http.MethodPut},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			adapter := newTestAdapterWithTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				assertHeaders(t, request)
				if request.URL.EscapedPath() != projectPath+"/issues/7" {
					t.Fatalf("path = %q", request.URL.String())
				}
				methods = append(methods, request.Method)
				if test.failureMethod == http.MethodPut && request.Method == http.MethodGet {
					return testHTTPResponse(http.StatusOK, `{"state":"opened","updated_at":"old-lease"}`), nil
				}
				if request.Method != test.failureMethod {
					t.Fatalf("method = %s, want failure on %s", request.Method, test.failureMethod)
				}
				if test.transport {
					return nil, transportFailure
				}
				return testHTTPResponse(http.StatusOK, `{`), nil
			}))

			result, err := adapter.Deliver(context.Background(), stateTestEntry())
			if result != (tracker.Result{}) {
				t.Fatalf("result = %+v, want zero result", result)
			}
			if test.transport {
				if !errors.Is(err, transportFailure) {
					t.Fatalf("error = %v, want transport failure", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "decode response") {
				t.Fatalf("error = %v, want decode response error", err)
			}
			wantMethods := http.MethodGet
			if test.failureMethod == http.MethodPut {
				wantMethods += "," + http.MethodPut
			}
			if got := strings.Join(methods, ","); got != wantMethods {
				t.Fatalf("request methods = %q, want %q", got, wantMethods)
			}
		})
	}
}

func TestStateDeliveryRejectsMalformedSuccessfulResponses(t *testing.T) {
	for _, test := range []struct {
		name          string
		failureMethod string
		body          string
		wantReason    string
	}{
		{name: "read has no state", failureMethod: http.MethodGet, body: `{"updated_at":"old-lease"}`, wantReason: "issue read returned malformed state or updated_at"},
		{name: "read has GitHub-spelled state", failureMethod: http.MethodGet, body: `{"state":"open","updated_at":"old-lease"}`, wantReason: "issue read returned malformed state or updated_at"},
		{name: "read has no lease", failureMethod: http.MethodGet, body: `{"state":"opened"}`, wantReason: "issue read returned malformed state or updated_at"},
		{name: "write has no lease", failureMethod: http.MethodPut, body: `{"state":"closed"}`, wantReason: "state response has no updated_at lease"},
		{name: "write has no state", failureMethod: http.MethodPut, body: `{"updated_at":"new-lease"}`, wantReason: "state response does not match requested state"},
		{name: "write remains opened for terminal candidate", failureMethod: http.MethodPut, body: `{"state":"opened","updated_at":"new-lease"}`, wantReason: "state response does not match requested state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.URL.EscapedPath() != projectPath+"/issues/7" {
					t.Fatalf("path = %q", r.URL.String())
				}
				methods = append(methods, r.Method)
				if test.failureMethod == http.MethodPut && r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"state":"opened","updated_at":"old-lease"}`))
					return
				}
				if r.Method != test.failureMethod {
					t.Fatalf("method = %s, want malformed response on %s", r.Method, test.failureMethod)
				}
				_, _ = w.Write([]byte(test.body))
			}))

			result, err := adapter.Deliver(context.Background(), stateTestEntry())
			if err != nil || result.Outcome != tracker.PermanentRefusal || !strings.Contains(result.Reason, test.wantReason) {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			wantMethods := http.MethodGet
			if test.failureMethod == http.MethodPut {
				wantMethods += "," + http.MethodPut
			}
			if got := strings.Join(methods, ","); got != wantMethods {
				t.Fatalf("request methods = %q, want %q", got, wantMethods)
			}
		})
	}
}

func TestReadStateMapsGitLabLifecycle(t *testing.T) {
	const lease = "2026-08-20T12:34:56Z"
	for _, test := range []struct {
		name        string
		label       string
		state       string
		labels      []string
		wantClass   tracker.LiveClass
		wantDisplay string
	}{
		{name: "opened, no label configured", state: "opened", wantClass: tracker.LiveNonterminal, wantDisplay: "opened"},
		{name: "closed, no label configured", state: "closed", wantClass: tracker.LiveCompleted, wantDisplay: "closed"},
		{
			name: "closed with labels, no label configured", state: "closed", labels: []string{"wf::canceled"},
			wantClass: tracker.LiveCompleted, wantDisplay: "closed",
		},
		{
			name: "closed with matching label configured", label: "wf::canceled", state: "closed", labels: []string{"wf::canceled"},
			wantClass: tracker.LiveCanceled, wantDisplay: "closed (wf::canceled)",
		},
		{
			name: "closed without configured label present", label: "wf::canceled", state: "closed",
			wantClass: tracker.LiveCompleted, wantDisplay: "closed",
		},
		{name: "opened with label configured", label: "wf::canceled", state: "opened", wantClass: tracker.LiveNonterminal, wantDisplay: "opened"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.Method != http.MethodGet || r.URL.EscapedPath() != projectPath+"/issues/7" {
					t.Fatalf("request = %s %s", r.Method, r.URL.String())
				}
				_ = json.NewEncoder(w).Encode(issue{State: test.state, Labels: test.labels, UpdatedAt: lease})
			})
			adapter := newTestAdapterWithLabel(t, handler, test.label)
			got, err := adapter.ReadState(context.Background(), testRef)
			want := tracker.LiveState{Class: test.wantClass, Display: test.wantDisplay, Lease: lease}
			if err != nil || got != want {
				t.Fatalf("ReadState() = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestReadStateRejectsInvalidReferencesBeforeRequest(t *testing.T) {
	for _, ref := range []string{
		"https://gitlab.test/group/sub/project/-/issues/abc",
		"https://other.test/group/sub/project/-/issues/7",
		"https://github.com/group/sub/project/-/issues/7",
	} {
		t.Run(ref, func(t *testing.T) {
			called := false
			adapter := newTestAdapter(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			got, err := adapter.ReadState(context.Background(), ref)
			if got != (tracker.LiveState{}) || err == nil || called {
				t.Fatalf("ReadState(%q) = %+v, %v; request called = %t", ref, got, err, called)
			}
		})
	}
}

func TestReadStateReturnsTransportAndAPIErrors(t *testing.T) {
	adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Project Not Found", http.StatusNotFound)
	}))
	got, err := adapter.ReadState(context.Background(), testRef)
	if got != (tracker.LiveState{}) || err == nil ||
		!strings.Contains(err.Error(), "check the token has api scope") ||
		!strings.Contains(err.Error(), "group/sub/project") {
		t.Fatalf("ReadState() = %+v, %v", got, err)
	}
}

func TestHTTPFailuresAreClassified(t *testing.T) {
	for _, test := range []struct {
		status int
		want   tracker.Outcome
	}{
		{http.StatusServiceUnavailable, tracker.RetryableFailure},
		{http.StatusTooManyRequests, tracker.RetryableFailure},
		{http.StatusRequestTimeout, tracker.RetryableFailure},
		{http.StatusUnprocessableEntity, tracker.PermanentRefusal},
		{http.StatusUnauthorized, tracker.PermanentRefusal},
		{http.StatusNotFound, tracker.PermanentRefusal},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "provider response", test.status)
			})
			adapter := newTestAdapter(t, handler)
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`),
			})
			if err != nil || result.Outcome != test.want {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if test.status == http.StatusNotFound && !strings.Contains(result.Reason, "check the token has api scope") {
				t.Fatalf("reason = %q, want scope hint", result.Reason)
			}
		})
	}
}

func TestRateLimitHeadersAreRetryable(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers http.Header
		want    tracker.Outcome
	}{
		{name: "retry after", headers: http.Header{"Retry-After": {"60"}}, want: tracker.RetryableFailure},
		{name: "rate limit exhausted", headers: http.Header{"RateLimit-Remaining": {"0"}}, want: tracker.RetryableFailure},
		{name: "rate limit remaining", headers: http.Header{"RateLimit-Remaining": {"5"}}, want: tracker.PermanentRefusal},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, values := range test.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"forbidden"}`))
			})
			adapter := newTestAdapter(t, handler)
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "create", IdempotencyKey: "key", Payload: json.RawMessage(`{"title":"Title"}`),
			})
			if err != nil || result.Outcome != test.want {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
		})
	}
}

func TestProjectRemoteParsing(t *testing.T) {
	for _, test := range []struct {
		remote   string
		wantHost string
		wantPath string
	}{
		{remote: "gitlab.test/group/sub/project", wantHost: "gitlab.test", wantPath: "group/sub/project"},
		{remote: "gitlab.test:2222/group/sub/project", wantHost: "gitlab.test", wantPath: "group/sub/project"},
		{remote: "git@gitlab.test:group/sub/project.git", wantHost: "gitlab.test", wantPath: "group/sub/project"},
		{remote: "https://gitlab.test/group/sub/project.git", wantHost: "gitlab.test", wantPath: "group/sub/project"},
		{remote: "ssh://git@gitlab.test:2222/group/sub/project", wantHost: "gitlab.test", wantPath: "group/sub/project"},
		{remote: "GitLab.Test/Group/Sub/Project", wantHost: "gitlab.test", wantPath: "Group/Sub/Project"},
	} {
		t.Run(test.remote, func(t *testing.T) {
			host, path, err := project(test.remote)
			if err != nil || host != test.wantHost || path != test.wantPath {
				t.Fatalf("project(%q) = %q, %q, %v; want %q, %q", test.remote, host, path, err, test.wantHost, test.wantPath)
			}
		})
	}
	for _, remote := range []string{
		"gitlab.test/project",
		"github.com/acme/widget",
		"gitlab.test",
		"",
	} {
		t.Run("reject "+remote, func(t *testing.T) {
			if _, _, err := project(remote); err == nil {
				t.Fatalf("project(%q) succeeded, want error", remote)
			}
		})
	}
}

func TestIssueReferenceParsing(t *testing.T) {
	adapter := &Adapter{host: "gitlab.test", project: "group/sub/project"}
	for _, test := range []struct {
		ref  string
		want int
	}{
		{ref: "https://gitlab.test/group/sub/project/-/issues/7", want: 7},
		{ref: "https://gitlab.test/group/sub/project/-/work_items/7", want: 7},
		{ref: "https://GitLab.Test/Group/Sub/Project/-/issues/7", want: 7},
	} {
		t.Run(test.ref, func(t *testing.T) {
			got, err := adapter.issueReference(test.ref)
			if err != nil || got != test.want {
				t.Fatalf("issueReference(%q) = %d, %v; want %d", test.ref, got, err, test.want)
			}
		})
	}
	for _, ref := range []string{
		"https://other.test/group/sub/project/-/issues/7",
		"https://gitlab.test/other/project/-/issues/7",
		"https://gitlab.test/group/sub/project/-/merge_requests/7",
		"https://gitlab.test/group/sub/project/-/issues/0",
		"https://gitlab.test/group/sub/project/issues/7",
	} {
		t.Run("reject "+ref, func(t *testing.T) {
			if _, err := adapter.issueReference(ref); err == nil {
				t.Fatalf("issueReference(%q) succeeded, want error", ref)
			}
		})
	}
}

func TestTokenPrecedence(t *testing.T) {
	isolateGlabEnv(t)
	for _, test := range []struct {
		name        string
		optionsTok  string
		wipEnv      string
		glEnv       string
		glabContent string
		wantToken   string
		wantErr     bool
	}{
		{name: "options wins over both envs", optionsTok: "from-options", wipEnv: "from-wip", glEnv: "from-gitlab", wantToken: "from-options"},
		{name: "WIP wins over GITLAB_TOKEN", wipEnv: "from-wip", glEnv: "from-gitlab", wantToken: "from-wip"},
		{name: "GITLAB_TOKEN used", glEnv: "from-gitlab", wantToken: "from-gitlab"},
		{name: "glab config used", wantToken: "from-glab", glabContent: "hosts:\n  gitlab.test:\n    token: from-glab\n"},
		{name: "none configured is an error", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WIP_GITLAB_TOKEN", test.wipEnv)
			t.Setenv("GITLAB_TOKEN", test.glEnv)
			if test.glabContent != "" {
				dir := writeGlabConfig(t, t.TempDir(), test.glabContent)
				t.Setenv("GLAB_CONFIG_DIR", dir)
			} else {
				t.Setenv("GLAB_CONFIG_DIR", t.TempDir())
			}
			adapter, err := New(tracker.FactoryInput{Repo: store.Repo{RemoteURL: testRemote}}, Options{Token: test.optionsTok})
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "WIP_GITLAB_TOKEN") || !strings.Contains(err.Error(), "gitlab.test") {
					t.Fatalf("err = %v, want token error", err)
				}
				return
			}
			if err != nil || adapter.token != test.wantToken {
				t.Fatalf("adapter = %+v, err = %v; want token %q", adapter, err, test.wantToken)
			}
		})
	}
}

func TestNewDefaultsBaseURLFromHost(t *testing.T) {
	for _, remote := range []string{testRemote, "gitlab.test:2222/group/sub/project"} {
		t.Run(remote, func(t *testing.T) {
			adapter, err := New(tracker.FactoryInput{Repo: store.Repo{RemoteURL: remote}}, Options{Token: "test-token"})
			if err != nil || adapter.baseURL != "https://gitlab.test/api/v4" {
				t.Fatalf("adapter = %+v, err = %v", adapter, err)
			}
		})
	}
}

func TestFactoryIgnoresProviderNeutralTarget(t *testing.T) {
	seam, err := Factory(Options{Token: "test-token"})(tracker.FactoryInput{
		Repo:   store.Repo{RemoteURL: testRemote},
		Target: "provider-owned-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := seam.(*Adapter)
	if !ok || adapter.host != "gitlab.test" || adapter.project != "group/sub/project" || adapter.projectID != "group%2Fsub%2Fproject" {
		t.Fatalf("factory seam = %#v", seam)
	}
}

func TestIssueStateWriteShapes(t *testing.T) {
	for _, test := range []struct {
		name          string
		disposition   store.TrackerDisposition
		canceledLabel string
		wantBody      string
	}{
		{name: "active", disposition: store.TrackerActive, wantBody: `{"state_event":"reopen"}`},
		{name: "completed", disposition: store.TrackerCompleted, wantBody: `{"state_event":"close"}`},
		{name: "canceled without label", disposition: store.TrackerCanceled, wantBody: `{"state_event":"close"}`},
		{
			name: "canceled with label", disposition: store.TrackerCanceled, canceledLabel: "wf::canceled",
			wantBody: `{"state_event":"close","add_labels":"wf::canceled"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			update, err := stateUpdate(test.disposition, test.canceledLabel)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(update)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != test.wantBody {
				t.Fatalf("body = %s, want %s", body, test.wantBody)
			}
		})
	}

	if _, err := stateUpdate(store.TrackerDisposition("unknown"), ""); err == nil {
		t.Fatal("unsupported disposition succeeded")
	}
}

func newTestAdapter(t *testing.T, handler http.Handler) *Adapter {
	t.Helper()
	return newTestAdapterWithTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	}))
}

func newTestAdapterWithTransport(t *testing.T, transport http.RoundTripper) *Adapter {
	t.Helper()
	adapter, err := New(tracker.FactoryInput{Repo: store.Repo{RemoteURL: testRemote}}, Options{
		BaseURL: "https://api.gitlab.test/api/v4",
		Token:   "test-token",
		Client:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func newTestAdapterWithLabel(t *testing.T, handler http.Handler, label string) *Adapter {
	t.Helper()
	adapter, err := New(tracker.FactoryInput{
		Repo:          store.Repo{RemoteURL: testRemote},
		CanceledLabel: label,
	}, Options{
		BaseURL: "https://api.gitlab.test/api/v4",
		Token:   "test-token",
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			return recorder.Result(), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func stateTestEntry() store.OutboxEntry {
	return store.OutboxEntry{
		Kind: "state", Ref: testRef, Lease: "old-lease",
		Payload: json.RawMessage(`{"disposition":"completed"}`),
	}
}

func testHTTPResponse(status int, body string) *http.Response {
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(status)
	_, _ = recorder.WriteString(body)
	return recorder.Result()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func assertHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("PRIVATE-TOKEN") != "test-token" || r.Header.Get("User-Agent") != "wip-tracker" {
		t.Fatalf("headers = %#v", r.Header)
	}
}
