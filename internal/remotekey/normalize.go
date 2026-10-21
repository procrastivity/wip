// Package remotekey normalizes Git remote URLs without depending on the local
// Store or command layers.
package remotekey

import (
	"fmt"
	"net/url"
	"strings"
)

var defaultPorts = map[string]string{
	"ssh":   "22",
	"http":  "80",
	"https": "443",
	"git":   "9418",
}

// Normalize turns a remote URL into the canonical Repo natural key:
// <host>[:port]/<path> without scheme, userinfo, trailing .git, or slash.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("remotekey: empty remote URL")
	}
	var host, port, path string
	if scheme, hasScheme := schemeOf(raw); hasScheme {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("remotekey: parse remote %q: %w", raw, err)
		}
		host = u.Hostname()
		port = u.Port()
		if port != "" && port == defaultPorts[scheme] {
			port = ""
		}
		path = u.Path
	} else {
		rest := raw
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			rest = raw[at+1:]
		}
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", fmt.Errorf("remotekey: %q is not a recognized remote URL shape", raw)
		}
		host, path = rest[:colon], rest[colon+1:]
	}
	if host == "" {
		return "", fmt.Errorf("remotekey: remote %q carries no host", raw)
	}
	host = strings.ToLower(host)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", fmt.Errorf("remotekey: remote %q carries no path", raw)
	}
	if port != "" {
		return host + ":" + port + "/" + path, nil
	}
	return host + "/" + path, nil
}

func schemeOf(raw string) (string, bool) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", false
	}
	return raw[:i], true
}
