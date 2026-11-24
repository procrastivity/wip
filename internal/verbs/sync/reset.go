package sync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/procrastivity/wip/internal/asset"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Recovery retains typed nonsecret identity/diagnostics, never credential bytes.
// Absence is separate from the hash of an empty saved entry.
type resetJournal struct {
	Version                       int
	ID, Connection, Store, Config string
	CredentialHash                string
	CredentialPresent             bool
	ConfigBefore, ConfigAfter     string
	Pending                       map[string]string
	Server, Account               string
	PendingRequests               map[string]pendingCreation
	Configuration                 resetConfigPreview
	Issues                        []resetRecordIssue
}

type resetRecordIssue struct {
	Path, Classification string
}

type resetConfigPreview struct {
	Definition   *struct{ Server string }
	Routes       []routeRule
	CleanupIssue string
}

// Inspect only the selected fields; never serialize arbitrary YAML values or
// unknown creation fields into previews/journals (they may contain secrets).
func previewResetConfig(raw []byte, name string) resetConfigPreview {
	var out resetConfigPreview
	if _, err := cleanConfig(raw, name); err != nil {
		out.CleanupIssue = err.Error()
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 {
		return out
	}
	field := func(n *yaml.Node, key string) *yaml.Node {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	block := field(doc.Content[0], "sync")
	if definition := field(field(block, "connections"), name); definition != nil {
		out.Definition = &struct{ Server string }{}
		if server := field(definition, "server"); server != nil && server.Kind == yaml.ScalarNode {
			out.Definition.Server = server.Value
		}
	}
	if routes := field(block, "routes"); routes != nil && routes.Kind == yaml.SequenceNode {
		for _, node := range routes.Content {
			connection := field(node, "connection")
			repo := field(node, "repo")
			if (connection == nil || connection.Tag != "!!str" || repo == nil || repo.Tag != "!!str") && out.CleanupIssue == "" {
				out.CleanupIssue = "malformed routing record retained unless identified as selected; selected store retirement is unaffected"
			}
			if connection == nil || connection.Value != name {
				continue
			}
			rule := routeRule{Connection: name}
			if repo := field(node, "repo"); repo != nil && repo.Kind == yaml.ScalarNode {
				rule.Repo = repo.Value
			}
			if join := field(node, "join"); join != nil {
				_ = join.Decode(&rule.Join)
			}
			out.Routes = append(out.Routes, rule)
		}
	}
	return out
}

func digest(raw []byte) string              { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func credentialDigest(c credentials) string { raw, _ := json.Marshal(c); return digest(raw) }

func resetPath(name string) (string, error) {
	if !connectionName.MatchString(name) {
		return "", fmt.Errorf("sync: invalid connection name")
	}
	p, err := credentialPath()
	return filepath.Join(filepath.Dir(p), "forget-"+name+".json"), err
}

func checkResetPending(name string) error {
	if name == "" {
		name = "default"
	}
	p, err := resetPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err == nil {
		return fmt.Errorf("sync: connection %q has pending offline cleanup; resume connection forget %s in its original selected store", name, name)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readReset(name string) (*resetJournal, error) {
	p, err := resetPath(name)
	if err != nil {
		return nil, err
	}
	raw, err := readPrivate(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j resetJournal
	if json.Unmarshal(raw, &j) != nil || j.Version != 1 || j.Connection != name || j.ID == "" {
		return nil, fmt.Errorf("sync: invalid reset journal")
	}
	return &j, nil
}

// YAML nodes preserve unrelated keys, comments and malformed unrelated rule
// values. An unidentifiable routes container fails cleanup, AFTER retirement.
func cleanConfig(raw []byte, name string) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil, fmt.Errorf("sync: cannot safely edit malformed config; repair it and resume forget")
	}
	if len(doc.Content) == 0 {
		return raw, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("sync: config must be a mapping")
	}
	changed := false
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "sync" {
			continue
		}
		block := root.Content[i+1]
		if block.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("sync: cannot edit nonmapping sync configuration")
		}
		for k := 0; k < len(block.Content); k += 2 {
			v := block.Content[k+1]
			switch block.Content[k].Value {
			case "connections":
				if v.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("sync: cannot edit nonmapping connection definitions")
				}
				for n := 0; n < len(v.Content); n += 2 {
					if v.Content[n].Value == name {
						v.Content = append(v.Content[:n], v.Content[n+2:]...)
						n -= 2
						changed = true
					}
				}
			case "routes":
				if v.Kind != yaml.SequenceNode {
					return nil, fmt.Errorf("sync: cannot safely identify routes; repair config and resume forget")
				}
				keep := v.Content[:0]
				for _, rule := range v.Content {
					selected := false
					if rule.Kind == yaml.MappingNode {
						for n := 0; n < len(rule.Content); n += 2 {
							if rule.Content[n].Value == "connection" && rule.Content[n+1].Value == name {
								selected = true
							}
						}
					}
					if selected {
						changed = true
					} else {
						keep = append(keep, rule)
					}
				}
				v.Content = keep
			}
		}
	}
	if !changed {
		return raw, nil
	}
	return yaml.Marshal(&doc)
}

func optionalFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sync: cleanup refuses nonregular file %s", path)
	}
	return os.ReadFile(path)
}

