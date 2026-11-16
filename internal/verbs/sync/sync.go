package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/spf13/cobra"
)

// Command never runs automatically. Ordinary writes have no dependency on this
// namespace, credentials, a server or an active association.
func Command(streams *iostreams.Streams) *cobra.Command {
	var server, repo, connection string
	root := &cobra.Command{Use: "sync", Short: "experimental explicit pairing and one-pass sync (bounded work families)"}
	root.PersistentFlags().StringVar(&server, "server", "", "explicit Laravel origin (use HTTPS except disposable local testing)")
	root.PersistentFlags().StringVar(&repo, "repo", "", "native Repo locator; defaults to current clone")
	root.PersistentFlags().StringVar(&connection, "connection", "", "named server/account connection; bound Repos retain their selection")
	write := func(cmd *cobra.Command, value any) error {
		if cliflags.FromContext(cmd.Context()).JSON {
			return json.NewEncoder(streams.Out).Encode(value)
		}
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(raw))
		return err
	}
	add := func(cmd *cobra.Command) {
		run := cmd.RunE
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			err := run(cmd, args)
			if err == nil {
				return nil
			}
			if _, ok := err.(*wiperr.Error); ok {
				return err
			}
			return wiperr.New("sync.failed", err.Error())
		}
		surface.Annotate(cmd, surface.Plumbing)
		root.AddCommand(cmd)
	}
	var email, passwordFile string
	loginCmd := &cobra.Command{Use: "login", Short: "log in through Laravel and save an owner-private token outside the checkout", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		name := connection
		if name == "" {
			name = "default"
		}
		selected, err := selectInitial(name, server, "")
		if err != nil {
			return err
		}
		if email == "" {
			return wiperr.New("refusal.credentials", "--email is required; passwords are never command arguments")
		}
		if err := login(cmd.Context(), name, selected.Server, email, passwordFile, streams.Err); err != nil {
			return err
		}
		return write(cmd, map[string]any{"authenticated": true, "server": selected.Server, "connection": name})
	}}
	loginCmd.Flags().StringVar(&email, "email", "", "verified account email")
	loginCmd.Flags().StringVar(&passwordFile, "password-file", "", "protected regular file containing password; otherwise hidden terminal prompt")
	add(loginCmd)
	add(&cobra.Command{Use: "connections", Short: "offline saved connection identities, never tokens or a validity claim", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		list, err := connectionList()
		if err != nil {
			return err
		}
		return write(cmd, list)
	}})
	add(&cobra.Command{Use: "logout", Short: "revoke the saved token; preserve local work and bindings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		name := connection
		if name == "" {
			name = "default"
		}
		c, err := loadConnection(name, server)
		if err != nil {
			return err
		}
		before := c
		if c.Account == "" {
			// An old revoked token can still be forgotten; a live legacy token
			// establishes the account pin before removing it.
			if err := c.identify(cmd.Context()); err != nil {
				var status *httpStatusError
				if !errors.As(err, &status) || status.status != 401 {
					return err
				}
			}
		}
		if err := newAPI(c.Server, c.Token).request(cmd.Context(), "DELETE", "/api/token", nil, nil); err != nil {
			var status *httpStatusError
			if !errors.As(err, &status) || status.status != 401 {
				return err
			}
		}
		c.Token = ""
		if err := replaceConnection(name, before, c); err != nil {
			return err
		}
		return write(cmd, map[string]any{"logged_out": true, "connection": name})
	}})
	add(&cobra.Command{Use: "repos", Short: "list accessible organizations and all paginated Repos; never infer a match", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		selected, err := selectDiscovery(cmd, connection, server, repo)
		if err != nil {
			return err
		}
		c, err := onlineConnection(cmd, selected)
		if err != nil {
			return err
		}
		api := newAPI(c.Server, c.Token)
		orgs, err := api.list(cmd, "/api/organizations")
		if err != nil {
			return err
		}
		var out []map[string]any
		for _, org := range orgs {
			var identity struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(org, &identity); err != nil {
				return err
			}
			repos, err := api.list(cmd, "/api/organizations/"+identity.ID+"/repos")
			if err != nil {
				return err
			}
			out = append(out, map[string]any{"organization": org, "repos": repos})
		}
		return write(cmd, out)
	}})
	withRepo := func(cmd *cobra.Command, fn func(*store.Store, store.Repo) error) error {
		s, err := tiers.OpenStore()
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		var r store.Repo
		if repo != "" {
			r, err = tiers.ResolveRepo(cmd.Context(), s.View, repo)
		} else {
			dir, e := os.Getwd()
			if e != nil {
				return e
			}
			clone, found, e := tiers.LookupCurrentClone(cmd.Context(), s.View, dir)
			if e != nil {
				return e
			}
			if !found {
				return wiperr.New("refusal.unknown-clone", "unknown clone; choose --repo or initialize it first")
			}
			r, err = s.Repo(cmd.Context(), clone.Repo)
		}
		if err != nil {
			return err
		}
		return fn(s, r)
	}
	add(&cobra.Command{Use: "route", Short: "offline route explanation; an existing binding always wins", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withRepo(cmd, func(s *store.Store, r store.Repo) error {
			status, err := s.ExchangeStatus(cmd.Context(), r.ID)
			if err != nil {
				return err
			}
			suggestion, routeErr := selectInitial(connection, server, r.RemoteURL)
			if !status.Paired {
				if routeErr != nil {
					return routeErr
				}
				return write(cmd, map[string]any{"repo": r.ID, "selection": suggestion, "paired": false})
			}
			effective, err := bindingSelection(connection, server, status)
			if err != nil {
				return err
			}
			diagnostic := ""
			if routeErr != nil {
				diagnostic = routeErr.Error()
			}
			credentialError := ""
			if saved, err := selectBound(connection, server, status); err == nil {
				effective.CredentialPresent = saved.CredentialPresent
			} else {
				credentialError = err.Error()
			}
			return write(cmd, map[string]any{"repo": r.ID, "remote_key": r.RemoteURL, "binding": status.Binding, "effective": effective, "suggestion": suggestion, "route_error": diagnostic, "credential_error": credentialError, "routing": "informational only; binding wins"})
		})
	}})
	var org, remote string
	var dryPair bool
	pair := &cobra.Command{Use: "pair", Short: "explicit future-work association; no existing-history merge or Git changes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withRepo(cmd, func(s *store.Store, r store.Repo) error {
			if org == "" || remote == "" || strings.ContainsAny(org+remote, "/?#%") {
				return wiperr.New("refusal.destination", "choose --org and --server-repo stable IDs from sync repos")
			}
			status, err := s.ExchangeStatus(cmd.Context(), r.ID)
			if err != nil {
				return err
			}
			var selected selection
			switch {
			case status.Paired:
				selected, err = bindingSelection(connection, server, status)
			case dryPair && server != "" && connection == "":
				selected.Server, err = serverURL(server)
				selected.RemoteKey = r.RemoteURL
			default:
				selected, err = selectInitial(connection, server, r.RemoteURL)
			}
			if err != nil {
				return err
			}
			path := "/api/organizations/" + org + "/repos/" + remote
			var destination json.RawMessage
			endpoint := selected.Server + path
			if status.Paired && status.Binding.Endpoint != endpoint {
				return wiperr.New("refusal.binding", "destination is immutable; unpair preserves its association, not a retarget operation")
			}
			if !dryPair {
				if status.Paired {
					selected, err = selectBound(connection, server, status)
					if err != nil {
						return err
					}
				}
				c, err := onlineConnection(cmd, selected)
				if err != nil {
					return err
				}
				api := newAPI(c.Server, c.Token)
				if err := api.request(cmd.Context(), "GET", path, nil, &destination); err != nil {
					return err
				}
				status, err = s.PairExchangeConnection(cmd.Context(), r.ID, endpoint, selected.Connection, c.Account)
				if err != nil {
					return err
				}
				if err := api.request(cmd.Context(), "POST", path+"/origins", map[string]string{"installation_id": status.Binding.InstallationID}, nil); err != nil {
					return err
				}
			}
			return write(cmd, map[string]any{"dry_run": dryPair, "selection": selected, "local": status, "destination": destination, "organization_id": org, "server_repo_id": remote, "destination_revalidated": !dryPair, "endpoint": endpoint, "initial_scope": "future originals only; earlier history not adopted; required pre-boundary dependencies may wait", "installation": "allocated only on apply; reused on retry", "git_and_checkout": "unchanged; paths not transmitted"})
		})
	}}
	pair.Flags().StringVar(&org, "org", "", "explicit organization stable ID")
	pair.Flags().StringVar(&remote, "server-repo", "", "explicit server Repo stable ID")
	pair.Flags().BoolVar(&dryPair, "dry-run", false, "read-only selection/inventory; no origin or local sync writes")
	add(pair)
	for _, name := range []string{"status", "unpair", "run", "activity", "recheck", "ack"} {
		name := name
		var dry bool
		args := cobra.NoArgs
		use := name
		if name == "ack" {
			args = cobra.ExactArgs(1)
			use += " <group-id>"
		}
		if name == "recheck" {
			args = cobra.MaximumNArgs(1)
			use += " [group-id]"
		}
		cmd := &cobra.Command{Use: use, Short: map[string]string{"status": "offline transfer and reconciliation inventory", "unpair": "stop exchange; preserve evidence, work and resumable cursor", "run": "one explicit exchange pass; never a background loop", "activity": "offline canonical source-time observations and separate retained claims", "recheck": "reconsider canonical decisions, never force application", "ack": "acknowledge local handling, not shared resolution"}[name], Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			return withRepo(cmd, func(s *store.Store, r store.Repo) error {
				status, err := s.ExchangeStatus(cmd.Context(), r.ID)
				if err != nil {
					return err
				}
				if status.Paired {
					if _, err := bindingSelection(connection, server, status); err != nil {
						return err
					}
				}
				switch name {
				case "status":
					return write(cmd, status)
				case "unpair":
					if err := s.UnpairExchange(cmd.Context(), r.ID); err != nil {
						return err
					}
					status, err = s.ExchangeStatus(cmd.Context(), r.ID)
				case "run":
					if dry {
						return write(cmd, map[string]any{"dry_run": true, "local": status, "download": "not fetched; remote work and decisions may change concurrently", "application": "no effects or checkpoint changes"})
					}
					if !status.Active {
						return fmt.Errorf("sync: Repo is not actively paired")
					}
					selected, e := selectBound(connection, server, status)
					if e != nil {
						return e
					}
					c, e := onlineConnection(cmd, selected)
					if e != nil {
						return e
					}
					if _, e := s.PairExchangeConnection(cmd.Context(), r.ID, status.Binding.Endpoint, selected.Connection, c.Account); e != nil {
						return e
					}
					api := newAPI(c.Server, c.Token)
					status, err = s.RunExchange(cmd.Context(), store.ExchangeClient{HTTP: api.http, Token: c.Token}, r.ID)
				case "activity":
					if !status.Paired {
						return write(cmd, status)
					}
					preview, e := readsurface.PreviewExchangeActivity(cmd.Context(), s, status.Binding.InstallationID, r.ID, 30*time.Minute)
					if e != nil {
						return e
					}
					return write(cmd, map[string]any{"binding": status.Binding, "canonical": preview.Activities, "periods": preview.Periods, "decisions": status.Decisions, "collision_claims": status.Collisions, "acknowledged": status.Acknowledged, "reported_time": "source observations, not remote liveness or duration"})
				case "recheck", "ack":
					if !status.Paired {
						return wiperr.New("refusal.binding", "no pairing evidence for this Repo")
					}
					if name == "recheck" && len(args) == 0 {
						if err := s.ApplyExchangeInbox(cmd.Context(), status.Binding); err != nil {
							return err
						}
						status, err = s.ExchangeStatus(cmd.Context(), r.ID)
						if err != nil {
							return err
						}
					}
					found := len(args) == 0
					for _, d := range status.Decisions {
						id := d.Source.Actions[0].Event.ID
						if len(args) > 0 && args[0] != id {
							continue
						}
						found = true
						if name == "ack" {
							_, err = s.ExperimentAcknowledgeBound(cmd.Context(), r.ID, id)
						} else {
							_, err = s.ExperimentRecheckBound(cmd.Context(), r.ID, id)
						}
						if err != nil {
							return err
						}
					}
					if !found {
						return wiperr.New("refusal.group", "group is not retained for this Repo")
					}
					status, err = s.ExchangeStatus(cmd.Context(), r.ID)
				}
				if err != nil {
					return err
				}
				return write(cmd, status)
			})
		}}
		if name == "run" {
			cmd.Flags().BoolVar(&dry, "dry-run", false, "offline read-only inventory; no capture, HTTP or application")
		}
		add(cmd)
	}
	return root
}

func (c apiClient) list(cmd *cobra.Command, path string) ([]json.RawMessage, error) {
	var result []json.RawMessage
	for page := 1; ; page++ {
		var data struct {
			Data []json.RawMessage `json:"data"`
			Next *string           `json:"next_page_url"`
		}
		if err := c.request(cmd.Context(), "GET", fmt.Sprintf("%s?page=%d", path, page), nil, &data); err != nil {
			return nil, err
		}
		result = append(result, data.Data...)
		if data.Next == nil {
			return result, nil
		}
	}
}
