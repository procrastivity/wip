package cli_test

import (
	"strings"
	"testing"
)

// `wip outbox canceled-label --help` and `wip outbox backlog-push --help` must
// each state the setting's effect, so an agent can configure a tracker from
// --help alone (tracker-config-discoverability step-03).
func TestOutboxSettingsHelpStateEachEffect(t *testing.T) {
	cases := []struct {
		verb  string
		wants []string
	}{
		{"canceled-label", []string{
			"the issue is closed and this label is added",
			"The label must already exist on the project",
			"github and linear: ignore the label.",
			"Pass none to clear it.",
		}},
		{"backlog-push", []string{
			"The default is manual, even when a tracker backend is configured.",
			"auto: wip backlog add also delegates the entry in the same commit",
			"auto with no tracker backend behaves as manual",
			"wip outbox approve and wip outbox flush",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			r := run(t, nil, "outbox", tc.verb, "--help")
			if r.exitCode != 0 {
				t.Fatalf("help: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
			}
			for _, want := range tc.wants {
				if !strings.Contains(r.stdout, want) {
					t.Errorf("%s --help missing %q in:\n%s", tc.verb, want, r.stdout)
				}
			}
		})
	}
}