func newReset(name, db, configPath string) (*resetJournal, error) {
	j := &resetJournal{Version: 1, ID: uuid.NewString(), Connection: name, Store: db, Config: configPath, Pending: map[string]string{}, PendingRequests: map[string]pendingCreation{}}
	r, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	c, present := r.Connections[name]
	j.CredentialPresent, j.CredentialHash = present, credentialDigest(c)
	j.Server, j.Account = c.Server, c.Account
	raw, err := optionalFile(configPath)
	if err != nil {
		return nil, err
	}
	j.ConfigBefore = digest(raw)
	j.Configuration = previewResetConfig(raw, name)
	if cleaned, err := cleanConfig(raw, name); err == nil {
		j.ConfigAfter = digest(cleaned)
	}
	p, err := credentialPath()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(p), "create-*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		raw, err := readPrivate(path)
		if err != nil {
			j.Issues = append(j.Issues, resetRecordIssue{path, "unreadable or unsafe creation record; ownership unknown; left untouched"})
			continue
		}
		var request pendingCreation
		if json.Unmarshal(raw, &request) != nil || request.Version != 1 || request.LocalRepo == "" || request.Payload.Key == "" {
			j.Issues = append(j.Issues, resetRecordIssue{path, "unknown or malformed creation record; ownership unknown; left untouched"})
			continue
		}
		if request.Connection == name || (name == "default" && request.Connection == "") {
			j.Pending[path] = digest(raw)
			j.PendingRequests[path] = request
		}
	}
	return j, nil
}

func archiveResetCreation(j *resetJournal, activePath string, request pendingCreation) error {
	path := filepath.Join(filepath.Dir(activePath), "retired-create-"+j.ID+"-"+digest([]byte(request.LocalRepo))+".json")
	raw, err := json.Marshal(struct {
		Version int
		ResetID string
		Request pendingCreation
	}{1, j.ID, request})
	if err != nil {
		return err
	}
	// Link publishes a fully flushed private record without replacing any
	// diagnostic already present, including one from interrupted cleanup.
	f, err := os.CreateTemp(filepath.Dir(path), ".retired-create-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		before, err := readPrivate(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, raw) {
			return fmt.Errorf("sync: different retired creation evidence exists; active request untouched")
		}
	}
	return syncDirectory(filepath.Dir(path))
}

