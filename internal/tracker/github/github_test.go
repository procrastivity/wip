package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

func TestCreateDeduplicatesHalfFailedReplay(t *testing.T) {
	var created []issue
	posts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.Path != "/repos/acme/widget/issues" {
			t.Fatalf("path = %q", r.URL.String())
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(created)
		case http.MethodPost:
			posts++
			var request struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Title != "Keep the cursor visible" || !strings.Contains(request.Body, "<!-- wip-idempotency:") {
				t.Fatalf("create request = %+v", request)
			}
			created = append(created, issue{Body: request.Body, HTMLURL: "https://github.com/acme/widget/issues/17"})
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
		if err != nil || result.Outcome != want || result.Ref != "https://github.com/acme/widget/issues/17" {
			t.Fatalf("attempt %d result = %+v, err = %v; want outcome %s", attempt+1, result, err, want)
		}
	}
	if posts != 1 {
		t.Fatalf("create POSTs = %d, want 1", posts)
	}
}

func TestCommentDeduplicatesHalfFailedReplay(t *testing.T) {
	var comments []comment
	posts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.Path != "/repos/acme/widget/issues/17/comments" {
			t.Fatalf("path = %q", r.URL.String())
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(comments)
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
			comments = append(comments, comment{Body: request.Body})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	})

	adapter := newTestAdapter(t, handler)
	entry := store.OutboxEntry{
		Kind: "comment", Ref: "https://github.com/acme/widget/issues/17", IdempotencyKey: "tracker:event:comment:ref:stage",
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
			lease: oldLease, observed: issue{State: "open", UpdatedAt: oldLease},
			wantOutcome: tracker.Delivered, wantLease: writtenLease, wantMethods: "GET,PATCH",
			wantBody: `{"state":"closed","state_reason":"completed"}`,
		},
		{
			name: "matching lease already active", disposition: store.TrackerActive,
			lease: oldLease, observed: issue{State: "open", UpdatedAt: oldLease},
			wantOutcome: tracker.Converged, wantLease: oldLease, wantMethods: "GET",
		},
		{
			name: "matching lease already completed", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "closed", StateReason: "completed", UpdatedAt: oldLease},
			wantOutcome: tracker.Converged, wantLease: oldLease, wantMethods: "GET",
		},
		{
			name: "matching lease already canceled", disposition: store.TrackerCanceled,
			lease: oldLease, observed: issue{State: "closed", StateReason: "not_planned", UpdatedAt: oldLease},
			wantOutcome: tracker.Converged, wantLease: oldLease, wantMethods: "GET",
		},
		{
			name: "changed lease already at candidate", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "closed", StateReason: "completed", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "changed lease beyond active candidate", disposition: store.TrackerActive,
			lease: oldLease, observed: issue{State: "closed", StateReason: "not_planned", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "matching lease never reopens terminal issue", disposition: store.TrackerActive,
			lease: observedLease, observed: issue{State: "closed", StateReason: "completed", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "changed lease elsewhere", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "open", UpdatedAt: observedLease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{`state "open"`, observedLease},
		},
		{
			name: "changed lease at competing terminal", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "closed", StateReason: "not_planned", UpdatedAt: observedLease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{`state "closed"`, `state_reason "not_planned"`, observedLease},
		},
		{
			name: "matching lease at competing terminal", disposition: store.TrackerCompleted,
			lease: oldLease, observed: issue{State: "closed", StateReason: "not_planned", UpdatedAt: oldLease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{`state "closed"`, `state_reason "not_planned"`, oldLease},
		},
		{
			name: "first push converges on active", disposition: store.TrackerActive,
			observed:    issue{State: "open", UpdatedAt: observedLease},
			wantOutcome: tracker.Converged, wantLease: observedLease, wantMethods: "GET",
		},
		{
			name: "first push advances active", disposition: store.TrackerCanceled,
			observed:    issue{State: "open", UpdatedAt: observedLease},
			wantOutcome: tracker.Delivered, wantLease: writtenLease, wantMethods: "GET,PATCH",
			wantBody: `{"state":"closed","state_reason":"not_planned"}`,
		},
		{
			name: "first push never asserts over terminal", disposition: store.TrackerCompleted,
			observed:    issue{State: "closed", StateReason: "completed", UpdatedAt: observedLease},
			wantOutcome: tracker.LeaseMismatch, wantMethods: "GET",
			wantReason: []string{`state "closed"`, `state_reason "completed"`, observedLease},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.URL.Path != "/repos/acme/widget/issues/17" {
					t.Fatalf("path = %q", r.URL.String())
				}
				methods = append(methods, r.Method)
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(test.observed)
				case http.MethodPatch:
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if string(body) != test.wantBody {
						t.Fatalf("PATCH body = %s, want %s", body, test.wantBody)
					}
					update, err := stateUpdate(test.disposition)
					if err != nil {
						t.Fatal(err)
					}
					_ = json.NewEncoder(w).Encode(issue{State: update.State, StateReason: update.StateReason, UpdatedAt: writtenLease})
				default:
					t.Fatalf("method = %s", r.Method)
				}
			})
			payload, err := json.Marshal(statePayload{Disposition: test.disposition})
			if err != nil {
				t.Fatal(err)
			}
			result, err := newTestAdapter(t, handler).Deliver(context.Background(), store.OutboxEntry{
				Kind: "state", Ref: "https://github.com/acme/widget/issues/17", Lease: test.lease, Payload: payload,
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
		{Kind: "state", Ref: "https://github.com/acme/widget/issues/17", Payload: json.RawMessage(`{"disposition":`)},
		{Kind: "state", Ref: "https://github.com/acme/widget/issues/17", Payload: json.RawMessage(`{"disposition":"unknown"}`)},
		{Kind: "state", Ref: "not-an-issue", Payload: json.RawMessage(`{"disposition":"active"}`)},
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
		body    string
		want    tracker.Outcome
	}{
		{name: "ordinary forbidden", status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`, want: tracker.PermanentRefusal},
		{name: "primary rate limit forbidden", status: http.StatusForbidden, headers: http.Header{"X-RateLimit-Remaining": {"0"}}, want: tracker.RetryableFailure},
		{name: "retry after forbidden", status: http.StatusForbidden, headers: http.Header{"Retry-After": {"60"}}, want: tracker.RetryableFailure},
		{name: "secondary rate limit forbidden", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`, want: tracker.RetryableFailure},
		{name: "request timeout", status: http.StatusRequestTimeout, want: tracker.RetryableFailure},
		{name: "too many requests", status: http.StatusTooManyRequests, want: tracker.RetryableFailure},
		{name: "internal server failure", status: http.StatusInternalServerError, want: tracker.RetryableFailure},
		{name: "service unavailable", status: http.StatusServiceUnavailable, want: tracker.RetryableFailure},
		{name: "other client failure", status: http.StatusUnprocessableEntity, want: tracker.PermanentRefusal},
	} {
		for _, failureMethod := range []string{http.MethodGet, http.MethodPatch} {
			t.Run(test.name+"/"+failureMethod, func(t *testing.T) {
				var methods []string
				adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assertHeaders(t, r)
					if r.URL.Path != "/repos/acme/widget/issues/17" {
						t.Fatalf("path = %q", r.URL.String())
					}
					methods = append(methods, r.Method)
					if failureMethod == http.MethodPatch && r.Method == http.MethodGet {
						_, _ = w.Write([]byte(`{"state":"open","updated_at":"old-lease"}`))
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
					body := test.body
					if body == "" {
						body = `{"message":"provider response"}`
					}
					_, _ = w.Write([]byte(body))
				}))

				result, err := adapter.Deliver(context.Background(), stateTestEntry())
				if err != nil || result.Outcome != test.want || !strings.Contains(result.Reason, "HTTP "+strconv.Itoa(test.status)) {
					t.Fatalf("result = %+v, err = %v; want outcome %s", result, err, test.want)
				}
				wantMethods := http.MethodGet
				if failureMethod == http.MethodPatch {
					wantMethods += "," + http.MethodPatch
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
		{name: "write transport error", failureMethod: http.MethodPatch, transport: true},
		{name: "malformed read JSON", failureMethod: http.MethodGet},
		{name: "malformed write JSON", failureMethod: http.MethodPatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			adapter := newTestAdapterWithTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				assertHeaders(t, request)
				if request.URL.Path != "/repos/acme/widget/issues/17" {
					t.Fatalf("path = %q", request.URL.String())
				}
				methods = append(methods, request.Method)
				if test.failureMethod == http.MethodPatch && request.Method == http.MethodGet {
					return testHTTPResponse(http.StatusOK, `{"state":"open","updated_at":"old-lease"}`), nil
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
			if test.failureMethod == http.MethodPatch {
				wantMethods += "," + http.MethodPatch
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
		{name: "read has unknown state", failureMethod: http.MethodGet, body: `{"state":"locked","updated_at":"old-lease"}`, wantReason: "issue read returned malformed state or updated_at"},
		{name: "read has no lease", failureMethod: http.MethodGet, body: `{"state":"open"}`, wantReason: "issue read returned malformed state or updated_at"},
		{name: "write has no lease", failureMethod: http.MethodPatch, body: `{"state":"closed","state_reason":"completed"}`, wantReason: "state response has no updated_at lease"},
		{name: "write has no state", failureMethod: http.MethodPatch, body: `{"updated_at":"new-lease"}`, wantReason: "state response does not match requested state"},
		{name: "write remains open for terminal candidate", failureMethod: http.MethodPatch, body: `{"state":"open","updated_at":"new-lease"}`, wantReason: "state response does not match requested state"},
		{name: "write returns competing terminal reason", failureMethod: http.MethodPatch, body: `{"state":"closed","state_reason":"not_planned","updated_at":"new-lease"}`, wantReason: "state response does not match requested state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.URL.Path != "/repos/acme/widget/issues/17" {
					t.Fatalf("path = %q", r.URL.String())
				}
				methods = append(methods, r.Method)
				if test.failureMethod == http.MethodPatch && r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"state":"open","updated_at":"old-lease"}`))
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
			if test.failureMethod == http.MethodPatch {
				wantMethods += "," + http.MethodPatch
			}
			if got := strings.Join(methods, ","); got != wantMethods {
				t.Fatalf("request methods = %q, want %q", got, wantMethods)
			}
		})
	}
}

func TestFlushRejectsMismatchedStatePatchResponsesWithoutRecordingPush(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing state", body: `{"updated_at":"new-lease"}`},
		{name: "still open", body: `{"state":"open","updated_at":"new-lease"}`},
		{name: "competing terminal reason", body: `{"state":"closed","state_reason":"not_planned","updated_at":"new-lease"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "wip.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()

			var repo, outbox, subject string
			events, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
				repo, outbox, subject = tx.NewID(), tx.NewID(), tx.NewID()
				return []store.Draft{{Type: store.TypeRepoAttached, Subject: repo, Env: &store.Env{Repo: repo}, Payload: store.RepoAttached{}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			const ref = "https://github.com/acme/widget/issues/17"
			_, err = db.Exec(`INSERT INTO outbox_entries
				(id,repo,kind,state,subject,ref,idempotency_key,payload,birth_event,last_event)
				VALUES (?,?,'state','approved',?,?,?,json_object('disposition','completed'),?,?)`,
				outbox, repo, subject, ref, "state:"+outbox, events[0].ID, events[0].ID)
			if err != nil {
				t.Fatal(err)
			}

			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_, _ = w.Write([]byte(`{"state":"open","updated_at":"observed-lease"}`))
				case http.MethodPatch:
					_, _ = w.Write([]byte(test.body))
				default:
					t.Fatalf("method = %s", r.Method)
				}
			}))
			report, err := tracker.Flush(ctx, s, store.ActorHuman, repo, adapter)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Entries) != 1 || report.Entries[0].State != "withheld" {
				t.Fatalf("report = %+v, want one withheld entry", report)
			}
			entry, err := s.OutboxEntry(ctx, repo, outbox)
			if err != nil || entry.State != "withheld" || entry.Attempts != 1 {
				t.Fatalf("entry = %+v, err = %v", entry, err)
			}
			if _, found, err := s.FindTrackerPushRecord(ctx, ref); err != nil || found {
				t.Fatalf("mismatched PATCH response changed push record: found=%v err=%v", found, err)
			}
			events, err = s.EventsOfSubject(ctx, outbox)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == store.TypeTrackerStatePushed {
					t.Fatalf("mismatched PATCH response recorded %s", event.Type)
				}
			}
		})
	}
}

