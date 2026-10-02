package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type gateExemptionRepairTerminal struct {
	DomainID, CommandID, RequestHash                           string
	ResultCode, RefusalCode, RefusalMessage                    string
	ObservedPosition                                           uint64
	ObservedEventID, ObservedPrefixDigest, EventID, OccurredAt string
}

type gateRepairSnapshot struct {
	nodes        map[string]step13Node
	lifecycle    map[string]string
	declarations map[string]step13Declaration
	states       map[string]step13GateState
}

type gateRepairRefusal struct {
	code, message string
}

func readGateExemptionRepairTerminal(ctx context.Context, queryer gateRepairQueryer, domain, command string) (gateExemptionRepairTerminal, error) {
	var terminal gateExemptionRepairTerminal
	var table int
	err := queryer.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='gate_exemption_repair_terminals'`).Scan(&table)
	if err != nil {
		return terminal, err
	}
	if table == 0 {
		return terminal, sql.ErrNoRows
	}
	var observed int64
	var refusalCode, refusalMessage, eventID sql.NullString
	err = queryer.QueryRowContext(ctx, `SELECT domain_id,command_id,request_hash,result_code,refusal_code,refusal_message,
		observed_position,observed_event_id,observed_prefix_digest,event_id,occurred_at
		FROM gate_exemption_repair_terminals WHERE domain_id=? AND command_id=?`, domain, command).Scan(
		&terminal.DomainID, &terminal.CommandID, &terminal.RequestHash, &terminal.ResultCode, &refusalCode, &refusalMessage,
		&observed, &terminal.ObservedEventID, &terminal.ObservedPrefixDigest, &eventID, &terminal.OccurredAt)
	if err != nil {
		return terminal, err
	}
	if observed <= 0 {
		return terminal, ErrInvalidStore
	}
	terminal.ObservedPosition = uint64(observed)
	terminal.RefusalCode, terminal.RefusalMessage, terminal.EventID = refusalCode.String, refusalMessage.String, eventID.String
	return terminal, nil
}

func (s *Store) completeGateExemptionRepair(ctx context.Context, canonical []byte, hash string, suppliedProof, eventID []byte, occurred time.Time) (gateExemptionRepairTerminal, error) {
	var empty gateExemptionRepairTerminal
	command, err := decodeGateExemptionRepairCommand(canonical, hash)
	if err != nil || len(suppliedProof) > 1<<20 || occurred.IsZero() {
		return empty, ErrInvalidProof
	}
	if len(eventID) != 0 && !ulid.MatchString(string(eventID)) {
		return empty, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return empty, errors.New("authoritystore: closed")
	}
	if err = checkStep13State(s.db); err != nil {
		return empty, fmt.Errorf("%w: signed gate history invalid: %v", ErrInvalidStore, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := readGateExemptionRepairAdmission(ctx, tx, command.DomainID, command.ID)
	if err != nil || stored.RequestHash != hash || !bytes.Equal(stored.Command, canonical) ||
		len(suppliedProof) != 0 && !bytes.Equal(stored.Proof, suppliedProof) {
		return empty, ErrConflict
	}
	if err = validateStoredGateExemptionRepairAdmissionCore(ctx, tx, stored); err != nil {
		return empty, err
	}
	if terminal, terminalErr := readGateExemptionRepairTerminal(ctx, tx, stored.DomainID, stored.CommandID); terminalErr == nil {
		if err = validateGateExemptionRepairTerminal(ctx, tx, stored, terminal); err != nil {
			return empty, err
		}
		return terminal, nil
	} else if !errors.Is(terminalErr, sql.ErrNoRows) {
		return empty, terminalErr
	}
	if err = validatePendingGateExemptionRepairAdmission(ctx, tx, stored); err != nil {
		return empty, err
	}
	domain, err := domainOwner(ctx, tx, stored.DomainID)
	if err != nil {
		return empty, err
	}
	refusal := (*gateRepairRefusal)(nil)
	if domain.ActiveEpoch != stored.AuthorityEpoch {
		refusal = &gateRepairRefusal{"refusal.claim-fenced", "the Matter authority epoch is no longer active"}
	} else if err = validateGateClaimTx(ctx, tx, command.claimCommand()); err != nil {
		if errors.Is(err, ErrFenced) {
			refusal = &gateRepairRefusal{"refusal.claim-fenced", "the Matter claim or current journal generation is no longer active"}
		} else {
			return empty, err
		}
	}
	anchor, err := currentAnchor(ctx, tx, stored.DomainID)
	if err != nil {
		return empty, err
	}
	var appendEvent bool
	if refusal == nil {
		current, snapshotErr := gateRepairSnapshotAt(ctx, tx, stored.DomainID, anchor.EventCount)
		if snapshotErr != nil {
			return empty, snapshotErr
		}
		guard := assessGateExemptionRepair(current, command, anchor.EventCount, true)
		if guard != nil {
			refusal = guard
		} else if current.states[gateRepairStateKey(stored.DomainID, command.NodeID, command.Gate)].state == "exempt" {
			appendEvent = false
		} else {
			historical, snapshotErr := gateRepairSnapshotAt(ctx, tx, stored.DomainID, command.Boundary.EventCount)
			if snapshotErr != nil {
				return empty, snapshotErr
			}
			if guard = assessGateExemptionRepair(historical, command, command.Boundary.EventCount, false); guard != nil {
				refusal = guard
			} else {
				appendEvent = true
			}
		}
	}
	resultCode, refusalCode, refusalMessage := "result.succeeded", "", ""
	terminalEventID := ""
	if refusal != nil {
		resultCode, refusalCode, refusalMessage = "result.refused", refusal.code, refusal.message
	} else if appendEvent {
		if len(eventID) == 0 {
			return empty, ErrInvalidProof
		}
		terminalEventID = string(eventID)
	}
	occurredAt := occurred.UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO gate_exemption_repair_terminals(
		domain_id,command_id,request_hash,result_code,refusal_code,refusal_message,observed_position,observed_event_id,
		observed_prefix_digest,event_id,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		stored.DomainID, stored.CommandID, stored.RequestHash, resultCode, nullableString(refusalCode), nullableString(refusalMessage),
		anchor.EventCount, anchor.EventID, anchor.Digest, nullableString(terminalEventID), occurredAt); err != nil {
		return empty, writeError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO submissions(
		domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
	) VALUES(?,?,?,?,?,?,?,?,1,'terminal')`, stored.DomainID, stored.CommandID, stored.RequestHash, stored.Command,
		stored.AuthorityEpoch, stored.EnvironmentID, stored.EnvironmentSequence, gateExemptionRepairOperationName); err != nil {
		return empty, writeError(err)
	}
	if appendEvent {
		identity := eventIdentity{stored.DomainID, stored.CommandID, stored.RequestHash, stored.EnvironmentID,
			stored.EnvironmentSequence, command.ActedAt, stored.RepoID}
		position, appendErr := appendCommandEvent(ctx, tx, identity, occurred, terminalEventID,
			"gate.exemption-repaired", stored.NodeID, map[string]any{"gate": stored.Gate})
		if appendErr != nil || position != anchor.EventCount+1 {
			if appendErr != nil {
				return empty, appendErr
			}
			return empty, ErrInvalidStore
		}
		if err = step13ProjectionTx(ctx, tx); err != nil {
			return empty, err
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE environments SET sequence_head=? WHERE domain_id=? AND environment_id=? AND sequence_head=?`,
		stored.EnvironmentSequence, stored.DomainID, stored.EnvironmentID, stored.EnvironmentSequence-1)
	if err != nil {
		return empty, writeError(err)
	}
	if changed, rowsErr := updated.RowsAffected(); rowsErr != nil || changed != 1 {
		return empty, ErrPending
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return readGateExemptionRepairTerminal(ctx, s.db, stored.DomainID, stored.CommandID)
}

func gateRepairSnapshotAt(ctx context.Context, tx *sql.Tx, domain string, position uint64) (gateRepairSnapshot, error) {
	nodes, err := step13NodesTx(ctx, tx)
	if err != nil {
		return gateRepairSnapshot{}, err
	}
	events, err := step13EventsTx(ctx, tx)
	if err != nil {
		return gateRepairSnapshot{}, err
	}
	var prefix []step13Event
	lifecycle := make(map[string]string)
	for key, node := range nodes {
		if node.node.domain == domain {
			lifecycle[key] = "planned"
		}
	}
	for _, event := range events {
		if event.domain != domain || event.position > position {
			continue
		}
		prefix = append(prefix, event)
		if _, to, _, _, ok := step13LifecycleTransition(event.kind, event.payload); ok {
			lifecycle[ownerKey(event.domain, event.subject)] = to
		}
	}
	projection, err := deriveStep13Projection(nodes, prefix)
	if err != nil {
		return gateRepairSnapshot{}, err
	}
	declarations := make(map[string]step13Declaration, len(projection.declarations))
	for _, declaration := range projection.declarations {
		declarations[gateRepairDeclarationKey(declaration.domain, declaration.repo, declaration.gate)] = declaration
	}
	states := make(map[string]step13GateState, len(projection.gateStates))
	for _, state := range projection.gateStates {
		states[gateRepairStateKey(state.domain, state.node, state.gate)] = state
	}
	return gateRepairSnapshot{nodes: nodes, lifecycle: lifecycle, declarations: declarations, states: states}, nil
}

func gateRepairDeclarationKey(domain, repo, gate string) string {
	return ownerKey(domain, repo) + "/" + gate
}

func gateRepairStateKey(domain, node, gate string) string {
	return ownerKey(domain, node) + "/" + gate
}

func assessGateExemptionRepair(snapshot gateRepairSnapshot, command gateExemptionRepairCommand, position uint64, allowExistingExemption bool) *gateRepairRefusal {
	targetKey := ownerKey(command.DomainID, command.NodeID)
	target, exists := snapshot.nodes[targetKey]
	if !exists || !step13NodeLiveAt(target, position) || target.node.repo != command.RepoID {
		return &gateRepairRefusal{"refusal.gate-repair-subject", fmt.Sprintf("%s is not a live node in Repo %s", command.NodeID, command.RepoID)}
	}
	declaration, declared := snapshot.declarations[gateRepairDeclarationKey(command.DomainID, command.RepoID, command.Gate)]
	if !declared || declaration.scale != target.node.kind {
		return &gateRepairRefusal{"refusal.gate-repair-scale", fmt.Sprintf("%s does not bind to %s scale on Repo %s", command.Gate, target.node.kind, command.RepoID)}
	}
	if snapshot.lifecycle[targetKey] != "done" {
		return &gateRepairRefusal{"refusal.gate-repair-lifecycle", fmt.Sprintf("%s was not Done at the %s exemption-repair boundary", command.NodeID, command.Gate)}
	}
	targetState := snapshot.states[gateRepairStateKey(command.DomainID, command.NodeID, command.Gate)].state
	if targetState == "exempt" && allowExistingExemption {
		return nil
	}
	if targetState != "" {
		return &gateRepairRefusal{"refusal.gate-already-satisfied", fmt.Sprintf("%s is already satisfied on %s", command.Gate, command.NodeID)}
	}
	for current := target; ; {
		if !step13NodeLiveAt(current, position) || current.node.repo != command.RepoID {
			return &gateRepairRefusal{"refusal.gate-repair-subject", fmt.Sprintf("ancestor %s is not live in Repo %s", current.node.id, command.RepoID)}
		}
		for _, requirement := range snapshot.declarations {
			if requirement.domain != command.DomainID || requirement.repo != command.RepoID || requirement.scale != current.node.kind {
				continue
			}
			if current.node.id == command.NodeID && requirement.gate == command.Gate {
				continue
			}
			state := snapshot.states[gateRepairStateKey(command.DomainID, current.node.id, requirement.gate)].state
			if state != "closed" && state != "dismissed" && state != "exempt" {
				return &gateRepairRefusal{"refusal.gate-repair-prerequisite", fmt.Sprintf("%s was not sealed before %s became operative: %s is open on %s", command.NodeID, command.Gate, requirement.gate, current.node.id)}
			}
		}
		if current.node.parent == "" {
			break
		}
		parent, ok := snapshot.nodes[ownerKey(command.DomainID, current.node.parent)]
		if !ok {
			return &gateRepairRefusal{"refusal.gate-repair-subject", fmt.Sprintf("ancestor %s is missing", current.node.parent)}
		}
		current = parent
	}
	return nil
}

func validateGateExemptionRepairTerminal(ctx context.Context, tx *sql.Tx, admission gateExemptionRepairAdmission, terminal gateExemptionRepairTerminal) error {
	if terminal.DomainID != admission.DomainID || terminal.CommandID != admission.CommandID || terminal.RequestHash != admission.RequestHash ||
		(terminal.ResultCode != "result.succeeded" && terminal.ResultCode != "result.refused") ||
		!ulid.MatchString(terminal.ObservedEventID) || !validDigest(terminal.ObservedPrefixDigest) {
		return fmt.Errorf("%w: private repair terminal identity invalid", ErrInvalidStore)
	}
	if _, err := utcTime(terminal.OccurredAt); err != nil {
		return fmt.Errorf("%w: private repair terminal time invalid", ErrInvalidStore)
	}
	var observedEventID, prefix string
	if err := tx.QueryRowContext(ctx, `SELECT event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`,
		terminal.DomainID, terminal.ObservedPosition).Scan(&observedEventID, &prefix); err != nil || observedEventID != terminal.ObservedEventID || prefix != terminal.ObservedPrefixDigest {
		return fmt.Errorf("%w: private repair terminal observed prefix mismatch", ErrInvalidStore)
	}
	command, err := decodeGateExemptionRepairCommand(admission.Command, admission.RequestHash)
	if err != nil {
		return ErrInvalidStore
	}
	var storedCommand []byte
	var storedHash, environment, operationName, state string
	var epoch, sequence, version uint64
	err = tx.QueryRowContext(ctx, `SELECT request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
		FROM submissions WHERE domain_id=? AND command_id=?`, admission.DomainID, admission.CommandID).Scan(
		&storedHash, &storedCommand, &epoch, &environment, &sequence, &operationName, &version, &state)
	if err != nil || state != "terminal" || storedHash != admission.RequestHash || !bytes.Equal(storedCommand, admission.Command) ||
		epoch != admission.AuthorityEpoch || environment != admission.EnvironmentID || sequence != admission.EnvironmentSequence ||
		operationName != gateExemptionRepairOperationName || version != 1 {
		return fmt.Errorf("%w: private repair terminal submission mismatch", ErrInvalidStore)
	}
	var receipts int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, admission.DomainID, admission.CommandID).Scan(&receipts); err != nil || receipts != 0 {
		return fmt.Errorf("%w: private repair unexpectedly has a client receipt", ErrInvalidStore)
	}
	var ownedEvents int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, admission.DomainID, admission.CommandID).Scan(&ownedEvents); err != nil {
		return err
	}
	if terminal.ResultCode == "result.refused" {
		if terminal.RefusalCode == "" || terminal.RefusalMessage == "" || terminal.EventID != "" || ownedEvents != 0 {
			return fmt.Errorf("%w: private repair refusal has effects or no status", ErrInvalidStore)
		}
		if terminal.RefusalCode != "refusal.claim-fenced" {
			snapshot, snapshotErr := gateRepairSnapshotAt(ctx, tx, admission.DomainID, terminal.ObservedPosition)
			if snapshotErr != nil {
				return snapshotErr
			}
			guard := assessGateExemptionRepair(snapshot, command, terminal.ObservedPosition, true)
			if guard == nil {
				historical, historyErr := gateRepairSnapshotAt(ctx, tx, admission.DomainID, command.Boundary.EventCount)
				if historyErr != nil {
					return historyErr
				}
				guard = assessGateExemptionRepair(historical, command, command.Boundary.EventCount, false)
			}
			if guard == nil || guard.code != terminal.RefusalCode || guard.message != terminal.RefusalMessage {
				return fmt.Errorf("%w: private repair refusal does not match terminal history", ErrInvalidStore)
			}
		}
		return nil
	}
	if terminal.RefusalCode != "" || terminal.RefusalMessage != "" {
		return fmt.Errorf("%w: successful private repair carries refusal status", ErrInvalidStore)
	}
	if terminal.EventID == "" {
		if ownedEvents != 0 {
			return fmt.Errorf("%w: no-event repair terminal owns events", ErrInvalidStore)
		}
		snapshot, snapshotErr := gateRepairSnapshotAt(ctx, tx, admission.DomainID, terminal.ObservedPosition)
		if snapshotErr != nil {
			return snapshotErr
		}
		guard := assessGateExemptionRepair(snapshot, command, terminal.ObservedPosition, true)
		if guard != nil || snapshot.states[gateRepairStateKey(admission.DomainID, command.NodeID, command.Gate)].state != "exempt" {
			return fmt.Errorf("%w: no-event repair is not an existing exemption", ErrInvalidStore)
		}
		return nil
	}
	if ownedEvents != 1 {
		return fmt.Errorf("%w: successful repair event identity invalid", ErrInvalidStore)
	}
	var position uint64
	var storedEventID string
	if err = tx.QueryRowContext(ctx, `SELECT position,event_id FROM authority_events WHERE domain_id=? AND event_id=? AND command_id=?`,
		admission.DomainID, terminal.EventID, admission.CommandID).Scan(&position, &storedEventID); err != nil || position != terminal.ObservedPosition+1 || storedEventID != terminal.EventID {
		return fmt.Errorf("%w: successful repair event position invalid", ErrInvalidStore)
	}
	if err = validatePrivateGateRepairEvent(ctx, tx, command, terminal.EventID, position); err != nil {
		return err
	}
	snapshot, snapshotErr := gateRepairSnapshotAt(ctx, tx, admission.DomainID, terminal.ObservedPosition)
	if snapshotErr != nil {
		return snapshotErr
	}
	if guard := assessGateExemptionRepair(snapshot, command, terminal.ObservedPosition, false); guard != nil {
		return fmt.Errorf("%w: successful repair was not eligible at terminal time: %v", ErrInvalidStore, guard)
	}
	historical, err := gateRepairSnapshotAt(ctx, tx, admission.DomainID, command.Boundary.EventCount)
	if err != nil {
		return err
	}
	if guard := assessGateExemptionRepair(historical, command, command.Boundary.EventCount, false); guard != nil {
		return fmt.Errorf("%w: successful repair lacked declaration-boundary eligibility: %v", ErrInvalidStore, guard)
	}
	return nil
}

func validatePrivateGateRepairEvent(ctx context.Context, queryer gateRepairQueryer, command gateExemptionRepairCommand, id string, position uint64) error {
	var raw []byte
	var storedID, storedCommand string
	if err := queryer.QueryRowContext(ctx, `SELECT record,event_id,command_id FROM authority_events WHERE domain_id=? AND position=?`,
		command.DomainID, position).Scan(&raw, &storedID, &storedCommand); err != nil {
		return ErrInvalidStore
	}
	return validatePrivateGateRepairEventRecord(command, id, position, raw, storedID, storedCommand)
}

func validatePrivateGateRepairEventRecord(command gateExemptionRepairCommand, id string, position uint64, raw []byte, storedID, storedCommand string) error {
	event, err := parseStep12Event(raw, command.DomainID, position, storedID, storedCommand)
	if err != nil || event.id != id || event.command != command.ID || event.hash != gateRepairRequestHash(commandBytes(command)) ||
		event.environment != command.EnvironmentID || event.sequence != command.EnvironmentSequence || event.acted != command.ActedAt ||
		event.repo != command.RepoID || event.kind != "gate.exemption-repaired" || event.subject != command.NodeID ||
		!step13ClosedPayload(event.payload, &struct {
			Gate string `cbor:"gate"`
		}{}, []string{"gate"}) {
		return fmt.Errorf("%w: private repair event does not match its admitted command", ErrInvalidStore)
	}
	var payload struct {
		Gate string `cbor:"gate"`
	}
	if artifactDecoder.Unmarshal(event.payload["gate"], &payload.Gate) != nil || payload.Gate != command.Gate {
		return fmt.Errorf("%w: private repair event gate mismatch", ErrInvalidStore)
	}
	return nil
}

func commandBytes(command gateExemptionRepairCommand) []byte {
	encoded, _, _ := command.canonicalBytes()
	return encoded
}

func checkStep15State(db *sql.DB) error {
	for _, query := range []string{
		`SELECT count(*) FROM gate_exemption_repair_terminals t
			LEFT JOIN gate_exemption_repair_admissions a ON a.domain_id=t.domain_id AND a.command_id=t.command_id
			LEFT JOIN submissions s ON s.domain_id=t.domain_id AND s.command_id=t.command_id
			LEFT JOIN terminal_receipts r ON r.domain_id=t.domain_id AND r.command_id=t.command_id
			WHERE a.domain_id IS NULL OR s.domain_id IS NULL OR r.command_id IS NOT NULL OR
				t.request_hash!=a.request_hash OR s.request_hash!=a.request_hash OR s.command!=a.command OR
				s.epoch!=a.authority_epoch OR s.environment_id!=a.environment_id OR
				s.environment_sequence!=a.environment_sequence OR s.operation_name!='gate.exemption.repair' OR
				s.operation_version!=1 OR s.state!='terminal'`,
		`SELECT count(*) FROM submissions s
			LEFT JOIN gate_exemption_repair_terminals t ON t.domain_id=s.domain_id AND t.command_id=s.command_id
			LEFT JOIN gate_exemption_repair_admissions a ON a.domain_id=s.domain_id AND a.command_id=s.command_id
			WHERE s.operation_name='gate.exemption.repair' AND
				(t.command_id IS NULL OR a.command_id IS NULL OR s.operation_version!=1 OR s.state!='terminal' OR
				 s.command!=a.command OR s.request_hash!=a.request_hash)`,
	} {
		var invalid int
		if err := db.QueryRow(query).Scan(&invalid); err != nil || invalid != 0 {
			return fmt.Errorf("%w: private repair terminal linkage invalid", ErrInvalidStore)
		}
	}
	rows, err := db.Query(`SELECT domain_id,command_id FROM gate_exemption_repair_terminals ORDER BY domain_id,command_id`)
	if err != nil {
		return err
	}
	type key struct{ domain, command string }
	var terminals []key
	for rows.Next() {
		var item key
		if err = rows.Scan(&item.domain, &item.command); err != nil {
			_ = rows.Close()
			return err
		}
		terminals = append(terminals, item)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range terminals {
		tx, txErr := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if txErr != nil {
			return txErr
		}
		admission, readErr := readGateExemptionRepairAdmission(context.Background(), tx, item.domain, item.command)
		terminal, terminalErr := readGateExemptionRepairTerminal(context.Background(), tx, item.domain, item.command)
		validationErr := readErr
		if validationErr == nil {
			validationErr = terminalErr
		}
		if validationErr == nil {
			validationErr = validateStoredGateExemptionRepairAdmissionCore(context.Background(), tx, admission)
		}
		if validationErr == nil {
			validationErr = validateGateExemptionRepairTerminal(context.Background(), tx, admission, terminal)
		}
		_ = tx.Rollback()
		if validationErr != nil {
			return fmt.Errorf("%w: private repair terminal invalid", ErrInvalidStore)
		}
	}
	return nil
}
