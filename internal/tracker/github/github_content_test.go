package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const (
	contentRef  = "https://github.com/acme/widget/issues/17"
	contentPath = "/repos/acme/widget/issues/17"
)

// contentAdapter answers one issue GET with body. Pass gets to count reads.
func contentAdapter(t *testing.T, body string, gets *int) *Adapter {
	t.Helper()
	return newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.Method != http.MethodGet || r.URL.Path != contentPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if gets != nil {
			*gets++
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestReadContentReturnsTheProviderObservation(t *testing.T) {
	gets := 0
	adapter := contentAdapter(t, `{"title":"Keep the cursor visible","body":"Show in-progress work.",`+
		`"html_url":"https://github.com/acme/widget/issues/17","state":"open","updated_at":"2026-08-20T12:34:56Z"}`, &gets)

	got, err := adapter.ReadContent(context.Background(), contentRef)
	if err != nil {
		t.Fatal(err)
	}
	want := tracker.Content{
		Ref:       contentRef,
		Title:     "Keep the cursor visible",
		Body:      "Show in-progress work.",
		URL:       "https://github.com/acme/widget/issues/17",
		State:     tracker.LiveState{Class: tracker.LiveNonterminal, Display: "open", Lease: "2026-08-20T12:34:56Z"},
		UpdatedAt: time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC),
	}
	if got.Ref != want.Ref || got.Title != want.Title || got.Body != want.Body || got.URL != want.URL || got.State != want.State {
		t.Fatalf("ReadContent() = %+v, want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("UpdatedAt = %s, want %s", got.UpdatedAt, want.UpdatedAt)
	}
	if gets != 1 {
		t.Fatalf("issue GETs = %d, want 1", gets)
	}
}

func TestReadContentStripsTheTrailingIdempotencyMarker(t *testing.T) {
	marker := idempotencyMarker("backlog:01ABC")
	for _, test := range []struct{ name, body, want string }{
		{
			// Exactly how create composes a body (github.go: detail, Source, marker).
			name: "body wip created", body: "Show in-progress work.\n\nSource: agent\n\n" + marker,
			want: "Show in-progress work.\n\nSource: agent",
		},
		{name: "marker only", body: marker, want: ""},
		{name: "trailing whitespace after the marker", body: "Detail\n\n" + marker + "\n", want: "Detail"},
		{name: "no marker", body: "Detail", want: "Detail"},
		{name: "empty body", body: "", want: ""},
		{
			name: "marker is not trailing", body: marker + "\n\nOperator kept writing",
			want: marker + "\n\nOperator kept writing",
		},
		{
			name: "look-alike without a digest", body: "Detail <!-- wip-idempotency:not-a-digest -->",
			want: "Detail <!-- wip-idempotency:not-a-digest -->",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := json.Marshal(issue{Body: test.body, State: "open", UpdatedAt: "2026-08-20T12:34:56Z"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := contentAdapter(t, string(response), nil).ReadContent(context.Background(), contentRef)
			if err != nil {
				t.Fatal(err)
			}
			if got.Body != test.want {
				t.Fatalf("Body = %q, want %q", got.Body, test.want)
			}
		})
	}
}

func TestReadContentAndReadStateClassifyIdentically(t *testing.T) {
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
			response, err := json.Marshal(issue{State: test.state, StateReason: test.stateReason, UpdatedAt: lease})
			if err != nil {
				t.Fatal(err)
			}
			adapter := contentAdapter(t, string(response), nil)
			live, err := adapter.ReadState(context.Background(), contentRef)
			if err != nil {
				t.Fatal(err)
			}
			content, err := adapter.ReadContent(context.Background(), contentRef)
			if err != nil {
				t.Fatal(err)
			}
			want := tracker.LiveState{Class: test.wantClass, Display: test.wantDisplay, Lease: lease}
			if live != want || content.State != want {
				t.Fatalf("ReadState() = %+v, ReadContent().State = %+v; want %+v", live, content.State, want)
			}
		})
	}
}

func TestReadContentRejectsMalformedSuccessfulResponses(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{name: "missing state", body: `{"updated_at":"2026-08-20T12:34:56Z"}`},
		{name: "unknown state", body: `{"state":"locked","updated_at":"2026-08-20T12:34:56Z"}`},
		{name: "missing updated at", body: `{"state":"open"}`},
		{name: "blank updated at", body: `{"state":"open","updated_at":"  "}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contentAdapter(t, test.body, nil).ReadContent(context.Background(), contentRef)
			if got != (tracker.Content{}) || err == nil || !strings.Contains(err.Error(), "malformed state or updated_at") {
				t.Fatalf("ReadContent() = %+v, %v", got, err)
			}
		})
	}
}

func TestReadContentRejectsInvalidReferencesBeforeRequest(t *testing.T) {
	for _, ref := range []string{
		"not-an-issue",
		"https://github.com/acme/widget/pull/17",
		"https://github.com/acme/widget/issues/0",
	} {
		t.Run(ref, func(t *testing.T) {
			called := false
			adapter := newTestAdapter(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			got, err := adapter.ReadContent(context.Background(), ref)
			if got != (tracker.Content{}) || err == nil || called {
				t.Fatalf("ReadContent(%q) = %+v, %v; request made = %t", ref, got, err, called)
			}
		})
	}
}

func TestReadContentReturnsTransportAndAPIErrors(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		transportFailure := errors.New("connection reset")
		adapter := newTestAdapterWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportFailure
		}))
		got, err := adapter.ReadContent(context.Background(), contentRef)
		if got != (tracker.Content{}) || !errors.Is(err, transportFailure) {
			t.Fatalf("ReadContent() = %+v, %v; want transport error", got, err)
		}
	})

	t.Run("http failure", func(t *testing.T) {
		adapter := newTestAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "provider response", http.StatusServiceUnavailable)
		}))
		got, err := adapter.ReadContent(context.Background(), contentRef)
		if got != (tracker.Content{}) || err == nil || !strings.Contains(err.Error(), "HTTP 503") {
			t.Fatalf("ReadContent() = %+v, %v", got, err)
		}
	})
}

func TestReadContentLeavesUpdatedAtZeroWhenTheProviderTimeIsUnparsable(t *testing.T) {
	got, err := contentAdapter(t, `{"title":"T","body":"B","state":"open","updated_at":"old-lease"}`, nil).
		ReadContent(context.Background(), contentRef)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %s, want the zero time", got.UpdatedAt)
	}
	if got.State.Lease != "old-lease" {
		t.Fatalf("Lease = %q, want the provider string verbatim", got.State.Lease)
	}
}

func TestAdapterAnnouncesTheContentReaderCapability(t *testing.T) {
	seam, err := Factory(Options{Token: "test-token"})(tracker.FactoryInput{
		Repo: store.Repo{RemoteURL: "github.com/acme/widget"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seam.(tracker.ContentReader); !ok {
		t.Fatalf("seam %#v does not implement tracker.ContentReader", seam)
	}
	if _, ok := seam.(tracker.StateReader); !ok {
		t.Fatalf("seam %#v no longer implements tracker.StateReader", seam)
	}
}
