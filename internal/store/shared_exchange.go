package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ExchangeBinding is explicit local configuration, never inferred from Git or
// a remote URL. Endpoint includes /api/organizations/{org}/repos/{repo}.
type ExchangeBinding struct {
	LocalRepo, Endpoint, InstallationID string
	Connection, Account                 string
}

// ExchangeChange v1 retains exact source JSON as a string. Authenticated uploader
// and receipt metadata are server observations, not original actor/time claims.
type ExchangeChange struct {
	Version        int    `json:"version"`
	InstallationID string `json:"installation_id"`
	GroupID        string `json:"group_id"`
	SourceRepoID   string `json:"source_repo_id"`
	Source         string `json:"source"`
	Sequence       int64  `json:"sequence,omitempty"`
	UploaderID     int64  `json:"uploader_id,omitempty"`
	ReceivedAt     string `json:"received_at,omitempty"`
}

// BindExchange initializes only opt-in disposable lab tables. Rebinding is an
// error: metadata changes must never switch/merge native Repo identities.
func (s *Store) BindExchange(ctx context.Context, repo, endpoint string) (ExchangeBinding, error) {
	return s.bindExchange(ctx, repo, endpoint, nil)
}

func (s *Store) bindExchange(ctx context.Context, repo, endpoint string, boundary *string, identity ...string) (ExchangeBinding, error) {
	if _, err := s.Repo(ctx, repo); err != nil {
		return ExchangeBinding{}, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ExchangeBinding{}, fmt.Errorf("exchange: invalid explicit endpoint")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if err := s.EnableSharedStoreExperiment(ctx); err != nil {
		return ExchangeBinding{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExchangeBinding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_installation (singleton INTEGER PRIMARY KEY CHECK(singleton=1), id TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS exchange_bindings (repo TEXT PRIMARY KEY REFERENCES repos(id), endpoint TEXT NOT NULL, cursor INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS exchange_exports (repo TEXT NOT NULL REFERENCES exchange_bindings(repo), group_id TEXT NOT NULL, envelope TEXT NOT NULL, PRIMARY KEY(repo,group_id));
	CREATE TABLE IF NOT EXISTS exchange_inbox (repo TEXT NOT NULL REFERENCES exchange_bindings(repo), sequence INTEGER NOT NULL, envelope TEXT NOT NULL, applied INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(repo,sequence));`)
	if err != nil {
		return ExchangeBinding{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ExchangeBinding{}, err
	}
	random[6] = (random[6] & 15) | 64
	random[8] = (random[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", random[0:4], random[4:6], random[6:8], random[8:10], random[10:16])
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_installation VALUES(1,?)`, id); err != nil {
		return ExchangeBinding{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_bindings(repo,endpoint) VALUES(?,?)`, repo, endpoint); err != nil {
		return ExchangeBinding{}, err
	}
	b := ExchangeBinding{LocalRepo: repo}
	if err := tx.QueryRowContext(ctx, `SELECT b.endpoint,i.id FROM exchange_bindings b CROSS JOIN exchange_installation i WHERE b.repo=?`, repo).Scan(&b.Endpoint, &b.InstallationID); err != nil {
		return b, err
	}
	if b.Endpoint != endpoint {
		return b, fmt.Errorf("exchange: binding is immutable")
	}
	// Additive opt-in metadata: legacy rows, source keys and all transfer state
	// remain in place. Reading an older store does not initialize this table.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_accounts (
	 repo TEXT PRIMARY KEY REFERENCES exchange_bindings(repo), version INTEGER NOT NULL,
	 connection TEXT NOT NULL, account TEXT NOT NULL)`); err != nil {
		return b, err
	}
	if len(identity) == 2 {
		if identity[0] == "" || identity[1] == "" {
			return b, fmt.Errorf("exchange: connection and verified account required")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_accounts VALUES(?,1,?,?)`, repo, identity[0], identity[1]); err != nil {
			return b, err
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT version,connection,account FROM exchange_accounts WHERE repo=?`, repo).Scan(&version, &b.Connection, &b.Account); err != nil {
			return b, err
		}
		if version != 1 || b.Connection != identity[0] || b.Account != identity[1] {
			return b, fmt.Errorf("exchange: connection/account is immutable")
		}
	}
	if boundary != nil {
		_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_cli (
		 repo TEXT PRIMARY KEY REFERENCES exchange_bindings(repo), version INTEGER NOT NULL,
		 boundary TEXT NOT NULL, active INTEGER NOT NULL);
		 CREATE TABLE IF NOT EXISTS exchange_upload_ack (
		 repo TEXT NOT NULL REFERENCES exchange_bindings(repo), group_id TEXT NOT NULL,
		 PRIMARY KEY(repo,group_id));`)
		if err != nil {
			return b, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_cli VALUES(?,1,?,1)`, repo, *boundary); err != nil {
			return b, err
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT version FROM exchange_cli WHERE repo=?`, repo).Scan(&version); err != nil {
			return b, err
		}
		if version != 1 {
			return b, fmt.Errorf("exchange: unsupported pairing version %d", version)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE exchange_cli SET active=1 WHERE repo=?`, repo); err != nil {
			return b, err
		}
	}
	return b, tx.Commit()
}

// ExchangeClient is deliberately explicit, with no startup hooks or daemon.
// Tokens remain in caller memory, not local binding rows or exported source.
type ExchangeClient struct {
	HTTP  *http.Client
	Token string
}

func (c ExchangeClient) request(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	// Never forward a bearer token to redirects outside the explicit binding.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("exchange: HTTP %d", resp.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

// ExchangePass uploads requested committed originals, downloads committed pages
// to a durable inbox+cursor, then applies/rechecks locally. No HTTP call holds a
// local writer transaction. A crash between receipt and application is safe.
func (s *Store) ExchangePass(ctx context.Context, c ExchangeClient, b ExchangeBinding, groups []string) error {
	var installation string
	if err := s.db.QueryRowContext(ctx, `SELECT i.id FROM exchange_bindings b CROSS JOIN exchange_installation i WHERE b.repo=? AND b.endpoint=?`, b.LocalRepo, b.Endpoint).Scan(&installation); err != nil {
		return err
	}
	if installation != b.InstallationID {
		return fmt.Errorf("exchange: installation differs from durable binding")
	}
	if err := c.request(ctx, "POST", b.Endpoint+"/origins", map[string]string{"installation_id": b.InstallationID}, nil); err != nil {
		return err
	}
	for _, group := range groups {
		change, err := s.exchangeExport(ctx, b, group)
		if err != nil {
			return err
		}
		if err := c.request(ctx, "POST", b.Endpoint+"/changes", map[string]any{"changes": []ExchangeChange{change}}, nil); err != nil {
			return err
		}
	}
	for {
		var after int64
		if err := s.db.QueryRowContext(ctx, `SELECT cursor FROM exchange_bindings WHERE repo=? AND endpoint=?`, b.LocalRepo, b.Endpoint).Scan(&after); err != nil {
			return err
		}
		var page struct {
			Changes []ExchangeChange `json:"changes"`
			Cursor  int64            `json:"cursor"`
		}
		if err := c.request(ctx, "GET", fmt.Sprintf("%s/changes?after=%d", b.Endpoint, after), nil, &page); err != nil {
			return err
		}
		if len(page.Changes) == 0 {
			if page.Cursor != after {
				return fmt.Errorf("exchange: empty page changed cursor")
			}
			break
		}
		if err := s.receiveExchangePage(ctx, b, after, page.Cursor, page.Changes); err != nil {
			return err
		}
	}
	return s.ApplyExchangeInbox(ctx, b)
}

func (s *Store) exchangeExport(ctx context.Context, b ExchangeBinding, group string) (ExchangeChange, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, b.LocalRepo, group).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		var source ExperimentSource
		source, err = s.ExperimentExport(ctx, b.InstallationID, group)
		if err != nil {
			return ExchangeChange{}, err
		}
		for _, a := range source.Actions {
			if a.Event.Repo != b.LocalRepo {
				return ExchangeChange{}, fmt.Errorf("exchange: source outside binding")
			}
		}
		var encoded []byte
		encoded, err = json.Marshal(source)
		if err != nil {
			return ExchangeChange{}, err
		}
		change := ExchangeChange{Version: 1, InstallationID: b.InstallationID, GroupID: group, SourceRepoID: b.LocalRepo, Source: string(encoded)}
		encoded, err = json.Marshal(change)
		if err != nil {
			return ExchangeChange{}, err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_exports VALUES(?,?,?)`, b.LocalRepo, group, string(encoded)); err != nil {
			return ExchangeChange{}, err
		}
		// A concurrent pass may have captured earlier correspondence evidence.
		err = s.db.QueryRowContext(ctx, `SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, b.LocalRepo, group).Scan(&raw)
	}
	if err != nil {
		return ExchangeChange{}, err
	}
	var change ExchangeChange
	err = json.Unmarshal([]byte(raw), &change)
	return change, err
}

func (s *Store) receiveExchangePage(ctx context.Context, b ExchangeBinding, after, cursor int64, changes []ExchangeChange) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, change := range changes {
		if change.Version != 1 || change.Sequence != after+1 {
			return fmt.Errorf("exchange: unsupported version or noncontiguous page")
		}
		var source ExperimentSource
		if err := json.Unmarshal([]byte(change.Source), &source); err != nil {
			return err
		}
		if source.Host != change.InstallationID || len(source.Actions) == 0 || source.Actions[0].Event.ID != change.GroupID {
			return fmt.Errorf("exchange: invalid envelope")
		}
		for _, a := range source.Actions {
			if a.Event.Repo != change.SourceRepoID {
				return fmt.Errorf("exchange: source Repo mismatch")
			}
		}
		raw, err := json.Marshal(change)
		if err != nil {
			return err
		}
		var old string
		err = tx.QueryRowContext(ctx, `SELECT envelope FROM exchange_inbox WHERE repo=? AND sequence=?`, b.LocalRepo, change.Sequence).Scan(&old)
		if err == nil && old != string(raw) {
			return fmt.Errorf("exchange: committed feed changed")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_inbox(repo,sequence,envelope) VALUES(?,?,?)`, b.LocalRepo, change.Sequence, string(raw)); err != nil {
			return err
		}
		after = change.Sequence
	}
	if cursor != after {
		return fmt.Errorf("exchange: cursor does not match durable page")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE exchange_bindings SET cursor=MAX(cursor,?) WHERE repo=? AND endpoint=?`, cursor, b.LocalRepo, b.Endpoint); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyExchangeInbox needs no server. Restarted processes finish receipt even
// while offline. Pending groups are reconsidered once after this incoming batch.
func (s *Store) ApplyExchangeInbox(ctx context.Context, b ExchangeBinding) error {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,envelope FROM exchange_inbox WHERE repo=? AND applied=0 ORDER BY sequence`, b.LocalRepo)
	if err != nil {
		return err
	}
	type incoming struct {
		seq    int64
		change ExchangeChange
	}
	var incomingRows []incoming
	for rows.Next() {
		var seq int64
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var change ExchangeChange
		if err := json.Unmarshal([]byte(raw), &change); err != nil {
			_ = rows.Close()
			return err
		}
		incomingRows = append(incomingRows, incoming{seq, change})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, row := range incomingRows {
		var source ExperimentSource
		if err := json.Unmarshal([]byte(row.change.Source), &source); err != nil {
			return err
		}
		if _, err := s.ExperimentImportBound(ctx, source, b.LocalRepo); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE exchange_inbox SET applied=1 WHERE repo=? AND sequence=?`, b.LocalRepo, row.seq); err != nil {
			return err
		}
	}
	// Reconsider only canonical evidence mapped to this pairing. An explicit
	// pass must not apply pending work in another (possibly unpaired) Repo.
	decisions, err := experimentActivities(ctx, s.db, false, b.LocalRepo)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.Disposition == "pending" {
			if _, err := s.ExperimentRecheckBound(ctx, b.LocalRepo, d.Source.Actions[0].Event.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
