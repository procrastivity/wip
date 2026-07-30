package tiers

import (
	"fmt"
	"net/url"
	"strings"
)

// defaultPorts maps a URL scheme to the port implied when none is written
// explicitly. The tiers Brief states the rule for ssh only ("omitted when it
// equals the scheme's default (22 for ssh)"), because ssh is the only scheme
// the worked table needs it for, but the rule itself is scheme-general —
// "the scheme's default" — so it is written that way here rather than
// special-cased to ssh alone.
var defaultPorts = map[string]string{
	"ssh":   "22",
	"http":  "80",
	"https": "443",
	"git":   "9418",
}

// NormalizeRemote turns a remote URL into wip's canonical Repo natural key:
// <host>[:port]/<path> — no scheme, no userinfo, no trailing `.git`, no
// trailing slash (tiers Brief, "Remote-URL normal form"). It parses all three
// shapes git itself accepts: the scp-like shorthand (`git@host:path`), and
// any `scheme://[user@]host[:port]/path` form (`https://…`, `ssh://…`).
func NormalizeRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("tiers: empty remote URL")
	}

	var host, port, path string
	if scheme, hasScheme := schemeOf(raw); hasScheme {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("tiers: parse remote %q: %w", raw, err)
		}
		host = u.Hostname()
		port = u.Port()
		if port != "" && port == defaultPorts[scheme] {
			port = ""
		}
		path = u.Path
	} else {
		// scp-like shorthand: [user@]host:path. There is no port in this
		// syntax — git itself does not support one here.
		rest := raw
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			rest = raw[at+1:]
		}
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", fmt.Errorf("tiers: %q is not a recognized remote URL shape", raw)
		}
		host = rest[:colon]
		path = rest[colon+1:]
	}

	if host == "" {
		return "", fmt.Errorf("tiers: remote %q carries no host", raw)
	}
	// DNS is case-insensitive.
	host = strings.ToLower(host)

	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", fmt.Errorf("tiers: remote %q carries no path", raw)
	}

	if port != "" {
		return host + ":" + port + "/" + path, nil
	}
	return host + "/" + path, nil
}

// schemeOf reports whether raw carries an explicit "scheme://" prefix, and
// the scheme itself when it does.
func schemeOf(raw string) (string, bool) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", false
	}
	return raw[:i], true
}
