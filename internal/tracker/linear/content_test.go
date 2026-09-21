package linear

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/tracker"
)

func TestReadContentReadsTitleBodyURLStateAndTimestamp(t *testing.T) {
	var operations []string
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		operations = append(operations, request.OperationName)
		if request.OperationName != "ReadIssueContent" {
			t.Fatalf("operation = %q; a content read must not use the write gate's query", request.OperationName)
		}
		for _, field := range []string{"title", "description", "url", "updatedAt", "identifier", "team", "state"} {
			if !strings.Contains(request.Query, field) {
				t.Fatalf("content query does not select %q: %s", field, request.Query)
			}
		}
		if got := request.stringVariable("ref"); got != "BDS-124" {
			t.Fatalf("ref variable = %q", got)
		}
		return contentResponse(map[string]any{
			"identifier":  "BDS-124",
			"title":       "Keep the cursor visible",
			"description": "Show in-progress work.\n\nSource: agent",
			"url":         "https://linear.app/acme/issue/BDS-124/keep-the-cursor-visible",
			"updatedAt":   "2026-09-21T17:04:05Z",
			"team":        map[string]any{"id": testTeam},
			"state":       workflowState{ID: startedState, Name: "Doing", Type: "started"},
		})
	})
	got, err := adapter.ReadContent(context.Background(), "BDS-124")
	if err != nil {
		t.Fatalf("ReadContent err = %v", err)
	}
	want := tracker.Content{
		Ref:       "BDS-124",
		Title:     "Keep the cursor visible",
		Body:      "Show in-progress work.\n\nSource: agent",
		URL:       "https://linear.app/acme/issue/BDS-124/keep-the-cursor-visible",
		State:     tracker.LiveState{Class: tracker.LiveActive, Display: "Doing", Lease: "2026-09-21T17:04:05Z"},
		UpdatedAt: time.Date(2026, 9, 21, 17, 4, 5, 0, time.UTC),
	}
	if got.Ref != want.Ref || got.Title != want.Title || got.Body != want.Body || got.URL != want.URL || got.State != want.State {
		t.Fatalf("ReadContent = %+v, want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %v, want exactly one content read", operations)
	}
}

func TestReadContentReturnsAnEmptyBodyAndZeroTimestampWhenLinearReportsNone(t *testing.T) {
	adapter := newTestAdapter(t, func(_ *testing.T, _ gqlTestRequest) testResponse {
		return contentResponse(map[string]any{
			"identifier":  "BDS-124",
			"title":       "Untouched",
			"description": nil,
			"url":         "https://linear.app/acme/issue/BDS-124/untouched",
			"updatedAt":   "",
			"team":        map[string]any{"id": testTeam},
			"state":       workflowState{ID: startedState, Name: "Todo", Type: "unstarted"},
		})
	})
	got, err := adapter.ReadContent(context.Background(), "BDS-124")
	if err != nil || got.Body != "" || !got.UpdatedAt.IsZero() {
		t.Fatalf("ReadContent = %+v, err = %v; want empty body and zero timestamp", got, err)
	}
	if got.State.Class != tracker.LiveBacklog || got.State.Lease != "" {
		t.Fatalf("state = %+v", got.State)
	}
}

func TestReadContentStateClassMatchesReadState(t *testing.T) {
	for _, stateType := range []string{"triage", "backlog", "unstarted", "started", "completed", "canceled", "duplicate", "future"} {
		t.Run(stateType, func(t *testing.T) {
			state := workflowState{ID: startedState, Name: "Visible name", Type: stateType}
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				switch request.OperationName {
				case "ReadIssue":
					return issueResponse(state, "2026-09-21T17:04:05Z")
				case "ReadIssueContent":
					return contentResponse(map[string]any{
						"identifier": "BDS-124", "title": "Title", "description": "Body",
						"url": "https://linear.app/acme/issue/BDS-124/title", "updatedAt": "2026-09-21T17:04:05Z",
						"team": map[string]any{"id": testTeam}, "state": state,
					})
				default:
					t.Fatalf("operation = %q", request.OperationName)
					return testResponse{}
				}
			})
			live, err := adapter.ReadState(context.Background(), "BDS-124")
			if err != nil {
				t.Fatalf("ReadState err = %v", err)
			}
			content, err := adapter.ReadContent(context.Background(), "BDS-124")
			if err != nil {
				t.Fatalf("ReadContent err = %v", err)
			}
			if content.State != live {
				t.Fatalf("content state = %+v, state read = %+v", content.State, live)
			}
		})
	}
}