func cleanupReset(ctx context.Context, s *store.Store, j *resetJournal, registryPath string) error {
	for path, hash := range j.Pending {
		raw, err := readPrivate(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if digest(raw) != hash {
			return fmt.Errorf("sync: pending creation replaced concurrently; replacement untouched")
		}
		var request pendingCreation
		if json.Unmarshal(raw, &request) != nil || request.LocalRepo == "" {
			return fmt.Errorf("sync: invalid selected pending creation")
		}
		if err := s.HoldExchangeOnboarding(ctx, request.LocalRepo, j.ID); err != nil {
			return err
		}
	}
	r, err := loadRegistry()
	if err != nil {
		return err
	}
	c, present := r.Connections[j.Connection]
	if present && (!j.CredentialPresent || credentialDigest(c) != j.CredentialHash) {
		return fmt.Errorf("sync: credential replaced concurrently; reset retained, replacement untouched")
	}
	if present {
		delete(r.Connections, j.Connection)
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := atomicPrivate(registryPath, raw); err != nil {
			return err
		}
	}
	raw, err := optionalFile(j.Config)
	if err != nil {
		return err
	}
	cleaned, err := cleanConfig(raw, j.Connection)
	if err != nil {
		return err
	}
	if digest(raw) != j.ConfigBefore && digest(raw) != j.ConfigAfter {
		// A repaired malformed config may be retried only when it no longer
		// contains selected routing. Never overwrite concurrent replacements.
		if string(raw) != string(cleaned) {
			return fmt.Errorf("sync: config changed concurrently; remove selected definitions/routes manually, then resume forget")
		}
	}
	if string(raw) != string(cleaned) {
		if err := atomicPrivate(j.Config, cleaned); err != nil {
			return err
		}
	}
	for path, hash := range j.Pending {
		raw, err := readPrivate(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if digest(raw) != hash {
			return fmt.Errorf("sync: pending creation replaced concurrently; replacement untouched")
		}
		var request pendingCreation
		if json.Unmarshal(raw, &request) != nil {
			return fmt.Errorf("sync: invalid selected pending creation")
		}
		if err := archiveResetCreation(j, path, request); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	// A new request published while confirmation was open is a replacement,
	// not part of the captured cleanup identity. Do not finish over it.
	current, err := newReset(j.Connection, j.Store, j.Config)
	if err != nil {
		return err
	}
	if len(current.Pending) != 0 {
		return fmt.Errorf("sync: new pending creation appeared concurrently; replacement untouched")
	}
	return syncDirectory(filepath.Dir(registryPath))
}

func confirmReset(cmd *cobra.Command, out io.Writer, yes bool, label string) (bool, error) {
	if yes {
		return true, nil
	}
	if cliflags.FromContext(cmd.Context()).JSON || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("sync: confirmation requires a terminal; inspect --dry-run then pass --yes")
	}
	if _, err := fmt.Fprintln(out, label+"\nType yes to confirm: "); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(line) == "yes", nil
}

func addResetCommands(root *cobra.Command, streams *iostreams.Streams, write func(*cobra.Command, any) error, connection, server *string) {
	connectionCmd := &cobra.Command{Use: "connection", Short: "offline connection lifecycle"}
	var dry, yes bool
	forget := &cobra.Command{Use: "forget NAME", Short: "offline retirement and resumable local cleanup; never revoke tokens or delete remote data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if !connectionName.MatchString(name) {
			return fmt.Errorf("sync: invalid connection name")
		}
		db, err := store.DBPath()
		if err != nil {
			return err
		}
		db, err = filepath.Abs(db)
		if err != nil {
			return err
		}
		dir, err := asset.OverrideDir()
		if err != nil {
			return err
		}
		configPath, err := filepath.Abs(filepath.Join(dir, "config.yaml"))
		if err != nil {
			return err
		}
		_, err = os.Stat(db)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var s *store.Store
		if err == nil || !dry {
			s, err = tiers.OpenStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
		}
		j, err := readReset(name)
		if err != nil {
			return err
		}
		if j != nil && (j.Store != db || j.Config != configPath) {
			return fmt.Errorf("sync: pending reset belongs to store %s and global config %s; select those paths to resume", j.Store, j.Config)
		}
		id := ""
		if j != nil {
			id = j.ID
		}
		var inventory []store.ExchangeStatus
		if s != nil {
			inventory, err = s.ForgetExchangeConnection(cmd.Context(), name, id, false)
			if err != nil {
				return err
			}
		}
		candidate := j
		if candidate == nil {
			candidate, err = newReset(name, db, configPath)
			if err != nil {
				return err
			}
		}
		result := map[string]any{"connection": name, "selected_store": db, "user_global_config": configPath, "other_stores": "not discovered or changed; their bindings may need separate retirement", "bindings": inventory, "dry_run": dry, "remote": "unchanged; no HTTP or revocation. In-flight requests may still commit remotely; retired creation requests may have left cloud Repos", "cleanup": "selected credentials, definition, routes and pending creations only", "saved_connection": map[string]any{"present": candidate.CredentialPresent, "server": candidate.Server, "account": candidate.Account}, "pending_creations": candidate.PendingRequests, "configuration": candidate.Configuration, "creation_record_issues": candidate.Issues}
		if dry {
			return write(cmd, result)
		}
		ok, err := confirmReset(cmd, streams.Err, yes, fmt.Sprintf("Forget connection %s in store %s and user-global config %s? Remote data/tokens remain unchanged. Other custom stores are not scanned.", name, db, configPath))
		if err != nil {
			return err
		}
		if !ok {
			result["cancelled"] = true
			return write(cmd, result)
		}
		err = withRegistryLock(func(registryPath string) error {
			current, err := readReset(name)
			if err != nil {
				return err
			}
			if current != nil {
				j = current
			}
			if j == nil {
				j = candidate
				raw, err := json.Marshal(j)
				if err != nil {
					return err
				}
				path, err := resetPath(name)
				if err != nil {
					return err
				}
				if err := atomicPrivate(path, raw); err != nil {
					return err
				}
			}
			if j.Store != db || j.Config != configPath {
				return fmt.Errorf("sync: reset scope changed concurrently")
			}
			inventory, err = s.ForgetExchangeConnection(cmd.Context(), name, j.ID, true)
			if err != nil {
				return err
			}
			if err := cleanupReset(cmd.Context(), s, j, registryPath); err != nil {
				return err
			}
			if err := s.FinishExchangeConnectionReset(cmd.Context(), name, j.ID); err != nil {
				return err
			}
			path, err := resetPath(name)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			return syncDirectory(filepath.Dir(path))
		})
		if err != nil {
			return err
		}
		result["bindings"], result["forgotten"] = inventory, true
		return write(cmd, result)
	}}
	forget.Flags().BoolVar(&dry, "dry-run", false, "offline selected-store preview")
	forget.Flags().BoolVar(&yes, "yes", false, "confirm local cleanup without prompting")
	annotateReset(forget)
	surface.Annotate(connectionCmd, surface.Plumbing)
	connectionCmd.AddCommand(forget)
	root.AddCommand(connectionCmd)
	addDeleteCommand(root, streams, write, connection, server)
}

