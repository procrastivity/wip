package tiers

import (
	"fmt"

	"github.com/procrastivity/wip/internal/remotekey"
)

// NormalizeRemote turns a remote URL into wip's canonical Repo natural key:
// <host>[:port]/<path> — no scheme, no userinfo, no trailing `.git`, no
// trailing slash (tiers Brief, "Remote-URL normal form"). It parses all three
// shapes git itself accepts: the scp-like shorthand (`git@host:path`), and
// any `scheme://[user@]host[:port]/path` form (`https://…`, `ssh://…`).
func NormalizeRemote(raw string) (string, error) {
	key, err := remotekey.Normalize(raw)
	if err != nil {
		return "", fmt.Errorf("tiers: %w", err)
	}
	return key, nil
}
