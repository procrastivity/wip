package authoritystore

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

const dependencyULIDAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func (s *Store) submitDependencyCommand(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool, definition operation.Definition,
) (CommandStatus, error) {
	var empty CommandStatus
	metadata := definition.Metadata()
	if metadata.Delivery != operation.DeliveryAuthority || metadata.Claim != operation.ClaimNone ||
		definition.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone != "" || command.Request.Context.Worktree != "" {
		return empty, ErrInvalidProof
	}
	var beforeCommit func() error
	if checkContext {
		beforeCommit = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return nil
		}
	}
	return s.submitIdentity(ctx, commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch,
		environment: command.EnvironmentID, sequence: command.EnvironmentSequence,
		id: command.ID, name: metadata.Operation.Name, version: uint64(metadata.Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, nil, beforeCommit, nil)
}

func completeDependencyTx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	eventID string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{}
	refuse := func(code, message string) (step4Fold, error) {
		fold.result = step4Problem(code, message)
		return fold, nil
	}
	memberships, err := dependencyRepoMemberships(ctx, tx)
	if err != nil {
		return fold, err
	}
	if memberships[identity.repo] != identity.domain {
		return refuse("refusal.dependency-repo", "the command Repo is not a member of this authority domain")
	}
	nodes, edges, position, err := dependencyStateTx(ctx, tx, memberships, identity.domain)
	if err != nil {
		return fold, err
	}
	var blocked, blocker string
	switch input := command.Request.Input.(type) {
	case operation.DependencyAddInput:
		blocked, blocker = input.BlockedID, input.BlockerID
	case operation.DependencyRemoveInput:
		blocked, blocker = input.BlockedID, input.BlockerID
	default:
		return fold, ErrInvalidProof
	}
	if !dependencyEndpointsLive(identity.domain, blocked, blocker, nodes, memberships, position) {
		return refuse("refusal.dependency-endpoint", "both dependency endpoints must be live nodes in this authority domain")
	}
	if blocked == blocker {
		return refuse("refusal.dependency-cycle", "a dependency cannot block itself or create a cycle")
	}
	active := make([]dependencyEdge, 0, len(edges))
	for _, edge := range edges {
		if edge.domain == identity.domain && edge.tombstone == "" &&
			dependencyEndpointsLive(identity.domain, edge.blocked, edge.blocker, nodes, memberships, position) {
			active = append(active, edge)
		}
	}

	switch command.Request.Operation {
	case operation.DependencyAddV1.Metadata().Operation:
		for _, edge := range active {
			if edge.blocked == blocked && edge.blocker == blocker {
				return refuse("refusal.dependency-exists", "that dependency pair is already in force")
			}
		}
		adjacency := make(map[string][]string)
		for _, edge := range active {
			adjacency[edge.blocked] = append(adjacency[edge.blocked], edge.blocker)
		}
		if dependencyReachable(blocker, blocked, adjacency) {
			return refuse("refusal.dependency-cycle", "the dependency would create a cycle")
		}
		edgeID, err := freshDependencyEdgeID(occurred, identity.domain, edges, tx)
		if err != nil {
			return fold, err
		}
		position, err = appendCommandEvent(ctx, tx, identity, occurred, eventID, "dependency.added", blocked,
			map[string]any{"edge": edgeID, "blocker": blocker})
		if err != nil {
			return fold, err
		}
		fold.result = operation.Result{Code: operation.ResultSucceeded, Output: operation.DependencyOutput{
			EdgeID: edgeID, BlockedID: blocked, BlockerID: blocker,
		}}
		fold.output, err = artifactEncoder.Marshal(map[string]any{"edge": edgeID, "blocked_id": blocked, "blocker_id": blocker})
		if err != nil {
			return fold, err
		}
	case operation.DependencyRemoveV1.Metadata().Operation:
		var edgeID string
		for _, edge := range active {
			if edge.blocked == blocked && edge.blocker == blocker {
				edgeID = edge.id
				break
			}
		}
		if edgeID == "" {
			return refuse("refusal.dependency-missing", "that live dependency pair does not exist in this authority domain")
		}
		position, err = appendCommandEvent(ctx, tx, identity, occurred, eventID, "dependency.removed", blocked,
			map[string]any{"edge": edgeID, "blocker": blocker})
		if err != nil {
			return fold, err
		}
		fold.result = operation.Result{Code: operation.ResultSucceeded, Output: operation.DependencyOutput{
			BlockedID: blocked, BlockerID: blocker,
		}}
		fold.output, err = artifactEncoder.Marshal(map[string]any{"blocked_id": blocked, "blocker_id": blocker})
		if err != nil {
			return fold, err
		}
	default:
		return fold, ErrInvalidProof
	}
	if err = dependencyProjectionTx(ctx, tx); err != nil {
		return fold, err
	}
	fold.first, fold.last = position, position
	fold.rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}
	return fold, nil
}

