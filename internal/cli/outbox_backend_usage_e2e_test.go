package cli_test

import (
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/tracker"
)

// The bracket list in `wip plumbing outbox backend --help` is rendered from
// the injected registry (workplan D1), never from a literal. A fake
// provider registered only in this test must therefore appear in the usage
// line.
func TestOutboxBackendUsageEnumeratesRegisteredBackends(t *testing.T) {
	providers := tracker.NewRegistry()
	providers.Register("zeta-fake", func(tracker.FactoryInput) (tracker.Seam, error) { return nil, nil })
	providers.Register("alpha-fake", func(tracker.FactoryInput) (tracker.Seam, error) { return nil, nil })

	r := runWithProviders(t, providers, "plumbing", "outbox", "backend", "--help")
	if r.exitCode != 0 {
		t.Fatalf("help: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	const want = "wip plumbing outbox backend [none|alpha-fake|zeta-fake]"
	if !strings.Contains(r.stdout, want) {
		t.Fatalf("usage line missing %q in:\n%s", want, r.stdout)
	}
}

// With no providers registered the usage line still offers the none sentinel.
func TestOutboxBackendUsageWithEmptyRegistry(t *testing.T) {
	r := runWithProviders(t, tracker.NewRegistry(), "plumbing", "outbox", "backend", "--help")
	if r.exitCode != 0 {
		t.Fatalf("help: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stdout, "wip plumbing outbox backend [none]") {
		t.Fatalf("usage line for empty registry:\n%s", r.stdout)
	}
}
