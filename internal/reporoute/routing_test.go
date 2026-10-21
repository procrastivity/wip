package reporoute

import (
	"errors"
	"testing"
)

const (
	routeRepoA   = "01J00000000000000000000001"
	routeRepoB   = "01J00000000000000000000002"
	routeDomainA = "01J00000000000000000000003"
	routeDomainB = "01J00000000000000000000004"
)

func TestResolveUsesExactThenLongestPrefixThenExplicitCatchAll(t *testing.T) {
	entries := []Entry{
		{Key: "**", DomainID: routeDomainB, ConnectionProfile: "fallback"},
		{Key: "github.com/acme/**", DomainID: routeDomainB, ConnectionProfile: "broad"},
		{Key: "github.com/acme/tools/**", DomainID: routeDomainB, ConnectionProfile: "long"},
		{Key: "github.com/acme/tools/agent", DomainID: routeDomainA, ConnectionProfile: "exact"},
	}
	tests := []struct {
		key     string
		binding Binding
		profile string
	}{
		{"github.com/acme/tools/agent", Binding{RepoID: routeRepoA, DomainID: routeDomainA, Epoch: 4}, "exact"},
		{"github.com/acme/tools/other", Binding{RepoID: routeRepoB, DomainID: routeDomainB, Epoch: 3}, "long"},
		{"github.com/other/repo", Binding{RepoID: routeRepoA, DomainID: routeDomainB, Epoch: 1}, "fallback"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			got, err := Resolve(entries, test.key, test.binding, nil)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.RepoID != test.binding.RepoID || got.DomainID != test.binding.DomainID ||
				got.Epoch != test.binding.Epoch || got.ConnectionProfile != test.profile {
				t.Fatalf("Resolve() = %#v, want binding %#v and profile %q", got, test.binding, test.profile)
			}
		})
	}
}

func TestResolveRefusesAmbiguousAndUnmatchedKeys(t *testing.T) {
	base := []Entry{
		{Key: "github.com/acme/**", DomainID: routeDomainA, ConnectionProfile: "one"},
		{Key: "github.com/acme/**", DomainID: routeDomainB, ConnectionProfile: "two"},
	}
	binding := Binding{RepoID: routeRepoA, DomainID: routeDomainA, Epoch: 1}
	if _, err := Resolve(base, "github.com/acme/repo", binding, nil); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("equal-best routes error = %v, want ErrAmbiguous", err)
	}
	if _, err := Resolve(nil, "github.com/acme/repo", binding, nil); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("no-match error = %v, want ErrNoMatch", err)
	}
	if _, err := Resolve([]Entry{{Key: "**", DomainID: routeDomainB, ConnectionProfile: "fallback"}},
		"github.com/acme/repo", binding, nil); !errors.Is(err, ErrBindingUnresolved) {
		t.Fatalf("route/domain disagreement error = %v, want ErrBindingUnresolved", err)
	}
}

func TestSelectResolvesProvisioningTargetWithoutAuthorityBinding(t *testing.T) {
	entries := []Entry{
		{Key: "**", DomainID: routeDomainB, ConnectionProfile: "fallback"},
		{Key: "github.com/acme/**", DomainID: routeDomainA, ConnectionProfile: "acme"},
		{Key: "github.com/acme/widget", DomainID: routeDomainB, ConnectionProfile: "exact"},
	}
	got, err := Select(entries, "github.com/acme/widget")
	if err != nil || got.DomainID != routeDomainB || got.ConnectionProfile != "exact" {
		t.Fatalf("unbound D54 selection = %+v, %v", got, err)
	}
	if _, err = Select(nil, "github.com/acme/widget"); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("unbound no-match = %v, want ErrNoMatch", err)
	}
	ambiguous := []Entry{
		{Key: "github.com/acme/**", DomainID: routeDomainA, ConnectionProfile: "one"},
		{Key: "github.com/acme/**", DomainID: routeDomainB, ConnectionProfile: "two"},
	}
	if _, err = Select(ambiguous, "github.com/acme/widget"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("unbound ambiguity = %v, want ErrAmbiguous", err)
	}
}

func TestResolveFencesRememberedIdentityButNotProfileChange(t *testing.T) {
	entries := []Entry{{Key: "github.com/acme/repo", DomainID: routeDomainA, ConnectionProfile: "new-profile"}}
	current := Binding{RepoID: routeRepoA, DomainID: routeDomainA, Epoch: 5}
	remembered := current
	got, err := Resolve(entries, "github.com/acme/repo", current, &remembered)
	if err != nil || got.ConnectionProfile != "new-profile" {
		t.Fatalf("profile-only change Resolve() = %#v, %v", got, err)
	}
	remembered.Epoch++
	if _, err = Resolve(entries, "github.com/acme/repo", current, &remembered); !errors.Is(err, ErrBindingDisagrees) {
		t.Fatalf("epoch disagreement error = %v, want ErrBindingDisagrees", err)
	}
	remembered = current
	remembered.RepoID = routeRepoB
	if _, err = Resolve(entries, "github.com/acme/repo", current, &remembered); !errors.Is(err, ErrBindingDisagrees) {
		t.Fatalf("Repo disagreement error = %v, want ErrBindingDisagrees", err)
	}
}

func TestResolveRejectsInvalidConfigurationEvenWhenOtherEntryMatches(t *testing.T) {
	entries := []Entry{
		{Key: "github.com/acme/repo", DomainID: routeDomainA, ConnectionProfile: "valid"},
		{Key: "github.com/acme/**/nested", DomainID: routeDomainA, ConnectionProfile: "invalid"},
	}
	binding := Binding{RepoID: routeRepoA, DomainID: routeDomainA, Epoch: 1}
	if _, err := Resolve(entries, "github.com/acme/repo", binding, nil); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("invalid config error = %v, want ErrInvalidRoute", err)
	}
}
