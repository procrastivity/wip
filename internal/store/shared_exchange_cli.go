package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ExchangeStatus is an offline inventory, not a claim of convergence. For a
// paired Repo the recorded boundary is retained pairing evidence only: every
// supported unsynced original group is eligible regardless of age.
// EarlierGroups counts command groups at or before that boundary as inventory;
// unsupported local work remains fully usable.
type ExchangeStatus struct {
	Binding                              ExchangeBinding
	Generation                           string
	Paired, Active                       bool
	Boundary                             string
	EarlierGroups                        int
	PendingUpload, Retained, Unsupported []string
	Cursor, Downloaded, Evaluated        int64
	Integrated, Waiting, Conflicts       int
	Decisions                            []ExperimentDecision
	Collisions                           []ExperimentDecision
	Acknowledged                         map[string]ExperimentAcknowledgement
	Dependencies                         []string
}

func (s *Store) exchangeCLIExists(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='exchange_cli'`).Scan(&n)
	return n != 0, err
}

// ExchangeBindingInfo is retained destination metadata, not transfer state or
// credential validity. Legacy pairings have empty connection and account fields.
type ExchangeBindingInfo struct {
	LocalRepo  string `json:"local_repo"`
	Active     bool   `json:"active"`
	Connection string `json:"connection"`
	Account    string `json:"account"`
	Endpoint   string `json:"endpoint"`
}

// ExchangeBindings lists current CLI pairings, including paused ones, in local
// Repo ID order. It never creates opt-in tables or reads work or credentials.
func (s *Store) ExchangeBindings(ctx context.Context) ([]ExchangeBindingInfo, error) {
	out := make([]ExchangeBindingInfo, 0)
	exists, err := s.exchangeCLIExists(ctx)
	if err != nil || !exists {
		return out, err
	}
	accounts, err := exchangeTableExists(ctx, s.db, "exchange_accounts")
	if err != nil {
		return nil, err
	}
	query := `SELECT c.repo,c.active,b.endpoint,c.version,1,'','' FROM exchange_cli c JOIN exchange_bindings b ON b.repo=c.repo`
	if accounts {
		query = `SELECT c.repo,c.active,b.endpoint,c.version,COALESCE(a.version,1),COALESCE(a.connection,''),COALESCE(a.account,'') FROM exchange_cli c JOIN exchange_bindings b ON b.repo=c.repo LEFT JOIN exchange_accounts a ON a.repo=c.repo`
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY c.repo`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var b ExchangeBindingInfo
		var version, accountVersion int
		if err := rows.Scan(&b.LocalRepo, &b.Active, &b.Endpoint, &version, &accountVersion, &b.Connection, &b.Account); err != nil {
			return nil, err
		}
		if version != 1 {
			return nil, fmt.Errorf("exchange: unsupported local pairing version %d", version)
		}
		if accountVersion != 1 {
			return nil, fmt.Errorf("exchange: unsupported connection metadata version %d", accountVersion)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PairExchange records the pairing boundary once; it is retained evidence,
// not an export exclusion. Repeat pairing resumes the same destination and
// cursor; no installation or state is created by preview.
func (s *Store) PairExchange(ctx context.Context, repo, endpoint string) (ExchangeStatus, error) {
	preview, err := s.ExchangeStatus(ctx, repo)
	if err != nil {
		return preview, err
	}
	if _, err = s.bindExchange(ctx, repo, endpoint, &preview.Boundary); err != nil {
		return preview, err
	}
	return s.ExchangeStatus(ctx, repo)
}

// PairExchangeConnection pins account selection atomically with initial pairing
// or attaches it to the same legacy endpoint without resetting transfer history.
func (s *Store) PairExchangeConnection(ctx context.Context, repo, endpoint, connection, account string) (ExchangeStatus, error) {
	preview, err := s.ExchangeStatus(ctx, repo)
	if err != nil {
		return preview, err
	}
	if _, err := s.bindExchange(ctx, repo, endpoint, &preview.Boundary, connection, account); err != nil {
		return preview, err
	}
	return s.ExchangeStatus(ctx, repo)
}

// PairExchangeConnectionAt rejects a pairing whose pre-HTTP view was reset.
func (s *Store) PairExchangeConnectionAt(ctx context.Context, repo, endpoint, connection, account, expectedGeneration, expectedConnectionGeneration string) (ExchangeStatus, error) {
	preview, err := s.ExchangeStatus(ctx, repo)
	if err != nil {
		return preview, err
	}
	if _, err := s.bindExchange(ctx, repo, endpoint, &preview.Boundary, connection, account, expectedGeneration, expectedConnectionGeneration); err != nil {
		return preview, err
	}
	return s.ExchangeStatus(ctx, repo)
}

// UnpairExchange disables future passes without deleting evidence or checkpoints.
func (s *Store) UnpairExchange(ctx context.Context, repo string) error {
	status, err := s.ExchangeStatus(ctx, repo)
	if err != nil || !status.Paired {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE exchange_cli SET active=0 WHERE repo=?`, repo)
	return err
}

// ExchangeStatus inventories local originals and retained outcomes without
// creating opt-in tables, capturing envelopes, or contacting the server.
func (s *Store) ExchangeStatus(ctx context.Context, repo string) (ExchangeStatus, error) {
	out := ExchangeStatus{Binding: ExchangeBinding{LocalRepo: repo}}
	if _, err := s.Repo(ctx, repo); err != nil {
		return out, err
	}
	var err error
	out.Generation, err = exchangeGeneration(ctx, s.db, repo)
	if err != nil {
		return out, err
	}
	exists, err := s.exchangeCLIExists(ctx)
	if err != nil {
		return out, err
	}
	if exists {
		var version int
		err = s.db.QueryRowContext(ctx, `SELECT c.version,c.boundary,c.active,b.endpoint,i.id,b.cursor FROM exchange_cli c JOIN exchange_bindings b ON b.repo=c.repo CROSS JOIN exchange_installation i WHERE c.repo=?`, repo).
			Scan(&version, &out.Boundary, &out.Active, &out.Binding.Endpoint, &out.Binding.InstallationID, &out.Cursor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil {
			if version != 1 {
				return out, fmt.Errorf("exchange: unsupported local pairing version %d", version)
			}
			out.Paired = true
		}
	}
	if out.Paired {
		out.Binding.AssociationID = out.Generation
		if exists, err := exchangeTableExists(ctx, s.db, "exchange_associations"); err != nil {
			return out, err
		} else if exists {
			err := s.db.QueryRowContext(ctx, `SELECT installation FROM exchange_associations WHERE repo=?`, repo).Scan(&out.Binding.InstallationID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
		}
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='exchange_accounts'`).Scan(&exists); err != nil {
			return out, err
		}
		if exists != 0 {
			var version int
			err := s.db.QueryRowContext(ctx, `SELECT version,connection,account FROM exchange_accounts WHERE repo=?`, repo).Scan(&version, &out.Binding.Connection, &out.Binding.Account)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			if err == nil && version != 1 {
				return out, fmt.Errorf("exchange: unsupported connection metadata version %d", version)
			}
		}
	}
	// Scan entire command groups, not individual eligible actions. Mixed or
	// unsupported groups must never be silently truncated into new originals.
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT correlation FROM events WHERE repo=? ORDER BY correlation`, repo)
	if err != nil {
		return out, err
	}
	var groups []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return out, err
		}
		groups = append(groups, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	if !out.Paired && len(groups) > 0 {
		out.Boundary = groups[len(groups)-1]
	}
	// A paired Repo evaluates every command group regardless of the recorded
	// boundary, which remains pairing evidence rather than an export exclusion.
	// An unpaired preview keeps the latest-group boundary, so every group is
	// inventory and nothing counts as outgoing work.
	type groupScan struct {
		events    []Event
		effect    bool
		supported bool
	}
	scans := make([]groupScan, len(groups))
	portable := map[string]bool{}
	if out.Paired {
		for i, group := range groups {
			events, err := s.EventsOfChain(ctx, group)
			if err != nil {
				return out, err
			}
			scans[i].events = events
			var effect int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e JOIN experiment_effects x ON x.id=e.id WHERE e.correlation=?`, group).Scan(&effect); err != nil {
				return out, err
			}
			scans[i].effect = effect != 0
			if scans[i].effect {
				continue
			}
			supported := true
			for _, ev := range events {
				if ev.Repo != repo || ev.Actor == integrationEffectActor {
					supported = false
				}
				switch ev.Type {
				case TypeMatterCreated, TypeMatterStarted, TypeStageStarted, TypeStepStarted:
				case TypeContentCreated:
					var p ContentWritten
					if json.Unmarshal(ev.Payload, &p) != nil || p.Kind != KindBrief || p.Bytes == nil || p.BlobRef != "" {
						supported = false
					}
				default:
					supported = false
				}
			}
			scans[i].supported = supported
			if supported {
				portable[group] = true
			}
		}
	}
	for i, group := range groups {
		if group <= out.Boundary {
			out.EarlierGroups++
			if !out.Paired {
				continue
			}
		}
		scan := scans[i]
		if scan.effect {
			continue
		}
		for _, ev := range scan.events {
			var prior, priorGroup string
			err := s.db.QueryRowContext(ctx, `SELECT id,correlation FROM events WHERE subject=? AND id<? ORDER BY id DESC LIMIT 1`, ev.Subject, ev.ID).Scan(&prior, &priorGroup)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			if prior == "" || priorGroup == group || portable[priorGroup] {
				continue
			}
			var effect int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment_effects WHERE id=?`, prior).Scan(&effect); err != nil {
				return out, err
			}
			// An integration effect is never a portable original, but its
			// retained source evidence resolves the dependency instead.
			if effect != 0 {
				continue
			}
			out.Dependencies = append(out.Dependencies, group+": depends on subject evidence "+prior+" in group "+priorGroup+" (not portable)")
		}
		if !scan.supported {
			out.Unsupported = append(out.Unsupported, group)
			continue
		}
		var ack int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exchange_upload_ack WHERE repo=? AND group_id=?`, repo, group).Scan(&ack); err != nil {
			return out, err
		}
		if ack == 0 {
			out.PendingUpload = append(out.PendingUpload, group)
		} else {
			out.Retained = append(out.Retained, group)
		}
	}
	if out.Paired {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(applied),0) FROM exchange_inbox WHERE repo=?`, repo).Scan(&out.Downloaded, &out.Evaluated); err != nil {
			return out, err
		}
		scope, err := exchangeScope(ctx, s.db, repo)
		if err != nil {
			return out, err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT decision,0 FROM experiment_sources WHERE target_repo=? AND (?=? OR substr(id,1,length(?)+1)=?||':') UNION ALL SELECT decision,1 FROM experiment_collisions WHERE target_repo=?`, repo, scope, repo, scope, scope, scope)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var raw string
			var collision bool
			if err := rows.Scan(&raw, &collision); err != nil {
				_ = rows.Close()
				return out, err
			}
			var d ExperimentDecision
			if err := json.Unmarshal([]byte(raw), &d); err != nil {
				_ = rows.Close()
				return out, err
			}
			if collision {
				out.Collisions = append(out.Collisions, d)
			} else {
				out.Decisions = append(out.Decisions, d)
			}
			switch d.Disposition {
			case "integrated":
				out.Integrated++
			case "pending":
				out.Waiting++
			case "conflict":
				out.Conflicts++
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return out, err
		}
		out.Acknowledged = map[string]ExperimentAcknowledgement{}
		for _, d := range out.Decisions {
			var raw string
			id := d.Source.Actions[0].Event.ID
			key, err := experimentSourceKey(ctx, s.db, repo, id)
			if err != nil {
				return out, err
			}
			err = s.db.QueryRowContext(ctx, `SELECT acknowledgement FROM experiment_acknowledgements WHERE source_group=?`, key).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return out, err
			}
			var a ExperimentAcknowledgement
			if err := json.Unmarshal([]byte(raw), &a); err != nil {
				return out, err
			}
			out.Acknowledged[id] = a
		}
	}
	return out, nil
}

