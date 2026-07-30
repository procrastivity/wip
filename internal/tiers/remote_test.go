package tiers

import "testing"

// TestNormalizeRemote_WorkedTable exercises the tiers Brief's own worked
// table verbatim ("Remote-URL normal form") — the acceptance reference
// step-01 of identity-rules names for this Step.
func TestNormalizeRemote_WorkedTable(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"git@github.com:procrastivity/wip.git", "github.com/procrastivity/wip"},
		{"https://github.com/procrastivity/wip.git", "github.com/procrastivity/wip"},
		{"ssh://git@github.com:22/procrastivity/wip/", "github.com/procrastivity/wip"},
		{"ssh://git@example.com:2222/team/repo.git", "example.com:2222/team/repo"},
	}
	for _, c := range cases {
		got, err := NormalizeRemote(c.input)
		if err != nil {
			t.Fatalf("NormalizeRemote(%q): %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestNormalizeRemote_EdgeCases covers the additional cases step-09 names:
// default vs. non-default port, .git suffix variants, mixed-case host.
func TestNormalizeRemote_EdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"mixed-case host is lowercased", "https://GitHub.COM/Acme/Widget", "github.com/Acme/Widget"},
		{"no .git suffix", "https://github.com/acme/widget", "github.com/acme/widget"},
		{"trailing slash, no .git", "https://github.com/acme/widget/", "github.com/acme/widget"},
		{"scp-like with no userinfo", "github.com:acme/widget.git", "github.com/acme/widget"},
		{"https default port 443 omitted", "https://github.com:443/acme/widget.git", "github.com/acme/widget"},
		{"https non-default port kept", "https://example.com:8443/acme/widget.git", "example.com:8443/acme/widget"},
		{"ssh non-default port, no userinfo", "ssh://example.com:2222/team/repo", "example.com:2222/team/repo"},
		{"userinfo always stripped even with a plain user", "https://alice@example.com/acme/widget.git", "example.com/acme/widget"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeRemote(c.input)
			if err != nil {
				t.Fatalf("NormalizeRemote(%q): %v", c.input, err)
			}
			if got != c.want {
				t.Errorf("NormalizeRemote(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestNormalizeRemote_TwoInputsSameNormalForm confirms the whole point of
// normalization: the same repo reached two different ways is one Repo.
func TestNormalizeRemote_TwoInputsSameNormalForm(t *testing.T) {
	a, err := NormalizeRemote("git@github.com:acme/widget.git")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NormalizeRemote("https://github.com/acme/widget.git")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("scp-like and https forms of the same remote normalized differently: %q vs %q", a, b)
	}
}

func TestNormalizeRemote_Rejects(t *testing.T) {
	for _, input := range []string{"", "   ", "not-a-remote-at-all"} {
		if _, err := NormalizeRemote(input); err == nil {
			t.Errorf("NormalizeRemote(%q) succeeded, want an error", input)
		}
	}
}
