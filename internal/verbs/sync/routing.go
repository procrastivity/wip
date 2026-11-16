package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/config"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/spf13/cobra"
)

type (
	routeRule     struct{ Repo, Connection string }
	routingConfig struct {
		Connections map[string]struct{ Server string }
		Routes      []routeRule
	}
)

type selection struct {
	Connection, Server, Account, RemoteKey, Rule string
	CredentialPresent                            bool
}

func readRouting(explicit string) (routingConfig, error) {
	var out routingConfig
	c, err := config.Load()
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(c["sync"])
	if err != nil {
		return out, err
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil {
		return out, fmt.Errorf("sync: invalid routing configuration")
	}
	if raw := block["connections"]; raw != nil {
		if err := json.Unmarshal(raw, &out.Connections); err != nil {
			return out, fmt.Errorf("sync: invalid connection configuration")
		}
	}
	// Explicit selection must not decode unrelated malformed route data.
	if raw := block["routes"]; explicit == "" && raw != nil {
		if err := json.Unmarshal(raw, &out.Routes); err != nil {
			return out, fmt.Errorf("sync: invalid routing configuration")
		}
	}
	for name, definition := range out.Connections {
		if explicit != "" && name != explicit {
			continue
		}
		if !connectionName.MatchString(name) {
			return out, fmt.Errorf("sync: invalid configured connection name")
		}
		if _, err := serverURL(definition.Server); err != nil {
			return out, err
		}
	}
	return out, nil
}

// The input is the native Repo's already-selected/adopted normalized identity,
// not a fresh Git remote scrape. No file order tie-break or general globbing.
func selectRoute(key string, rules []routeRule, definitions routingConfig) (routeRule, error) {
	best, score := routeRule{}, -1
	tie := false
	for _, rule := range rules {
		if _, ok := definitions.Connections[rule.Connection]; !ok {
			return best, fmt.Errorf("sync: route references an undefined connection")
		}
		pattern := rule.Repo
		prefix := strings.HasSuffix(pattern, "/**")
		base := strings.TrimSuffix(pattern, "/**")
		if pattern != "**" {
			probe := base
			if prefix && !strings.Contains(base, "/") {
				probe += "/route-probe"
			}
			normal, err := tiers.NormalizeRemote("route://" + probe)
			if err != nil || normal != probe || strings.ContainsAny(base, "*?#\\ \t\r\n@") || (!prefix && !strings.Contains(base, "/")) {
				return best, fmt.Errorf("sync: route must be an exact normalized key, trailing /** prefix, or **")
			}
		}
		rank := -1
		switch {
		case key == "": // Even ** cannot turn a filesystem path into an identity.
		case pattern == "**":
			rank = 0
		case prefix && strings.HasPrefix(key, base+"/"):
			rank = len(base) + 1
		case !prefix && pattern == key:
			rank = len(key) + 2
		}
		if rank > score {
			best, score, tie = rule, rank, false
		} else if rank >= 0 && rank == score {
			tie = true
		}
	}
	if score < 0 {
		return best, fmt.Errorf("sync: no route for this native remote key; choose --connection")
	}
	if tie {
		return best, fmt.Errorf("sync: ambiguous equally specific routes")
	}
	return best, nil
}

func selectInitial(name, server, key string) (selection, error) {
	out := selection{Connection: name, RemoteKey: key}
	config, err := readRouting(name)
	if err != nil {
		return out, err
	}
	registry, err := loadRegistry()
	if err != nil {
		return out, err
	}
	if name == "" && len(config.Routes) > 0 {
		rule, err := selectRoute(key, config.Routes, config)
		if err != nil {
			return out, err
		}
		out.Connection, out.Rule = rule.Connection, rule.Repo
	}
	if out.Connection == "" {
		if len(registry.Connections) > 1 || len(config.Connections) > 1 {
			return out, fmt.Errorf("sync: choose --connection or configure a matching route")
		}
		// Only the legacy/default experience is implicit, never the last login.
		if len(registry.Connections) == 1 && registry.Connections["default"].Server == "" {
			return out, fmt.Errorf("sync: choose --connection")
		}
		if len(config.Connections) == 1 {
			if _, ok := config.Connections["default"]; !ok {
				return out, fmt.Errorf("sync: choose --connection")
			}
		}
		out.Connection = "default"
	}
	if !connectionName.MatchString(out.Connection) {
		return out, fmt.Errorf("sync: invalid connection name")
	}
	saved := registry.Connections[out.Connection]
	out.Server, out.Account, out.CredentialPresent = saved.Server, saved.Account, saved.Token != ""
	if definition, ok := config.Connections[out.Connection]; ok {
		origin, err := serverURL(definition.Server)
		if err != nil {
			return out, err
		}
		if out.Server != "" && out.Server != origin {
			return out, fmt.Errorf("sync: configured origin differs from saved immutable connection")
		}
		out.Server = origin
	}
	if server != "" {
		origin, err := serverURL(server)
		if err != nil {
			return out, err
		}
		if out.Server != "" && out.Server != origin {
			return out, fmt.Errorf("sync: selected connection belongs to another server")
		}
		out.Server = origin
	}
	if out.Server == "" {
		return out, fmt.Errorf("sync: connection needs an explicit server origin")
	}
	return out, nil
}

func selectBound(name, server string, status store.ExchangeStatus) (selection, error) {
	b := status.Binding
	if b.Connection == "" {
		// A legacy binding may attach only default at its retained origin.
		b.Connection = "default"
	}
	if name != "" && name != b.Connection {
		return selection{}, fmt.Errorf("sync: connection differs from immutable binding")
	}
	if server != "" {
		origin, err := serverURL(server)
		if err != nil {
			return selection{}, err
		}
		if !strings.HasPrefix(b.Endpoint, origin+"/api/") {
			return selection{}, fmt.Errorf("sync: binding belongs to another server")
		}
	}
	r, err := loadRegistry()
	if err != nil {
		return selection{}, err
	}
	c := r.Connections[b.Connection]
	if c.Server == "" || !strings.HasPrefix(b.Endpoint, c.Server+"/api/") || (b.Account != "" && b.Account != c.Account) {
		return selection{}, fmt.Errorf("sync: bound connection origin/account is unavailable or mismatched")
	}
	return selection{Connection: b.Connection, Server: c.Server, Account: c.Account, CredentialPresent: c.Token != ""}, nil
}

func connectionList() ([]map[string]any, error) {
	r, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(r.Connections))
	for name := range r.Connections {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		c := r.Connections[name]
		out = append(out, map[string]any{"name": name, "server": c.Server, "account": c.Account, "credential_present": c.Token != "", "validity": "not checked offline"})
	}
	return out, nil
}