func TestIssueStateReadShape(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/widget/issues/17" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"body":"Detail","html_url":"https://github.com/acme/widget/issues/17","state":"closed","state_reason":"not_planned","updated_at":"2026-08-20T12:34:56Z"}`))
	})

	got, err := newTestAdapter(t, handler).readIssue(context.Background(), "acme", "widget", 17)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "closed" || got.StateReason != "not_planned" || got.UpdatedAt != "2026-08-20T12:34:56Z" {
		t.Fatalf("issue = %+v", got)
	}
}

func TestReadStateMapsGitHubLifecycle(t *testing.T) {
	const lease = "2026-08-20T12:34:56Z"
	for _, test := range []struct {
		name        string
		state       string
		stateReason string
		wantClass   tracker.LiveClass
		wantDisplay string
	}{
		{name: "open", state: "open", wantClass: tracker.LiveNonterminal, wantDisplay: "open"},
		{name: "reopened", state: "open", stateReason: "reopened", wantClass: tracker.LiveNonterminal, wantDisplay: "open (reopened)"},
		{name: "completed", state: "closed", stateReason: "completed", wantClass: tracker.LiveCompleted, wantDisplay: "closed (completed)"},
		{name: "not planned", state: "closed", stateReason: "not_planned", wantClass: tracker.LiveCanceled, wantDisplay: "closed (not_planned)"},
		{name: "other closed reason", state: "closed", stateReason: "duplicate", wantClass: tracker.LiveTerminal, wantDisplay: "closed (duplicate)"},
		{name: "closed without reason", state: "closed", wantClass: tracker.LiveTerminal, wantDisplay: "closed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/widget/issues/17" {
					t.Fatalf("request = %s %s", r.Method, r.URL.String())
				}
				_ = json.NewEncoder(w).Encode(issue{
					State: test.state, StateReason: test.stateReason, UpdatedAt: lease,
				})
			}))

			got, err := adapter.ReadState(context.Background(), "https://github.com/acme/widget/issues/17")
			want := tracker.LiveState{Class: test.wantClass, Display: test.wantDisplay, Lease: lease}
			if err != nil || got != want {
				t.Fatalf("ReadState() = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestReadStateRejectsMalformedSuccessfulResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing state", body: `{"updated_at":"2026-08-20T12:34:56Z"}`},
		{name: "unknown state", body: `{"state":"locked","updated_at":"2026-08-20T12:34:56Z"}`},
		{name: "missing updated at", body: `{"state":"open"}`},
		{name: "blank updated at", body: `{"state":"open","updated_at":"  "}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			got, err := adapter.ReadState(context.Background(), "https://github.com/acme/widget/issues/17")
			if got != (tracker.LiveState{}) || err == nil || !strings.Contains(err.Error(), "malformed state or updated_at") {
				t.Fatalf("ReadState() = %+v, %v", got, err)
			}
		})
	}
}

func TestReadStateRejectsInvalidReferencesBeforeRequest(t *testing.T) {
	for _, ref := range []string{
		"not-an-issue",
		"https://github.com/acme/widget/pull/17",
		"https://github.com/acme/widget/issues/0",
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
	transportFailure := errors.New("connection reset")
	t.Run("transport", func(t *testing.T) {
		adapter := newTestAdapterWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportFailure
		}))
		got, err := adapter.ReadState(context.Background(), "https://github.com/acme/widget/issues/17")
		if got != (tracker.LiveState{}) || !errors.Is(err, transportFailure) {
			t.Fatalf("ReadState() = %+v, %v; want transport error", got, err)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{`))
		}))
		got, err := adapter.ReadState(context.Background(), "https://github.com/acme/widget/issues/17")
		if got != (tracker.LiveState{}) || err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Fatalf("ReadState() = %+v, %v", got, err)
		}
	})

	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "provider response", status)
			}))
			got, err := adapter.ReadState(context.Background(), "https://github.com/acme/widget/issues/17")
			if got != (tracker.LiveState{}) || err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
				t.Fatalf("ReadState() = %+v, %v", got, err)
			}
		})
	}
}

