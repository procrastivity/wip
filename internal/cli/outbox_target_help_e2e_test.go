package cli_test

import (
	"strings"
	"testing"
)

// `wip outbox target --help` must say, per backend, whether the target is
// read and where the token comes from, so an agent can configure a tracker
// from --help alone (tracker-config-discoverability step-02, D2).
func TestOutboxTargetHelpDescribesEachBackend(t *testing.T) {
	r := run(t, nil, "outbox", "target", "--help")
	if r.exitCode != 0 {
		t.Fatalf("help: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	for _, want := range []string{
		"github: ignores the target.",
		"WIP_GITHUB_TOKEN, GH_TOKEN, or GITHUB_TOKEN",
		"gitlab: ignores the target.",
		"WIP_GITLAB_TOKEN, GITLAB_TOKEN, or the glab CLI's config.yml",
		"linear: requires the target, a team UUID.",
		"WIP_LINEAR_TOKEN or LINEAR_API_KEY",
		"Pass none to clear it.",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("target --help missing %q in:\n%s", want, r.stdout)
		}
	}
}