func bindingSelection(name, server string, status store.ExchangeStatus) (selection, error) {
	b := status.Binding
	connection := b.Connection
	if connection == "" {
		connection = "default"
	}
	origin, _, ok := strings.Cut(b.Endpoint, "/api/")
	if !ok {
		return selection{}, fmt.Errorf("sync: invalid retained endpoint")
	}
	if name != "" && name != connection {
		return selection{}, fmt.Errorf("sync: connection differs from immutable binding")
	}
	if server != "" {
		requested, err := serverURL(server)
		if err != nil {
			return selection{}, err
		}
		if origin != requested {
			return selection{}, fmt.Errorf("sync: binding belongs to another server")
		}
	}
	return selection{Connection: connection, Server: origin, Account: b.Account}, nil
}

func onlineConnection(cmd *cobra.Command, selected selection) (credentials, error) {
	c, err := loadConnection(selected.Connection, selected.Server)
	if err != nil {
		return c, err
	}
	before := c
	if err := c.identify(cmd.Context()); err != nil {
		return c, err
	}
	if selected.Account != "" && selected.Account != c.Account {
		return c, fmt.Errorf("sync: bound account differs from authenticated account")
	}
	if before != c {
		if err := replaceConnection(selected.Connection, before, c); err != nil {
			return c, err
		}
	}
	return c, nil
}

func selectDiscovery(cmd *cobra.Command, name, server, repo string) (selection, error) {
	s, err := tiers.OpenStore()
	if err != nil {
		return selection{}, err
	}
	defer func() { _ = s.Close() }()
	var r store.Repo
	if repo != "" {
		r, err = tiers.ResolveRepo(cmd.Context(), s.View, repo)
		if err != nil {
			return selection{}, err
		}
	} else {
		dir, err := os.Getwd()
		if err != nil {
			return selection{}, err
		}
		clone, found, err := tiers.LookupCurrentClone(cmd.Context(), s.View, dir)
		if err != nil {
			return selection{}, err
		}
		if !found {
			return selectInitial(name, server, "")
		}
		r, err = s.Repo(cmd.Context(), clone.Repo)
		if err != nil {
			return selection{}, err
		}
	}
	status, err := s.ExchangeStatus(cmd.Context(), r.ID)
	if err != nil {
		return selection{}, err
	}
	if status.Paired {
		return selectBound(name, server, status)
	}
	return selectInitial(name, server, r.RemoteURL)
}
