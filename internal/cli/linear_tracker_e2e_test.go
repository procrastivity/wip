package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
	lineartracker "github.com/procrastivity/wip/internal/tracker/linear"
)

const (
	linearCLITeam         = "11111111-1111-4111-8111-111111111111"
	linearCLIStartedState = "22222222-2222-4222-8222-222222222222"
)

type linearCLIRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn linearCLIRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestLinearCreateConvergesThroughCLIAndPersistsBindingAndConfig(t *testing.T) {
	dir := newGitRepo(t, "linear-tracker")
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	t.Setenv("WIP_DB_PATH", dbPath)

	var operations []string
	lookups := 0
	mutations := 0
	client := &http.Client{Transport: linearCLIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://api.linear.test/graphql" {
			t.Fatalf("Linear request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "test-token" {
			t.Fatalf("Linear Authorization = %q", request.Header.Get("Authorization"))
		}
		var body struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, body.OperationName)

		status := http.StatusOK
		response := map[string]any{}
		switch body.OperationName {
		case "FindIssueByClientID":
			lookups++
			nodes := []any{}
			if lookups == 2 {
				nodes = append(nodes, map[string]any{
					"id": body.Variables["id"], "identifier": "BDS-240",
					"team": map[string]any{"id": linearCLITeam},
				})
			}
			response["data"] = map[string]any{"issues": map[string]any{"nodes": nodes}}
		case "WorkflowStates":
			if body.Variables["teamId"] != linearCLITeam {
				t.Fatalf("WorkflowStates variables = %#v", body.Variables)
			}
			response["data"] = map[string]any{"team": map[string]any{"states": map[string]any{"nodes": []any{
				map[string]any{"id": linearCLIStartedState, "name": "In Progress", "type": "started", "position": 1},
			}}}}
		case "CreateIssue":
			mutations++
			input, ok := body.Variables["input"].(map[string]any)
			if !ok || input["teamId"] != linearCLITeam || input["stateId"] != linearCLIStartedState || input["title"] != "Reconcile CLI create" {
				t.Fatalf("CreateIssue variables = %#v", body.Variables)
			}
			// Model an ambiguous server response after Linear accepted the write.
			// The adapter must find the issue on its post-failure read.
			status = http.StatusServiceUnavailable
		default:
			t.Fatalf("unexpected Linear operation %q", body.OperationName)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(encoded))),
			Request:    request,
		}, nil
	})}

	providers := tracker.NewRegistry()
	providers.Register("linear", lineartracker.Factory(lineartracker.Options{
		BaseURL: "https://api.linear.test/graphql",
		Token:   "test-token",
		Client:  client,
	}))
	for _, args := range [][]string{
		{"init", "--json"},
		{"outbox", "backend", "linear"},
		{"outbox", "target", linearCLITeam},
	} {
		if result := runWithProviders(t, providers, args...); result.exitCode != 0 {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, result.exitCode, result.stdout, result.stderr)
		}
	}

	added := mustJSON[struct {
		ID string `json:"id"`
	}](t, runWithProviders(t, providers, "backlog", "add", "--title", "Reconcile CLI create", "--provenance", "intake", "--json").stdout)
	delegated := mustJSON[struct {
		Outbox string `json:"outbox"`
	}](t, runWithProviders(t, providers, "backlog", "delegate", added.ID, "--json").stdout)
	if delegated.Outbox == "" {
		t.Fatal("delegation returned no outbox identity")
	}
	if result := runWithProviders(t, providers, "outbox", "approve", delegated.Outbox); result.exitCode != 0 {
		t.Fatalf("approve: exit=%d stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	flushed := mustJSON[tracker.Report](t, runWithProviders(t, providers, "outbox", "flush", "--json").stdout)
	if len(flushed.Entries) != 1 || flushed.Entries[0].ID != delegated.Outbox || flushed.Entries[0].State != "converged" {
		t.Fatalf("flush report = %+v", flushed)
	}

	wantOperations := []string{"FindIssueByClientID", "WorkflowStates", "CreateIssue", "FindIssueByClientID"}
	if !reflect.DeepEqual(operations, wantOperations) || lookups != 2 || mutations != 1 {
		t.Fatalf("Linear calls = %v, lookups=%d mutations=%d", operations, lookups, mutations)
	}
	if result := runWithProviders(t, providers, "outbox", "backend"); result.exitCode != 0 || result.stdout != "linear\n" {
		t.Fatalf("durable backend = exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
	}
	if result := runWithProviders(t, providers, "outbox", "target"); result.exitCode != 0 || result.stdout != linearCLITeam+"\n" {
		t.Fatalf("durable target = exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
	}

	s := openTestStore(t, dbPath)
	repos, err := s.Repos(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v (err %v)", repos, err)
	}
	backend, _, err := s.Config(context.Background(), repos[0].ID, store.TrackerBackendKey)
	if err != nil || backend != "linear" {
		t.Fatalf("stored backend = %q (err %v)", backend, err)
	}
	target, _, err := s.Config(context.Background(), repos[0].ID, store.TrackerTargetKey)
	if err != nil || target != linearCLITeam {
		t.Fatalf("stored target = %q (err %v)", target, err)
	}
	entry, err := s.OutboxEntry(context.Background(), repos[0].ID, delegated.Outbox)
	if err != nil || entry.State != "flushed" || entry.Attempts != 1 || entry.Reason != "" {
		t.Fatalf("durable create entry = %+v (err %v)", entry, err)
	}
	events, err := s.EventsOfSubject(context.Background(), delegated.Outbox)
	if err != nil || len(events) != 2 || events[1].Type != store.TypeTrackerItemCreated {
		t.Fatalf("binding events = %+v (err %v)", events, err)
	}
	var binding store.TrackerItemCreated
	if err := json.Unmarshal(events[1].Payload, &binding); err != nil || binding.Ref != "BDS-240" {
		t.Fatalf("durable binding = %+v (err %v)", binding, err)
	}
	active, err := s.ActiveBacklog(context.Background(), repos[0].ID)
	if err != nil || len(active) != 0 {
		t.Fatalf("active backlog after binding = %+v (err %v)", active, err)
	}
}
