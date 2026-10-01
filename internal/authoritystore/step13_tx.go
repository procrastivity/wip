package authoritystore

import (
	"context"
	"database/sql"
)

func step13ProjectionTx(ctx context.Context, tx *sql.Tx) error {
	nodes, err := step13NodesTx(ctx, tx)
	if err != nil {
		return err
	}
	events, err := step13EventsTx(ctx, tx)
	if err != nil {
		return err
	}
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		return err
	}
	for _, table := range []string{"m6_tracker_candidates", "m6_tracker_aggregates", "m6_tracker_references", "m6_gate_states", "m6_gate_declarations", "m6_repo_config"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	for _, value := range projection.config {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_repo_config VALUES(?,?,?,?,?)`, value.domain, value.repo, value.key, value.value, value.event); err != nil {
			return err
		}
	}
	for _, value := range projection.declarations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_gate_declarations VALUES(?,?,?,?,?)`, value.domain, value.repo, value.gate, value.scale, value.event); err != nil {
			return err
		}
	}
	for _, value := range projection.gateStates {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_gate_states VALUES(?,?,?,?,?,?,?,?)`, value.domain, value.repo, value.node, value.gate, value.scale, value.state, nullableString(value.reason), value.event); err != nil {
			return err
		}
	}
	for _, value := range projection.references {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_tracker_references VALUES(?,?,?,?,?,?)`, value.domain, value.matter, value.ref, nullableString(value.removed), value.birth, value.last); err != nil {
			return err
		}
	}
	for _, value := range projection.aggregates {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_tracker_aggregates VALUES(?,?,?,?)`, value.domain, value.ref, nullableString(value.disposition), value.members); err != nil {
			return err
		}
	}
	for _, value := range projection.candidates {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_tracker_candidates VALUES(?,?,?,?,?,?,?,?,?)`, value.domain, value.id, value.repo, value.kind, value.subject, value.ref, value.key, value.payload, value.event); err != nil {
			return err
		}
	}
	return nil
}

func step13NodesTx(ctx context.Context, tx *sql.Tx) (map[string]step13Node, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.domain_id,n.node_id,n.kind,n.repo_id,n.matter_id,coalesce(n.parent_id,''),n.locator,n.title,n.sort_key,
		n.birth_event_id,n.last_event_id,coalesce(n.tombstone_event_id,''),n.repair_required,coalesce(n.requested_locator,''),e.position,t.position
		FROM m6_nodes n JOIN authority_events e ON e.domain_id=n.domain_id AND e.event_id=n.birth_event_id
		LEFT JOIN authority_events t ON t.domain_id=n.domain_id AND t.event_id=n.tombstone_event_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	nodes := make(map[string]step13Node)
	for rows.Next() {
		var node step12Node
		var repair int
		var position uint64
		var tombstonePosition sql.NullInt64
		if err = rows.Scan(&node.domain, &node.id, &node.kind, &node.repo, &node.matter, &node.parent,
			&node.locator, &node.title, &node.sortKey, &node.birth, &node.last, &node.tombstone, &repair, &node.requested, &position, &tombstonePosition); err != nil {
			return nil, err
		}
		node.repair = repair == 1
		item := step13Node{node: node, birthPos: position}
		if tombstonePosition.Valid {
			item.tombstonePos = tombstonePosition.Int64
		}
		nodes[ownerKey(node.domain, node.id)] = item
	}
	return nodes, rows.Err()
}

func step13EventsTx(ctx context.Context, tx *sql.Tx) ([]step13Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT domain_id,position,event_id,command_id,record FROM authority_events ORDER BY domain_id,position`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []step13Event
	for rows.Next() {
		var domain, id, command string
		var position uint64
		var raw []byte
		if err = rows.Scan(&domain, &position, &id, &command, &raw); err != nil {
			return nil, err
		}
		event, parseErr := parseStep12Event(raw, domain, position, id, command)
		if parseErr != nil {
			return nil, parseErr
		}
		events = append(events, step13Event{step12Event: event})
	}
	return events, rows.Err()
}
