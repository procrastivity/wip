package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if err != nil || result.Outcome != tracker.Delivered || result.Ref != "https://github.com/acme/widget/issues/17" {
			t.Fatalf("attempt %d result = %+v, err = %v", attempt+1, result, err)
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
		if err != nil || result.Outcome != tracker.Delivered {
			t.Fatalf("attempt %d result = %+v, err = %v", attempt+1, result, err)
		}
	}
	if posts != 1 {
		t.Fatalf("comment POSTs = %d, want 1", posts)
	}
}

func TestStateDeliveryRefusesWithoutCallingGitHub(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("state delivery called GitHub")
	})
	adapter := newTestAdapter(t, handler)

	result, err := adapter.Deliver(context.Background(), store.OutboxEntry{Kind: "state"})
	if err != nil || result.Outcome != tracker.PermanentRefusal || !strings.Contains(result.Reason, "atomic lease guard") {
		t.Fatalf("result = %+v, err = %v", result, err)
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

func newTestAdapter(t *testing.T, handler http.Handler) *Adapter {
	t.Helper()
	adapter, err := New(store.Repo{RemoteURL: "github.com/acme/widget"}, Options{
		BaseURL: "https://api.github.test",
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
