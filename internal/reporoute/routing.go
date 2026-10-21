// Package reporoute resolves D54 provisioning routes without mutating
// authority or Environment state.
package reporoute

import (
	"errors"
	"strings"
)

var (
	// ErrNoMatch means no D54 route matches the normalized key.
	ErrNoMatch = errors.New("reporoute: no route matches normalized Repo key")
	// ErrAmbiguous means multiple equally specific D54 routes match the key.
	ErrAmbiguous = errors.New("reporoute: equally specific routes match normalized Repo key")
	// ErrInvalidRoute means a D54 route entry is malformed.
	ErrInvalidRoute = errors.New("reporoute: invalid route")
	// ErrBindingDisagrees means a selected route conflicts with remembered identity.
	ErrBindingDisagrees = errors.New("reporoute: route disagrees with persisted Environment identity")
	// ErrBindingUnresolved means route identity is missing or its domain disagrees.
	ErrBindingUnresolved = errors.New("reporoute: route has no authority Repo binding")
)

// Entry maps one normalized Repo key rule to an authority domain and a
// separately named connection profile. Profile is deliberately not part of
// the persisted Repo/domain/epoch identity.
type Entry struct {
	Key               string
	DomainID          string
	ConnectionProfile string
}

// Binding is the authority-derived Repo/domain/epoch tuple already remembered
// by an Environment. It excludes connection profile and endpoint origin.
type Binding struct {
	RepoID   string
	DomainID string
	Epoch    uint64
}

// Resolved is a read-only route result. RepoID and Epoch come from the
// authority-owned remote-key index and membership/domain records; Profile
// comes from D54 provisioning configuration.
type Resolved struct {
	RepoID            string
	DomainID          string
	Epoch             uint64
	ConnectionProfile string
}

// Select performs only the D54 configuration match. It intentionally does not
// require an authority binding, so callers can report a read-only unbound
// result with the selected profile and provisioning target.
func Select(entries []Entry, normalizedKey string) (Entry, error) {
	if !validNormalizedKey(normalizedKey) {
		return Entry{}, ErrInvalidRoute
	}
	best := -1
	bestPrefix := -1
	bestEntry := Entry{}
	var tied bool
	for _, entry := range entries {
		if !validEntry(entry) {
			return Entry{}, ErrInvalidRoute
		}
		specificity, matches := matchSpecificity(entry.Key, normalizedKey)
		if !matches {
			continue
		}
		if specificity > best || specificity == best && specificity > 0 && prefixLength(entry.Key) > bestPrefix {
			best, bestPrefix, bestEntry, tied = specificity, prefixLength(entry.Key), entry, false
		} else if specificity == best && prefixLength(entry.Key) == bestPrefix {
			tied = true
		}
	}
	if best < 0 {
		return Entry{}, ErrNoMatch
	}
	if tied {
		return Entry{}, ErrAmbiguous
	}
	return bestEntry, nil
}

// Resolve selects exact keys before the longest trailing-/** prefix and then
// the explicit ** catch-all. A missing or equally-specific best match fails
// closed. When remembered is non-nil, a different Repo/domain/epoch refuses;
// profile changes alone do not alter identity.
func Resolve(entries []Entry, normalizedKey string, authority Binding, remembered *Binding) (Resolved, error) {
	if !validNormalizedKey(normalizedKey) || authority.RepoID == "" || authority.DomainID == "" || authority.Epoch == 0 {
		return Resolved{}, ErrBindingUnresolved
	}
	selected, err := Select(entries, normalizedKey)
	if err != nil {
		return Resolved{}, err
	}
	if authority.DomainID != selected.DomainID {
		return Resolved{}, ErrBindingUnresolved
	}
	if remembered != nil && *remembered != authority {
		return Resolved{}, ErrBindingDisagrees
	}
	return Resolved{
		RepoID: authority.RepoID, DomainID: authority.DomainID, Epoch: authority.Epoch,
		ConnectionProfile: selected.ConnectionProfile,
	}, nil
}

func matchSpecificity(rule, key string) (int, bool) {
	switch {
	case rule == key:
		return 3, true
	case rule == "**":
		return 1, true
	case strings.HasSuffix(rule, "/**"):
		prefix := strings.TrimSuffix(rule, "**")
		return 2, strings.HasPrefix(key, prefix)
	default:
		return 0, false
	}
}

func prefixLength(rule string) int {
	if strings.HasSuffix(rule, "/**") {
		return len(strings.TrimSuffix(rule, "**"))
	}
	return 0
}

func validEntry(entry Entry) bool {
	if entry.DomainID == "" || entry.ConnectionProfile == "" || strings.TrimSpace(entry.ConnectionProfile) != entry.ConnectionProfile {
		return false
	}
	if entry.Key == "**" {
		return true
	}
	if strings.HasSuffix(entry.Key, "/**") {
		prefix := strings.TrimSuffix(entry.Key, "/**")
		return validNormalizedKey(prefix) && !strings.Contains(prefix, "*")
	}
	return validNormalizedKey(entry.Key)
}

func validNormalizedKey(key string) bool {
	return key != "" && strings.TrimSpace(key) == key && !strings.ContainsAny(key, "*?#") &&
		!strings.Contains(key, "://") && !strings.HasPrefix(key, "/") && !strings.HasSuffix(key, "/") &&
		!strings.HasSuffix(key, ".git") && strings.Contains(key, "/")
}
