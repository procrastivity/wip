package authoritystore

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"

	"github.com/procrastivity/wip/internal/operation"
)

type dependencyEdge struct {
	// repo retains the add command's context, not ownership of the graph or endpoints.
	domain, id, repo, blocked, blocker, birth, last, tombstone string
}

// Dependencies retain their exact edge identity after removal. Endpoint
// tombstones remove edges from the live domain-wide graph, not retained history.
func deriveDependencyProjection(nodes map[string]step13Node, events []step13Event, memberships map[string]string) ([]dependencyEdge, error) {
	edges := make(map[string]dependencyEdge)
	for _, event := range events {
		if event.kind != "dependency.added" && event.kind != "dependency.removed" {
			continue
		}
		if memberships[event.repo] != event.domain {
			return nil, ErrInvalidStore
		}
		var payload struct {
			Edge    string `cbor:"edge"`
			Blocker string `cbor:"blocker"`
		}
		if !step13ClosedPayload(event.payload, &payload, []string{"edge", "blocker"}) ||
			!ulid.MatchString(payload.Edge) || !ulid.MatchString(payload.Blocker) || event.subject == payload.Blocker {
			return nil, ErrInvalidStore
		}
		for _, id := range []string{event.subject, payload.Blocker} {
			node, exists := nodes[ownerKey(event.domain, id)]
			if !exists || node.node.domain != event.domain || memberships[node.node.repo] != event.domain ||
				(node.node.kind != "matter" && node.node.kind != "step") || node.birthPos >= event.position ||
				!step13NodeLiveAt(node, event.position) {
				return nil, ErrInvalidStore
			}
		}
		key := ownerKey(event.domain, payload.Edge)
		if event.kind == "dependency.removed" {
			edge, exists := edges[key]
			if !exists || edge.tombstone != "" || edge.blocked != event.subject || edge.blocker != payload.Blocker {
				return nil, ErrInvalidStore
			}
			edge.last, edge.tombstone = event.id, event.id
			edges[key] = edge
			continue
		}
		if _, exists := edges[key]; exists {
			return nil, ErrInvalidStore
		}
		adjacency := make(map[string][]string)
		for _, edge := range edges {
			if edge.domain != event.domain || edge.tombstone != "" ||
				!step13NodeLiveAt(nodes[ownerKey(edge.domain, edge.blocked)], event.position) ||
				!step13NodeLiveAt(nodes[ownerKey(edge.domain, edge.blocker)], event.position) {
				continue
			}
			if edge.blocked == event.subject && edge.blocker == payload.Blocker {
				return nil, ErrInvalidStore
			}
			adjacency[edge.blocked] = append(adjacency[edge.blocked], edge.blocker)
		}
		// A blocked->blocker addition cycles iff blocker already reaches blocked.
		pending := []string{payload.Blocker}
		visited := make(map[string]bool)
		for len(pending) != 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if id == event.subject {
				return nil, ErrInvalidStore
			}
			if !visited[id] {
				visited[id] = true
				pending = append(pending, adjacency[id]...)
			}
		}
		edges[key] = dependencyEdge{event.domain, payload.Edge, event.repo, event.subject, payload.Blocker, event.id, event.id, ""}
	}
	projection := make([]dependencyEdge, 0, len(edges))
	for _, edge := range edges {
		projection = append(projection, edge)
	}
	sort.Slice(projection, func(i, j int) bool {
		return ownerKey(projection[i].domain, projection[i].id) < ownerKey(projection[j].domain, projection[j].id)
	})
	return projection, nil
}