// RunExchange captures every eligible complete original before first HTTP.
// An acknowledgement lost after server commit leaves the exact cached envelope
// pending; replay uses the same key and bytes. Unpair never deletes this state.
func (s *Store) RunExchange(ctx context.Context, c ExchangeClient, repo string) (ExchangeStatus, error) {
	status, err := s.ExchangeStatus(ctx, repo)
	if err != nil {
		return status, err
	}
	if !status.Paired || !status.Active {
		return status, fmt.Errorf("exchange: Repo is not actively paired")
	}
	for _, id := range status.PendingUpload {
		if _, err := s.exchangeExport(ctx, status.Binding, id); err != nil {
			return status, err
		}
	}
	// Register/download even when nothing is outgoing. Each successful upload
	// gets its own durable local acknowledgement, never inferred from a cursor.
	if err := c.request(ctx, "POST", status.Binding.Endpoint+"/origins", map[string]string{"installation_id": status.Binding.InstallationID}, nil); err != nil {
		return status, err
	}
	for _, id := range status.PendingUpload {
		change, err := s.exchangeExport(ctx, status.Binding, id)
		if err != nil {
			return status, err
		}
		if err := c.request(ctx, "POST", status.Binding.Endpoint+"/changes", map[string]any{"changes": []ExchangeChange{change}}, nil); err != nil {
			return status, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return status, err
		}
		if err = checkExchangeBinding(ctx, tx, status.Binding); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_upload_ack VALUES(?,?)`, repo, id)
		}
		if err == nil {
			err = tx.Commit()
		}
		_ = tx.Rollback()
		if err != nil {
			return status, err
		}
	}
	if err := s.ExchangePass(ctx, c, status.Binding, nil); err != nil {
		return status, err
	}
	return s.ExchangeStatus(ctx, repo)
}