func dependencyStateTx(ctx context.Context, tx *sql.Tx, memberships map[string]string, domain string) (
	map[string]step13Node, []dependencyEdge, uint64, error,
) {
	var noNodes map[string]step13Node
	var noEdges []dependencyEdge
	nodes, err := step13NodesTx(ctx, tx)
	if err != nil {
		return noNodes, noEdges, 0, err
	}
	events, err := step13EventsTx(ctx, tx)
	if err != nil {
		return noNodes, noEdges, 0, err
	}
	edges, err := deriveDependencyProjection(nodes, events, memberships)
	if err != nil {
		return noNodes, noEdges, 0, err
	}
	stored, err := readDependencyProjectionTx(ctx, tx)
	if err != nil {
		return noNodes, noEdges, 0, err
	}
	if !reflect.DeepEqual(stored, edges) {
		return noNodes, noEdges, 0, ErrInvalidStore
	}
	var position uint64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(position),0) FROM authority_events WHERE domain_id=?`, domain).Scan(&position); err != nil {
		return noNodes, noEdges, 0, err
	}
	return nodes, edges, position, nil
}

func dependencyEndpointsLive(domain, blocked, blocker string, nodes map[string]step13Node,
	memberships map[string]string, position uint64,
) bool {
	for _, id := range []string{blocked, blocker} {
		node, exists := nodes[ownerKey(domain, id)]
		if !exists || node.node.domain != domain || memberships[node.node.repo] != domain ||
			(node.node.kind != "matter" && node.node.kind != "step") || node.birthPos > position ||
			!step13NodeLiveAt(node, position) {
			return false
		}
	}
	return true
}

func dependencyReachable(from, target string, adjacency map[string][]string) bool {
	pending := []string{from}
	visited := make(map[string]bool)
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if id == target {
			return true
		}
		if !visited[id] {
			visited[id] = true
			pending = append(pending, adjacency[id]...)
		}
	}
	return false
}

func freshDependencyEdgeID(at time.Time, domain string, edges []dependencyEdge, tx *sql.Tx) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		id, err := makeDependencyEdgeID(at)
		if err != nil {
			return "", err
		}
		var exists int
		if err = tx.QueryRow(`SELECT count(*) FROM m6_dependencies WHERE domain_id=? AND edge_id=?`, domain, id).Scan(&exists); err != nil {
			return "", err
		}
		for _, edge := range edges {
			if edge.domain == domain && edge.id == id {
				exists++
				break
			}
		}
		if exists == 0 {
			return id, nil
		}
	}
	return "", ErrResourceLimit
}

func makeDependencyEdgeID(at time.Time) (string, error) {
	millis := at.UnixMilli()
	if millis < 0 || uint64(millis) >= 1<<48 {
		return "", ErrInvalidProof
	}
	var raw [16]byte
	value := uint64(millis)
	for index := 5; index >= 0; index-- {
		raw[index] = byte(value)
		value >>= 8
	}
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	var encoded [26]byte
	for index := range encoded {
		var character byte
		for bit := 0; bit < 5; bit++ {
			bitIndex := index*5 + bit - 2
			character <<= 1
			if bitIndex >= 0 && bitIndex < len(raw)*8 {
				character |= raw[bitIndex/8] >> (7 - uint(bitIndex%8)) & 1
			}
		}
		encoded[index] = dependencyULIDAlphabet[character]
	}
	if !ulid.MatchString(string(encoded[:])) {
		return "", errors.New("authoritystore: generated invalid dependency edge identity")
	}
	return string(encoded[:]), nil
}
