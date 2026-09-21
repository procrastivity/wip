package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const (
	freeBodyRef          = "https://github.com/acme/widget/issues/17"
	freeBodyCommentsPath = "/repos/acme/widget/issues/17/comments"
	freeBodyKey          = "tracker:01EVENT:comment:https://github.com/acme/widget/issues/17"
)

// postedCommentAdapter answers the comment listing with an empty page and
// captures the one body the adapter posts.
func postedCommentAdapter(t *testing.T, posted *string, posts *int) *Adapter {
	t.Helper()
	return newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.Path != freeBodyCommentsPath {
			t.Fatalf("path = %q", r.URL.String())
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case http.MethodPost:
			*posts++
			var request struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			*posted = request.Body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
}

func freeBodyEntry(payload string) store.OutboxEntry {
	return store.OutboxEntry{
		Kind: "comment", Ref: freeBodyRef, IdempotencyKey: freeBodyKey,
		Payload: json.RawMessage(payload),
	}
}

func TestCommentDeliversTheOperatorBodyVerbatim(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "free body only",
			payload: `{"body":"Rolled the release back"}`,
			want:    "Rolled the release back",
		},
		{
			name:    "free body wins over lifecycle fields",
			payload: `{"stage":"01ABC","title":"Provider API","action":"closed","body":"Operator note"}`,
			want:    "Operator note",
		},
		{
			name:    "surrounding whitespace is trimmed",
			payload: "{\"body\":\"  Rolled the release back\\n\"}",
			want:    "Rolled the release back",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var posted string
			posts := 0
			entry := freeBodyEntry(test.payload)

			result, err := postedCommentAdapter(t, &posted, &posts).Deliver(context.Background(), entry)
			if err != nil || result.Outcome != tracker.Delivered {
				t.Fatalf("result = %+v, err = %v; want outcome %s", result, err, tracker.Delivered)
			}
			if posts != 1 {
				t.Fatalf("comment POSTs = %d, want 1", posts)
			}
			want := test.want + "\n\n" + idempotencyMarker(entry.IdempotencyKey)
			if posted != want {
				t.Fatalf("comment body = %q, want %q", posted, want)
			}
			if strings.Contains(posted, "Stage ") {
				t.Fatalf("free body rendered lifecycle narration: %q", posted)
			}
		})
	}
}

func TestCommentFallsBackToLifecycleRenderingWhenTheBodyIsEmpty(t *testing.T) {
	for _, test := range []struct{ name, payload string }{
		{name: "no body field", payload: `{"stage":"01ABC","title":"Provider API","action":"closed"}`},
		{name: "empty body field", payload: `{"stage":"01ABC","title":"Provider API","action":"closed","body":""}`},
		{name: "blank body field", payload: `{"stage":"01ABC","title":"Provider API","action":"closed","body":"   "}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var posted string
			posts := 0
			entry := freeBodyEntry(test.payload)

			result, err := postedCommentAdapter(t, &posted, &posts).Deliver(context.Background(), entry)
			if err != nil || result.Outcome != tracker.Delivered {
				t.Fatalf("result = %+v, err = %v; want outcome %s", result, err, tracker.Delivered)
			}
			if posts != 1 {
				t.Fatalf("comment POSTs = %d, want 1", posts)
			}
			want := `Stage "Provider API" closed.` + "\n\n" + idempotencyMarker(entry.IdempotencyKey)
			if posted != want {
				t.Fatalf("comment body = %q, want %q", posted, want)
			}
		})
	}
}

func TestCommentRefusesAPayloadWithNeitherBodyNorTitle(t *testing.T) {
	for _, test := range []struct{ name, payload string }{
		{name: "empty object", payload: `{}`},
		{name: "blank body", payload: `{"body":"   "}`},
		{name: "lifecycle fields without a title", payload: `{"stage":"01ABC","action":"closed"}`},
		{name: "blank title and blank body", payload: `{"title":"  ","action":"closed","body":""}`},
		{name: "malformed JSON", payload: `{"body":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			adapter := newTestAdapter(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

			result, err := adapter.Deliver(context.Background(), freeBodyEntry(test.payload))
			if err != nil || result.Outcome != tracker.PermanentRefusal || called {
				t.Fatalf("result = %+v, err = %v, request made = %t", result, err, called)
			}
			if !strings.Contains(result.Reason, "malformed comment payload") {
				t.Fatalf("reason = %q", result.Reason)
			}
		})
	}
}

func TestFreeBodyCommentConvergesOnRetry(t *testing.T) {
	var comments []comment
	posts := 0
	adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.Path != freeBodyCommentsPath {
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
			comments = append(comments, comment{Body: request.Body})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))

	entry := freeBodyEntry(`{"body":"Rolled the release back"}`)
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
	if len(comments) != 1 ||
		!strings.HasPrefix(comments[0].Body, "Rolled the release back\n\n") ||
		!strings.HasSuffix(comments[0].Body, idempotencyMarker(entry.IdempotencyKey)) {
		t.Fatalf("stored comments = %+v", comments)
	}

	// The marker keys off the idempotency key, not the text: the same body
	// under a new key is a new comment.
	second := entry
	second.IdempotencyKey = "tracker:01OTHER:comment:" + freeBodyRef
	result, err := adapter.Deliver(context.Background(), second)
	if err != nil || result.Outcome != tracker.Delivered {
		t.Fatalf("result = %+v, err = %v; want outcome %s", result, err, tracker.Delivered)
	}
	if posts != 2 || len(comments) != 2 {
		t.Fatalf("comment POSTs = %d, stored = %d; want 2 and 2", posts, len(comments))
	}
}