func addDeleteCommand(root *cobra.Command, streams *iostreams.Streams, write func(*cobra.Command, any) error, connection, server *string) {
	repoCmd := &cobra.Command{Use: "repo", Short: "explicit remote Repo lifecycle"}
	var org, remote string
	var dry, yes bool
	deleteCmd := &cobra.Command{Use: "delete", Short: "owner-authorized irreversible remote deletion; no local forgetting", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if *connection == "" || !destinationID(org) || !destinationID(remote) {
			return fmt.Errorf("sync: --connection, --org and --server-repo exact IDs are required")
		}
		if err := checkResetPending(*connection); err != nil {
			return err
		}
		c, err := loadConnection(*connection, *server)
		if err != nil {
			return err
		}
		before := c
		if err := c.identify(cmd.Context()); err != nil {
			return err
		}
		path := "/api/organizations/" + org + "/repos/" + remote
		api := newAPI(c.Server, c.Token)
		var preview struct {
			Repo    remoteIdentity `json:"repo"`
			Changes int            `json:"changes"`
			Origins int            `json:"origins"`
		}
		if err := api.request(cmd.Context(), "GET", path+"/deletion", nil, &preview); err != nil {
			return fmt.Errorf("sync: deletion preview unavailable (404 means absent OR inaccessible): %w", err)
		}
		if preview.Repo.ID != remote {
			return fmt.Errorf("sync: deletion preview identity mismatch")
		}
		const warning = "Cloud-only work may be lost; deletion does not download it first. Back up valuable work before deleting."
		result := map[string]any{"connection": *connection, "server": c.Server, "account": c.Account, "endpoint": c.Server + path, "dry_run": dry, "preview": preview, "warning": warning, "local": "unchanged; use connection forget separately"}
		if dry {
			return write(cmd, result)
		}
		ok, err := confirmReset(cmd, streams.Err, yes, fmt.Sprintf("Permanently delete %q (%s) at %s as account %s? %d changes, %d origins. Collaborators lose this server Repo; local work is not erased. %s", preview.Repo.Name, remote, c.Server+path, c.Account, preview.Changes, preview.Origins, warning))
		if err != nil {
			return err
		}
		if !ok {
			result["cancelled"] = true
			return write(cmd, result)
		}
		if err := checkResetPending(*connection); err != nil {
			return err
		}
		current, err := loadConnection(*connection, c.Server)
		if err != nil {
			return err
		}
		if current != before {
			return fmt.Errorf("sync: selected credential changed; repeat preview")
		}
		if err := current.identify(cmd.Context()); err != nil {
			return err
		}
		if current.Account != c.Account {
			return fmt.Errorf("sync: authenticated account changed")
		}
		if err := api.request(cmd.Context(), "DELETE", path, nil, nil); err != nil {
			var status *httpStatusError
			if errors.As(err, &status) && status.status < 500 {
				return fmt.Errorf("sync: deletion not confirmed (404 means absent OR inaccessible): %w", err)
			}
			return wiperr.New("sync.remote_outcome_unknown", "remote_outcome_unknown: retry only exact endpoint "+c.Server+path+"; do not discover a replacement by slug")
		}
		result["deleted"] = true
		return write(cmd, result)
	}}
	deleteCmd.Flags().StringVar(&org, "org", "", "exact organization ID")
	deleteCmd.Flags().StringVar(&remote, "server-repo", "", "exact server Repo ID")
	deleteCmd.Flags().BoolVar(&dry, "dry-run", false, "online owner-authorized deletion preview only")
	deleteCmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion without prompting")
	annotateReset(deleteCmd)
	surface.Annotate(repoCmd, surface.Plumbing)
	repoCmd.AddCommand(deleteCmd)
	root.AddCommand(repoCmd)
}

func annotateReset(cmd *cobra.Command) {
	surface.Annotate(cmd, surface.Plumbing)
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
}
