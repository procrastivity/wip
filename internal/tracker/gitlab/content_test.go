package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

// notesHandler serves the notes collection for issue 7 out of a slice the test
// keeps, the same shape TestCommentDeduplicatesHalfFailedReplay uses.
func notesHandler(t *testing.T, notes *[]note, posts *int) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.EscapedPath() != projectPath+"/issues/7/notes" {
			t.Fatalf("path = %q", r.URL.String())
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(*notes)
		case http.MethodPost:
			*posts++
			var request struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			*notes = append(*notes, note{Body: request.Body})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	})
}

func TestCommentDeliversAFreeBody(t *testing.T) {
	var notes []note
	posts := 0
	adapter := newTestAdapter(t, notesHandler(t, &notes, &posts))

	entry := store.OutboxEntry{
		Kind: "comment", Ref: testRef, IdempotencyKey: "tracker:01ADHOC:comment:" + testRef,
		Payload: json.RawMessage(`{"body":"Blocked on the staging cluster.\n\nSecond paragraph."}`),
	}
	result, err := adapter.Deliver(context.Background(), entry)
	if err != nil || result.Outcome != tracker.Delivered {
		t.Fatalf("Deliver() = %+v, %v; want Delivered", result, err)
	}
	if posts != 1 || len(notes) != 1 {
		t.Fatalf("posts = %d, notes = %d; want 1, 1", posts, len(notes))
	}
	want := "Blocked on the staging cluster.\n\nSecond paragraph.\n\n" + idempotencyMarker(entry.IdempotencyKey)
	if notes[0].Body != want {
		t.Fatalf("note body = %q, want %q", notes[0].Body, want)
	}
	if strings.Contains(notes[0].Body, "Stage ") {
		t.Fatalf("free body borrowed the lifecycle rendering: %q", notes[0].Body)
	}
}

func TestCommentFallsBackToLifecycleRenderingWhenBodyIsEmpty(t *testing.T) {
	for _, payload := range []string{
		`{"stage":"01ABC","title":"Provider API","action":"closed"}`,
		`{"stage":"01ABC","title":"Provider API","action":"closed","body":""}`,
		`{"stage":"01ABC","title":"Provider API","action":"closed","body":"   "}`,
	} {
		t.Run(payload, func(t *testing.T) {
			var notes []note
			posts := 0
			adapter := newTestAdapter(t, notesHandler(t, &notes, &posts))
			entry := store.OutboxEntry{
				Kind: "comment", Ref: testRef, IdempotencyKey: "tracker:event:comment:ref:stage",
				Payload: json.RawMessage(payload),
			}
			result, err := adapter.Deliver(context.Background(), entry)
			if err != nil || result.Outcome != tracker.Delivered || posts != 1 {
				t.Fatalf("Deliver() = %+v, %v; posts = %d", result, err, posts)
			}
			want := "Stage \"Provider API\" closed.\n\n" + idempotencyMarker(entry.IdempotencyKey)
			if notes[0].Body != want {
				t.Fatalf("note body = %q, want %q", notes[0].Body, want)
			}
		})
	}
}