func TestIssueStateWriteShapes(t *testing.T) {
	for _, test := range []struct {
		name        string
		disposition store.TrackerDisposition
		wantBody    string
	}{
		{name: "active", disposition: store.TrackerActive, wantBody: `{"state":"open"}`},
		{name: "completed", disposition: store.TrackerCompleted, wantBody: `{"state":"closed","state_reason":"completed"}`},
		{name: "canceled", disposition: store.TrackerCanceled, wantBody: `{"state":"closed","state_reason":"not_planned"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertHeaders(t, r)
				if r.Method != http.MethodPatch || r.URL.Path != "/repos/acme/widget/issues/17" {
					t.Fatalf("request = %s %s", r.Method, r.URL.String())
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if string(body) != test.wantBody {
					t.Fatalf("body = %s, want %s", body, test.wantBody)
				}
				_, _ = w.Write([]byte(`{"state":"closed","updated_at":"2026-08-20T12:35:00Z"}`))
			})

			update, err := stateUpdate(test.disposition)
			if err != nil {
				t.Fatal(err)
			}
			got, err := newTestAdapter(t, handler).writeIssueState(context.Background(), "acme", "widget", 17, update)
			if err != nil || got.UpdatedAt != "2026-08-20T12:35:00Z" {
				t.Fatalf("issue = %+v, err = %v", got, err)
			}
		})
	}

	if _, err := stateUpdate(store.TrackerDisposition("unknown")); err == nil {
		t.Fatal("unsupported disposition succeeded")
	}
}

func TestHTTPFailuresAreClassified(t *testing.T) {
	for _, test := range []struct {
		status int
		want   tracker.Outcome
	}{{http.StatusServiceUnavailable, tracker.RetryableFailure}, {http.StatusUnprocessableEntity, tracker.PermanentRefusal}} {
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
		})
	}
}

func TestForbiddenFailuresAreClassified(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers http.Header
		body    string
		want    tracker.Outcome
	}{
		{name: "retry after", headers: http.Header{"Retry-After": {"60"}}, want: tracker.RetryableFailure},
		{name: "rate limit exhausted", headers: http.Header{"X-RateLimit-Remaining": {"0"}}, want: tracker.RetryableFailure},
		{
			name: "secondary rate limit message",
			body: `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`,
			want: tracker.RetryableFailure,
		},
		{
			name: "permission denied",
			body: `{"message":"Resource not accessible by integration"}`,
			want: tracker.PermanentRefusal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			if body == "" {
				body = `{"message":"Forbidden"}`
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, values := range test.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(body))
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

func TestRepositoryRemoteParsing(t *testing.T) {
	for _, remote := range []string{
		"github.com/acme/widget",
		"https://github.com/acme/widget.git",
		"git@github.com:acme/widget.git",
	} {
		owner, name, err := repository(remote)
		if err != nil || owner != "acme" || name != "widget" {
			t.Fatalf("repository(%q) = %q, %q, %v", remote, owner, name, err)
		}
	}
	if _, _, err := repository("gitlab.com/acme/widget"); err == nil {
		t.Fatal("non-GitHub remote succeeded")
	}
}

func TestFactoryIgnoresProviderNeutralTarget(t *testing.T) {
	seam, err := Factory(Options{Token: "test-token"})(tracker.FactoryInput{
		Repo:   store.Repo{RemoteURL: "github.com/acme/widget"},
		Target: "provider-owned-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := seam.(*Adapter)
	if !ok || adapter.owner != "acme" || adapter.repo != "widget" {
		t.Fatalf("factory seam = %#v", seam)
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
	adapter, err := New(store.Repo{RemoteURL: "github.com/acme/widget"}, Options{
		BaseURL: "https://api.github.test",
		Token:   "test-token",
		Client:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func stateTestEntry() store.OutboxEntry {
	return store.OutboxEntry{
		Kind: "state", Ref: "https://github.com/acme/widget/issues/17", Lease: "old-lease",
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
	if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-GitHub-Api-Version") != apiVersion {
		t.Fatalf("headers = %#v", r.Header)
	}
}