func TestReadContentSucceedsForAnIssueTheWriteGateRefuses(t *testing.T) {
	// validateIssue refuses an issue whose workflow state carries no UUID and
	// whose updatedAt is empty, because neither can gate a guarded write. The
	// same issue is still perfectly readable, and the content read must say so.
	state := workflowState{Name: "Doing", Type: "started"}
	adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
		switch request.OperationName {
		case "ReadIssue":
			item := issueMap(state, "")
			item["state"] = state
			return gqlData(map[string]any{"issue": item})
		case "ReadIssueContent":
			return contentResponse(map[string]any{
				"identifier": "BDS-124", "title": "Readable", "description": "Readable body",
				"url": "https://linear.app/acme/issue/BDS-124/readable", "updatedAt": "",
				"team": map[string]any{"id": testTeam}, "state": state,
			})
		default:
			t.Fatalf("operation = %q", request.OperationName)
			return testResponse{}
		}
	})
	if _, err := adapter.ReadState(context.Background(), "BDS-124"); err == nil {
		t.Fatal("ReadState accepted an issue the write gate must refuse")
	}
	got, err := adapter.ReadContent(context.Background(), "BDS-124")
	if err != nil {
		t.Fatalf("ReadContent err = %v; the write gate must not gate a content read", err)
	}
	if got.Title != "Readable" || got.Body != "Readable body" || got.State.Class != tracker.LiveActive {
		t.Fatalf("ReadContent = %+v", got)
	}
}

func TestReadContentRefusesForeignReferencesWithoutWideningTheWriteGate(t *testing.T) {
	t.Run("malformed reference makes no request", func(t *testing.T) {
		called := false
		adapter := newTestAdapter(t, func(*testing.T, gqlTestRequest) testResponse {
			called = true
			return testResponse{}
		})
		if _, err := adapter.ReadContent(context.Background(), "not-an-issue"); err == nil || called {
			t.Fatalf("err = %v, called = %t", err, called)
		}
	})
	for _, test := range []struct {
		name string
		item map[string]any
	}{
		{name: "different team", item: map[string]any{
			"identifier": "BDS-124", "title": "Title", "url": "https://linear.app/x",
			"updatedAt": "2026-09-21T17:04:05Z",
			"team":      map[string]any{"id": "99999999-9999-4999-8999-999999999999"},
			"state":     workflowState{ID: startedState, Name: "Doing", Type: "started"},
		}},
		{name: "identifier mismatch", item: map[string]any{
			"identifier": "BDS-999", "title": "Title", "url": "https://linear.app/x",
			"updatedAt": "2026-09-21T17:04:05Z", "team": map[string]any{"id": testTeam},
			"state": workflowState{ID: startedState, Name: "Doing", Type: "started"},
		}},
		{name: "missing issue", item: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, func(t *testing.T, request gqlTestRequest) testResponse {
				if request.OperationName != "ReadIssueContent" {
					t.Fatalf("operation = %q", request.OperationName)
				}
				if test.item == nil {
					return gqlData(map[string]any{"issue": nil})
				}
				return contentResponse(test.item)
			})
			got, err := adapter.ReadContent(context.Background(), "BDS-124")
			if err == nil || got != (tracker.Content{}) {
				t.Fatalf("ReadContent = %+v, err = %v", got, err)
			}
		})
	}
}

func contentResponse(item map[string]any) testResponse {
	return gqlData(map[string]any{"issue": item})
}
