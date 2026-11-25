package sync

import (
	"context"
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

// Command exposes explicit exchange operations. Opt-in init onboarding uses
// the same operations; ordinary writes have no network dependency.
func Command(streams *iostreams.Streams) *cobra.Command {
	var server, repo, connection string
	root := &cobra.Command{Use: "sync", Short: "experimental explicit pairing and one-pass sync (bounded work families)", Long: "Explicit pairing and one explicit pass per invocation; never a background loop. Pairing is the only sync opt-in: once paired, old, new and previously imported portable work all follow one policy — no re-pairing, per-project grant, migration acceptance, mandatory preview or acknowledgement gate."}
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
	add(&cobra.Command{Use: "bindings", Short: "offline retained bindings in the selected store, including paused Repos", Long: "List all current retained local bindings, independent of checkout. Does not load credentials or contact a server. Select active Repos from .bindings[] in JSON output; --repo, --connection and --server do not apply.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		for _, flag := range []string{"repo", "connection", "server"} {
			if cmd.Flags().Changed(flag) {
				return wiperr.New("refusal.selection", "bindings inventories the selected store; --"+flag+" does not apply")
			}
		}
		s, err := tiers.OpenStore()
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		bindings, err := s.ExchangeBindings(cmd.Context())
		if err != nil {
			return err
		}
		return write(cmd, struct {
			Bindings []store.ExchangeBindingInfo `json:"bindings"`
		}{Bindings: bindings})
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
		out, err := discoverRepos(cmd.Context(), selected)
		if err != nil {
			return err
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
	pair := &cobra.Command{Use: "pair", Short: "explicit project association; portable originals sync regardless of age; no Git changes", Long: "Pairing is the only sync opt-in. Once paired, every supported original — old, new and previously imported work alike — follows this one policy: no re-pairing, per-project grant or migration acceptance. Unsupported families, missing prerequisites and incompatible formats stay local and inspectable. --dry-run previews selection without writes; inspection never gates application.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
				status, destination, err = applyPair(cmd.Context(), s, r.ID, selected, org, remote, nil)
				if err != nil {
					return err
				}
			}
			return write(cmd, map[string]any{"dry_run": dryPair, "selection": selected, "local": status, "destination": destination, "organization_id": org, "server_repo_id": remote, "destination_revalidated": !dryPair, "endpoint": endpoint, "initial_scope": "all supported unsynced originals regardless of age; unsupported families, missing prerequisites and incompatible formats remain inspectable local states", "installation": "allocated only on apply; reused on retry", "git_and_checkout": "unchanged; paths not transmitted"})
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
		cmd := &cobra.Command{Use: use, Short: map[string]string{"status": "offline transfer and reconciliation inventory", "unpair": "stop exchange; preserve evidence, work and resumable cursor", "run": "one explicit exchange pass; never a background loop", "activity": "offline canonical source-time observations and separate retained claims", "recheck": "reconsider canonical decisions, never force application", "ack": "acknowledge local handling, not shared resolution"}[name], Long: map[string]string{
			"status":   "Offline inventory of one Repo's exchange state; never contacts the server and never claims convergence. Categories: Queued portable originals awaiting upload acknowledgement; Acknowledged uploads the server retained; Received durable remote envelopes; Waiting received groups pending application, split into WaitingBytes held for spilled content and WaitingDependencies held for any other prerequisite; Applied groups integrated as local work; Conflicts retained conflict and collision claims; Excluded local command groups with no portable member, which never sync by design. Pairing is the only opt-in; old, new and imported work follow this one policy.",
			"run":      "One explicit exchange pass: upload queued originals, receive remote work and apply retained decisions, then re-report status. Never a background loop; safe to repeat because acknowledgements and cursors make retries idempotent. There is no mandatory preview or acknowledgement before application — pairing is the only opt-in.",
			"activity": "Offline canonical source-time observations retained from applied decisions, plus separately retained collision claims and local acknowledgements. Reported time is source observation, not remote liveness or duration. Inspection only: it never gates `sync run`, and pairing is the only opt-in.",
		}[name], Args: args, RunE: func(cmd *cobra.Command, args []string) error {
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
					status, err = runBound(cmd.Context(), s, r.ID)
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
	addResetCommands(root, streams, write, &connection, &server)
	return root
}

func (c apiClient) list(ctx context.Context, path string) ([]json.RawMessage, error) {
	var result []json.RawMessage
	for page := 1; ; page++ {
		var data struct {
			Data []json.RawMessage `json:"data"`
			Next *string           `json:"next_page_url"`
		}
		if err := c.request(ctx, "GET", fmt.Sprintf("%s?page=%d", path, page), nil, &data); err != nil {
			return nil, err
		}
		result = append(result, data.Data...)
		if data.Next == nil {
			return result, nil
		}
	}
}