func TestCommentRefusesAPayloadWithNeitherBodyNorTitle(t *testing.T) {
	for _, payload := range []string{
		`{"stage":"01ABC","action":"closed"}`,
		`{"body":""}`,
		`{"body":"  ","title":"  "}`,
		`{`,
	} {
		t.Run(payload, func(t *testing.T) {
			called := false
			adapter := newTestAdapter(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			result, err := adapter.Deliver(context.Background(), store.OutboxEntry{
				Kind: "comment", Ref: testRef, IdempotencyKey: "k",
				Payload: json.RawMessage(payload),
			})
			if err != nil || result.Outcome != tracker.PermanentRefusal ||
				result.Reason != "gitlab tracker: malformed comment payload" || called {
				t.Fatalf("Deliver() = %+v, %v; request called = %t", result, err, called)
			}
		})
	}
}

func TestFreeBodyCommentConvergesOnRetry(t *testing.T) {
	const key = "tracker:01ADHOC:comment:" + testRef
	notes := []note{{Body: "unrelated operator note"}, {Body: idempotencyMarker(key), System: true}}
	posts := 0
	adapter := newTestAdapter(t, notesHandler(t, &notes, &posts))
	entry := store.OutboxEntry{
		Kind: "comment", Ref: testRef, IdempotencyKey: key,
		Payload: json.RawMessage(`{"body":"Blocked on the staging cluster."}`),
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := adapter.Deliver(context.Background(), entry)
		want := tracker.Delivered
		if attempt > 0 {
			want = tracker.Converged
		}
		if err != nil || result.Outcome != want {
			t.Fatalf("attempt %d = %+v, %v; want %s", attempt+1, result, err, want)
		}
	}
	if posts != 1 {
		t.Fatalf("comment POSTs = %d, want 1", posts)
	}
}

func TestReadContentReturnsTheProviderObservation(t *testing.T) {
	const lease = "2026-08-20T12:34:56.123Z"
	const webURL = "https://gitlab.test/group/sub/project/-/issues/7"
	adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.Method != http.MethodGet || r.URL.EscapedPath() != projectPath+"/issues/7" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(issue{
			IID: 7, Title: "Keep the cursor visible", Description: "Show in-progress work.",
			WebURL: webURL, State: "opened", UpdatedAt: lease,
		})
	}))

	got, err := adapter.ReadContent(context.Background(), testRef)
	if err != nil {
		t.Fatalf("ReadContent() error = %v", err)
	}
	want := tracker.Content{
		Ref:       testRef,
		Title:     "Keep the cursor visible",
		Body:      "Show in-progress work.",
		URL:       webURL,
		State:     tracker.LiveState{Class: tracker.LiveNonterminal, Display: "opened", Lease: lease},
		UpdatedAt: time.Date(2026, 8, 20, 12, 34, 56, 123000000, time.UTC),
	}
	if got != want {
		t.Fatalf("ReadContent() = %+v, want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
}

func TestReadContentStripsTheIdempotencyMarker(t *testing.T) {
	marker := idempotencyMarker("backlog:01ABC")
	for _, test := range []struct {
		name        string
		description string
		want        string
	}{
		{name: "marker only", description: marker, want: ""},
		{name: "body then marker", description: "Show in-progress work.\n\n" + marker, want: "Show in-progress work."},
		{
			name:        "body, provenance, marker",
			description: "Show in-progress work.\n\nSource: adhoc\n\n" + marker + "\n",
			want:        "Show in-progress work.\n\nSource: adhoc",
		},
		{name: "no marker", description: "Show in-progress work.\n", want: "Show in-progress work.\n"},
		{
			name:        "marker-shaped text that is not a trailing marker",
			description: "<!-- wip-idempotency:not-a-digest --> still here",
			want:        "<!-- wip-idempotency:not-a-digest --> still here",
		},
		{
			name:        "marker followed by operator text is left alone",
			description: marker + "\n\nAdded later.",
			want:        marker + "\n\nAdded later.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(issue{
					Title: "t", Description: test.description, WebURL: testRef,
					State: "closed", UpdatedAt: "2026-08-20T12:34:56Z",
				})
			}))
			got, err := adapter.ReadContent(context.Background(), testRef)
			if err != nil || got.Body != test.want {
				t.Fatalf("ReadContent().Body = %q, %v; want %q", got.Body, err, test.want)
			}
		})
	}
}

func TestReadContentAndReadStateAgreeOnTheClass(t *testing.T) {
	const lease = "2026-08-20T12:34:56Z"
	for _, test := range []struct {
		name   string
		label  string
		state  string
		labels []string
	}{
		{name: "opened", state: "opened"},
		{name: "closed", state: "closed"},
		{name: "closed with label configured", label: "wf::canceled", state: "closed", labels: []string{"wf::canceled"}},
		{name: "closed without the configured label", label: "wf::canceled", state: "closed"},
		{name: "closed with an unconfigured label", state: "closed", labels: []string{"wf::canceled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(issue{
					Title: "t", Description: "d", WebURL: testRef,
					State: test.state, Labels: test.labels, UpdatedAt: lease,
				})
			})
			adapter := newTestAdapterWithLabel(t, handler, test.label)
			state, stateErr := adapter.ReadState(context.Background(), testRef)
			content, contentErr := adapter.ReadContent(context.Background(), testRef)
			if stateErr != nil || contentErr != nil {
				t.Fatalf("ReadState err = %v, ReadContent err = %v", stateErr, contentErr)
			}
			if content.State != state {
				t.Fatalf("ReadContent().State = %+v, ReadState() = %+v", content.State, state)
			}
		})
	}
}

func TestReadContentRejectsInvalidReferencesAndMalformedReads(t *testing.T) {
	t.Run("invalid reference", func(t *testing.T) {
		called := false
		adapter := newTestAdapter(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		got, err := adapter.ReadContent(context.Background(), "https://other.test/group/sub/project/-/issues/7")
		if got != (tracker.Content{}) || err == nil || called {
			t.Fatalf("ReadContent() = %+v, %v; called = %t", got, err, called)
		}
	})
	t.Run("malformed state", func(t *testing.T) {
		adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(issue{Title: "t", State: "locked", UpdatedAt: ""})
		}))
		got, err := adapter.ReadContent(context.Background(), testRef)
		if got != (tracker.Content{}) || err == nil ||
			!strings.Contains(err.Error(), "malformed state or updated_at") {
			t.Fatalf("ReadContent() = %+v, %v", got, err)
		}
	})
	t.Run("api error", func(t *testing.T) {
		adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Project Not Found", http.StatusNotFound)
		}))
		got, err := adapter.ReadContent(context.Background(), testRef)
		if got != (tracker.Content{}) || err == nil ||
			!strings.Contains(err.Error(), "check the token has api scope") {
			t.Fatalf("ReadContent() = %+v, %v", got, err)
		}
	})
}

func TestReadContentFallsBackToTheRequestedReferenceForURL(t *testing.T) {
	adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(issue{
			Title: "t", Description: "d", State: "opened", UpdatedAt: "2026-08-20T12:34:56Z",
		})
	}))
	got, err := adapter.ReadContent(context.Background(), testRef)
	if err != nil || got.URL != testRef {
		t.Fatalf("ReadContent().URL = %q, %v; want %q", got.URL, err, testRef)
	}
}
