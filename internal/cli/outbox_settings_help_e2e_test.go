package cli_test

import (
	"strings"
	"testing"
)

// `wip plumbing outbox canceled-label --help` and `wip plumbing outbox
// backlog-push --help` must each state the setting's effect, so an agent
// can configure a tracker from --help alone
// (tracker-config-discoverability step-03).
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
			"auto: wip plumbing backlog add also delegates the entry in the same commit",
			"auto with no tracker backend behaves as manual",
			"wip plumbing outbox approve and wip plumbing outbox flush",
		}},
		{"project", []string{
			"github and gitlab: ignore the project.",
			"linear: when set, every issue wip creates is filed in this project",
			"Pass none to clear it.",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			r := run(t, nil, "plumbing", "outbox", tc.verb, "--help")
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
