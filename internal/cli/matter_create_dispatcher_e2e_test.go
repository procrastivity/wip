package cli_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestMatterCreateDispatcherParity fixes the observable pre-dispatch contract
// as literal bytes and store facts. Dynamic ULIDs are extracted only so the
// surrounding bytes and the corresponding event/projection can be compared.
func TestMatterCreateDispatcherParity(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	human := runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Dispatcher parity")
	if human.exitCode != 0 || human.stderr != "" {
		t.Fatalf("human create: exit=%d stdout=%q stderr=%q", human.exitCode, human.stdout, human.stderr)
	}
	const humanPrefix = "created matter dispatcher-parity ("
	if !strings.HasPrefix(human.stdout, humanPrefix) || !strings.HasSuffix(human.stdout, ")\n") {
		t.Fatalf("human stdout = %q, want the legacy created-matter line", human.stdout)
	}
	humanID := strings.TrimSuffix(strings.TrimPrefix(human.stdout, humanPrefix), ")\n")
	if want := fmt.Sprintf("created matter dispatcher-parity (%s)\n", humanID); human.stdout != want {
		t.Fatalf("human stdout = %q, want byte-for-byte %q", human.stdout, want)
	}

	jsonResult := runIn(t, dir, dbEnv, "plumbing", "matter", "create",
		"--title", "Dispatcher JSON parity", "--locator", "dispatcher-json", "--json")
	if jsonResult.exitCode != 0 || jsonResult.stderr != "" {
		t.Fatalf("JSON create: exit=%d stdout=%q stderr=%q", jsonResult.exitCode, jsonResult.stdout, jsonResult.stderr)
	}
	jsonMatter := mustJSON[nodePayload](t, jsonResult.stdout)
	jsonWant := fmt.Sprintf("{\"id\":\"%s\",\"locator\":\"dispatcher-json\",\"title\":\"Dispatcher JSON parity\"}\n", jsonMatter.ID)
	if jsonResult.stdout != jsonWant {
		t.Fatalf("JSON stdout = %q, want byte-for-byte %q", jsonResult.stdout, jsonWant)
	}

	humanFailure := runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "!!!")
	const humanFailureWant = "wip: plumbing matter create: a matter's title must contain at least one letter or digit\n"
	if humanFailure.exitCode != 1 || humanFailure.stdout != "" || humanFailure.stderr != humanFailureWant {
		t.Fatalf("human failure = exit %d, stdout %q, stderr %q; want exit 1 and stderr %q",
			humanFailure.exitCode, humanFailure.stdout, humanFailure.stderr, humanFailureWant)
	}

	jsonFailure := runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "!!!", "--json")
	const jsonFailureWant = "{\"error\":{\"code\":\"validation.invalid-title\",\"message\":\"a matter's title must contain at least one letter or digit\"}}\n"
	if jsonFailure.exitCode != 1 || jsonFailure.stdout != "" || jsonFailure.stderr != jsonFailureWant {
		t.Fatalf("JSON failure = exit %d, stdout %q, stderr %q; want exit 1 and stderr %q",
			jsonFailure.exitCode, jsonFailure.stdout, jsonFailure.stderr, jsonFailureWant)
	}

	s := openTestStore(t, dbEnvPath(dbEnv))
	assertMatterCreateProjection(t, s, humanID, "dispatcher-parity", "Dispatcher parity")
	assertMatterCreateProjection(t, s, jsonMatter.ID, "dispatcher-json", "Dispatcher JSON parity")
}

func assertMatterCreateProjection(t *testing.T, s *store.Store, id, locator, title string) {
	t.Helper()
	ctx := context.Background()
	events, err := s.EventsOfSubject(ctx, id)
	if err != nil {
		t.Fatalf("events of %s: %v", id, err)
	}
	if len(events) != 1 {
		t.Fatalf("events of %s = %d, want exactly one", id, len(events))
	}
	event := events[0]
	if event.Type != store.TypeMatterCreated || event.Actor != store.ActorHuman || event.Subject != id ||
		event.Causation != event.ID || event.Correlation != event.ID || event.Repo == "" ||
		event.Clone != "" || event.Worktree != "" {
		t.Fatalf("matter.created envelope changed: %+v", event)
	}
	wantPayload := fmt.Sprintf("{\"title\":%q,\"locator\":%q,\"sort_key\":0}", title, locator)
	if string(event.Payload) != wantPayload {
		t.Fatalf("event payload = %s, want byte-for-byte %s", event.Payload, wantPayload)
	}

	node, err := s.Node(ctx, id)
	if err != nil {
		t.Fatalf("projected node %s: %v", id, err)
	}
	if node.ID != id || node.Kind != store.ScaleMatter || node.Repo != event.Repo || node.Matter != id ||
		node.Parent != "" || node.Locator != locator || node.Title != title || node.Lifecycle != store.Planned ||
		node.SortKey != 0 || node.ExternalRef != "" {
		t.Fatalf("matter projection changed: %+v", node)
	}
}