func dependencyRepoMemberships(ctx context.Context, queryer step13ProjectionQueryer) (map[string]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT repo_id,domain_id FROM repo_memberships`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	memberships := make(map[string]string)
	for rows.Next() {
		var repo, domain string
		if err = rows.Scan(&repo, &domain); err != nil {
			return nil, err
		}
		memberships[repo] = domain
	}
	return memberships, rows.Err()
}

func readDependencyProjection(db *sql.DB) ([]dependencyEdge, error) {
	return readDependencyProjectionTx(context.Background(), db)
}

func readDependencyProjectionTx(ctx context.Context, queryer step13ProjectionQueryer) ([]dependencyEdge, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT domain_id,edge_id,repo_id,blocked_id,blocker_id,birth_event_id,last_event_id,coalesce(tombstone_event_id,'')
		FROM m6_dependencies ORDER BY domain_id,edge_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	edges := make([]dependencyEdge, 0)
	for rows.Next() {
		var edge dependencyEdge
		if err = rows.Scan(&edge.domain, &edge.id, &edge.repo, &edge.blocked, &edge.blocker, &edge.birth, &edge.last, &edge.tombstone); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func checkDependencyState(db *sql.DB) error {
	nodes, err := step13Nodes(db)
	if err != nil {
		return err
	}
	events, err := step13Events(db)
	if err != nil {
		return err
	}
	memberships, err := dependencyRepoMemberships(context.Background(), db)
	if err != nil {
		return err
	}
	want, err := deriveDependencyProjection(nodes, events, memberships)
	if err != nil {
		return fmt.Errorf("%w: dependency event fold: %v", ErrInvalidStore, err)
	}
	got, err := readDependencyProjection(db)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: dependency projection differs from event history", ErrInvalidStore)
	}
	return nil
}

// Rebuild inside the caller's migration or future command transaction. Ordinary
// reopen only checks, never repairs a divergent projection.
func dependencyProjectionTx(ctx context.Context, tx *sql.Tx) error {
	nodes, err := step13NodesTx(ctx, tx)
	if err != nil {
		return err
	}
	events, err := step13EventsTx(ctx, tx)
	if err != nil {
		return err
	}
	memberships, err := dependencyRepoMemberships(ctx, tx)
	if err != nil {
		return err
	}
	edges, err := deriveDependencyProjection(nodes, events, memberships)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM m6_dependencies`); err != nil {
		return err
	}
	for _, edge := range edges {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_dependencies VALUES(?,?,?,?,?,?,?,?)`,
			edge.domain, edge.id, edge.repo, edge.blocked, edge.blocker, edge.birth, edge.last, nullableString(edge.tombstone)); err != nil {
			return err
		}
	}
	return nil
}

// This definition lookup is for retained history only, not command admission.
func dependencyHistoryDefinition(id operation.ID) (operation.Definition, bool) {
	for _, definition := range []operation.Definition{operation.DependencyAddV1, operation.DependencyRemoveV1} {
		if definition.Metadata().Operation == id {
			return definition, true
		}
	}
	return operation.Definition{}, false
}

func validateDependencyHistoryEvent(event step12Event, submission storedSubmission, command operation.Command) error {
	definition, exists := dependencyHistoryDefinition(command.Request.Operation)
	if !exists || definition.ValidateRequest(command.Request) != nil || command.Request.Claim != nil ||
		event.hash != submission.hash || event.domain != command.AuthorityDomainID || event.command != command.ID ||
		event.repo != command.Request.Context.Repo || event.environment != command.EnvironmentID ||
		event.sequence != command.EnvironmentSequence || event.acted != command.ActedAt {
		return ErrInvalidStore
	}
	var payload struct {
		Edge    string `cbor:"edge"`
		Blocker string `cbor:"blocker"`
	}
	if !step13ClosedPayload(event.payload, &payload, []string{"edge", "blocker"}) || !ulid.MatchString(payload.Edge) {
		return ErrInvalidStore
	}
	switch input := command.Request.Input.(type) {
	case operation.DependencyAddInput:
		if event.kind != "dependency.added" || event.subject != input.BlockedID || payload.Blocker != input.BlockerID {
			return ErrInvalidStore
		}
	case operation.DependencyRemoveInput:
		if event.kind != "dependency.removed" || event.subject != input.BlockedID || payload.Blocker != input.BlockerID {
			return ErrInvalidStore
		}
	default:
		return ErrInvalidStore
	}
	return nil
}

func checkDependencyCommands(db *sql.DB) error {
	rows, err := db.Query(`SELECT s.command,r.receipt,r.first_position,r.last_position,coalesce(e.event_id,''),coalesce(e.record,X'')
		FROM submissions s JOIN terminal_receipts r USING(domain_id,command_id)
		LEFT JOIN authority_events e ON e.domain_id=s.domain_id AND e.command_id=s.command_id AND e.position=r.first_position
		WHERE s.operation_name IN ('dependency.add','dependency.remove')`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw, receiptBytes, eventBytes []byte
		var eventID string
		var first, last sql.NullInt64
		if err = rows.Scan(&raw, &receiptBytes, &first, &last, &eventID, &eventBytes); err != nil {
			return err
		}
		command, decodeErr := operation.DecodeCanonicalCommand(raw)
		definition, known := dependencyHistoryDefinition(command.Request.Operation)
		if decodeErr != nil || !known || definition.ValidateRequest(command.Request) != nil || command.Request.Claim != nil {
			return ErrInvalidStore
		}
		receipt, err := readReceipt(receiptBytes)
		if err != nil {
			return err
		}
		if receipt.Result.Code != string(operation.ResultSucceeded) {
			continue
		}
		if !first.Valid || !last.Valid || first.Int64 != last.Int64 {
			return ErrInvalidStore
		}
		var output struct {
			Edge    string `cbor:"edge"`
			Blocked string `cbor:"blocked_id"`
			Blocker string `cbor:"blocker_id"`
		}
		names := []string{"blocked_id", "blocker_id"}
		if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation {
			names = append(names, "edge")
		}
		if closedPayload(receipt.Result.Output, &output, names...) != nil {
			return ErrInvalidStore
		}
		event, err := parseStep12Event(eventBytes, command.AuthorityDomainID, uint64(first.Int64), eventID, command.ID)
		if err != nil {
			return err
		}
		var edge, blocker string
		if canonicalDecode(event.payload["edge"], &edge) != nil || canonicalDecode(event.payload["blocker"], &blocker) != nil ||
			output.Blocked != event.subject || output.Blocker != blocker ||
			(command.Request.Operation == operation.DependencyAddV1.Metadata().Operation && output.Edge != edge) {
			return ErrInvalidStore
		}
	}
	return rows.Err()
}
