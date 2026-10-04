package authoritystore

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func dependencyFoldNodes() map[string]step13Node {
	nodes := make(map[string]step13Node)
	for index, kind := range []string{"matter", "step", "matter", "step"} {
		id := claimTestID(20 + index)
		nodes[ownerKey(domainA, id)] = step13Node{
			node: step12Node{domain: domainA, id: id, kind: kind, repo: repoA}, birthPos: uint64(index + 1),
		}
	}
	return nodes
}

func dependencyFoldEvent(position int, kind string, edge, blocked, blocker int) step13Event {
	return step13TestEvent(position, kind, claimTestID(blocked), map[string]any{"edge": claimTestID(edge), "blocker": claimTestID(blocker)})
}

func dependencyFoldMemberships() map[string]string {
	return map[string]string{repoA: domainA, repoB: domainA, repoC: domainA}
}

func TestDependencyFoldCrossRepoGraph(t *testing.T) {
	nodes := dependencyFoldNodes()
	for id, repo := range map[int]string{21: repoB, 22: repoC} {
		key := ownerKey(domainA, claimTestID(id))
		node := nodes[key]
		node.node.repo = repo
		nodes[key] = node
	}
	add := dependencyFoldEvent(5, "dependency.added", 90, 20, 21)
	add.repo = repoC // Command context differs from both endpoint Repos.
	remove := dependencyFoldEvent(6, "dependency.removed", 90, 20, 21)
	remove.repo = repoB // Removal need not use the add's command context.
	want := []dependencyEdge{{domainA, claimTestID(90), repoC, claimTestID(20), claimTestID(21), claimTestID(305), claimTestID(306), claimTestID(306)}}
	if got, err := deriveDependencyProjection(nodes, []step13Event{add, remove}, dependencyFoldMemberships()); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cross-Repo add/remove: %+v %v", got, err)
	}
	next := dependencyFoldEvent(6, "dependency.added", 91, 21, 22)
	next.repo = repoA
	cycle := dependencyFoldEvent(7, "dependency.added", 92, 22, 20)
	cycle.repo = repoB
	duplicate := dependencyFoldEvent(6, "dependency.added", 91, 20, 21)
	duplicate.repo = repoA
	for name, events := range map[string][]step13Event{
		"multi-hop-cycle": {add, next, cycle},
		"duplicate-pair":  {add, duplicate},
	} {
		if _, err := deriveDependencyProjection(nodes, events, dependencyFoldMemberships()); !errors.Is(err, ErrInvalidStore) {
			t.Fatalf("cross-Repo %s accepted: %v", name, err)
		}
	}
	for _, foreign := range []bool{false, true} {
		memberships := dependencyFoldMemberships()
		if foreign {
			memberships[repoC] = domainB
		} else {
			delete(memberships, repoC)
		}
		invalidRemove := remove
		invalidRemove.repo = repoC
		for _, events := range [][]step13Event{{add}, {dependencyFoldEvent(5, "dependency.added", 90, 20, 21), invalidRemove}} {
			if _, err := deriveDependencyProjection(nodes, events, memberships); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("non-member command context accepted (foreign=%v): %v", foreign, err)
			}
		}
	}
	memberships := dependencyFoldMemberships()
	memberships[repoB] = domainB
	if _, err := deriveDependencyProjection(nodes, []step13Event{add}, memberships); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("foreign-domain endpoint Repo accepted: %v", err)
	}
}

func TestDependencyFoldRetainsExactIdentityAndDirection(t *testing.T) {
	// A diamond is a DAG, not a cycle. Removal breaks the only B->D path;
	// adding D->B must then succeed despite retained, removed edge history.
	events := []step13Event{
		dependencyFoldEvent(5, "dependency.added", 90, 20, 21),
		dependencyFoldEvent(6, "dependency.added", 91, 20, 22),
		dependencyFoldEvent(7, "dependency.added", 92, 21, 23),
		dependencyFoldEvent(8, "dependency.added", 93, 22, 23),
		dependencyFoldEvent(9, "dependency.removed", 92, 21, 23),
		dependencyFoldEvent(10, "dependency.added", 94, 23, 21),
	}
	want := []dependencyEdge{
		{domainA, claimTestID(90), repoA, claimTestID(20), claimTestID(21), claimTestID(305), claimTestID(305), ""},
		{domainA, claimTestID(91), repoA, claimTestID(20), claimTestID(22), claimTestID(306), claimTestID(306), ""},
		{domainA, claimTestID(92), repoA, claimTestID(21), claimTestID(23), claimTestID(307), claimTestID(309), claimTestID(309)},
		{domainA, claimTestID(93), repoA, claimTestID(22), claimTestID(23), claimTestID(308), claimTestID(308), ""},
		{domainA, claimTestID(94), repoA, claimTestID(23), claimTestID(21), claimTestID(310), claimTestID(310), ""},
	}
	for iteration := 0; iteration < 3; iteration++ {
		got, err := deriveDependencyProjection(dependencyFoldNodes(), events, dependencyFoldMemberships())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("fold %d: got %+v, want %+v: %v", iteration, got, want, err)
		}
	}
}

func TestDependencyFoldRejectsInvalidHistory(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]step13Node, *[]step13Event)
	}{
		{"self-edge", func(_ map[string]step13Node, events *[]step13Event) {
			*events = []step13Event{dependencyFoldEvent(5, "dependency.added", 90, 20, 20)}
		}},
		{"reverse-cycle", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.added", 91, 21, 20))
		}},
		{"long-cycle", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.added", 91, 21, 22), dependencyFoldEvent(7, "dependency.added", 92, 22, 20))
		}},
		{"duplicate-pair", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.added", 91, 20, 21))
		}},
		{"reissued-id", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.removed", 90, 20, 21), dependencyFoldEvent(7, "dependency.added", 90, 20, 22))
		}},
		{"missing-edge", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.removed", 91, 20, 21))
		}},
		{"wrong-removal-blocked", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.removed", 90, 22, 21))
		}},
		{"wrong-removal-blocker", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.removed", 90, 20, 22))
		}},
		{"double-removal", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.removed", 90, 20, 21), dependencyFoldEvent(7, "dependency.removed", 90, 20, 21))
		}},
		{"old-id-after-readd", func(_ map[string]step13Node, events *[]step13Event) {
			*events = append(*events, dependencyFoldEvent(6, "dependency.removed", 90, 20, 21), dependencyFoldEvent(7, "dependency.added", 91, 20, 21), dependencyFoldEvent(8, "dependency.removed", 90, 20, 21))
		}},
		{"missing-blocked", func(nodes map[string]step13Node, _ *[]step13Event) { delete(nodes, ownerKey(domainA, claimTestID(20))) }},
		{"missing-blocker", func(nodes map[string]step13Node, _ *[]step13Event) { delete(nodes, ownerKey(domainA, claimTestID(21))) }},
		{"unattached-endpoint-repo", func(nodes map[string]step13Node, _ *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			node.node.repo = claimTestID(99)
			nodes[key] = node
		}},
		{"wrong-domain", func(nodes map[string]step13Node, _ *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			delete(nodes, key)
			node.node.domain = domainB
			nodes[ownerKey(domainB, node.node.id)] = node
		}},
		{"stage-endpoint", func(nodes map[string]step13Node, _ *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			node.node.kind = "stage"
			nodes[key] = node
		}},
		{"birth-at-edge", func(nodes map[string]step13Node, _ *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			node.birthPos = 5
			nodes[key] = node
		}},
		{"birth-after-edge", func(nodes map[string]step13Node, _ *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			node.birthPos = 6
			nodes[key] = node
		}},
		{"tombstone-at-edge", func(nodes map[string]step13Node, _ *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			node.tombstonePos = 5
			nodes[key] = node
		}},
		{"remove-after-tombstone", func(nodes map[string]step13Node, events *[]step13Event) {
			key := ownerKey(domainA, claimTestID(21))
			node := nodes[key]
			node.tombstonePos = 6
			nodes[key] = node
			*events = append(*events, dependencyFoldEvent(7, "dependency.removed", 90, 20, 21))
		}},
		{"extra-payload", func(_ map[string]step13Node, events *[]step13Event) {
			(*events)[0] = step13TestEvent(5, "dependency.added", claimTestID(20), map[string]any{"edge": claimTestID(90), "blocker": claimTestID(21), "extra": true})
		}},
		{"null-edge", func(_ map[string]step13Node, events *[]step13Event) {
			(*events)[0] = step13TestEvent(5, "dependency.added", claimTestID(20), map[string]any{"edge": nil, "blocker": claimTestID(21)})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			nodes := dependencyFoldNodes()
			events := []step13Event{dependencyFoldEvent(5, "dependency.added", 90, 20, 21)}
			test.mutate(nodes, &events)
			if _, err := deriveDependencyProjection(nodes, events, dependencyFoldMemberships()); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("invalid history accepted: %v", err)
			}
		})
	}
}

func TestDependencyFoldTombstonesAndDomainIsolation(t *testing.T) {
	nodes := dependencyFoldNodes()
	key := ownerKey(domainA, claimTestID(21))
	node := nodes[key]
	node.tombstonePos = 8
	nodes[key] = node
	// At position 7, the future tombstone must not erase a cycle. At 9 it
	// breaks the path through B without deleting either retained edge.
	events := []step13Event{dependencyFoldEvent(5, "dependency.added", 90, 20, 21), dependencyFoldEvent(6, "dependency.added", 91, 21, 22)}
	if _, err := deriveDependencyProjection(nodes, append(events, dependencyFoldEvent(7, "dependency.added", 92, 22, 20)), dependencyFoldMemberships()); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("future tombstone erased a historical cycle: %v", err)
	}
	if got, err := deriveDependencyProjection(nodes, append(events, dependencyFoldEvent(9, "dependency.added", 92, 22, 20)), dependencyFoldMemberships()); err != nil || len(got) != 3 {
		t.Fatalf("endpoint tombstone did not break live path: %+v %v", got, err)
	}
	for _, id := range []int{20, 21} {
		node := nodes[ownerKey(domainA, claimTestID(id))]
		node.node.domain, node.node.repo, node.tombstonePos = domainB, repoB, 0
		nodes[ownerKey(domainB, node.node.id)] = node
	}
	other := dependencyFoldEvent(5, "dependency.added", 90, 21, 20)
	other.domain, other.repo = domainB, repoB
	memberships := dependencyFoldMemberships()
	memberships[repoB] = domainB
	if got, err := deriveDependencyProjection(nodes, []step13Event{events[0], other}, memberships); err != nil || len(got) != 2 {
		t.Fatalf("separate domains shared edge identity or reachability: %+v %v", got, err)
	}
}

func newDependencyHistoryFixture(t *testing.T) (*claimTestFixture, string) {
	t.Helper()
	f := newClaimTestFixture(t)
	other := claimTestID(24)
	command := matterCommand(claimTestID(11), 2, "beta")
	status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("second Matter: %+v %v", status, err)
	}
	if _, err = f.s.CompleteCommand(context.Background(), status.Owner, success(other, "beta"), other, claimTestID(101), f.now, signWith(f.key)); err != nil {
		t.Fatal(err)
	}
	return f, other
}

func newDependencyCrossRepoHistoryFixture(t *testing.T) *claimTestFixture {
	t.Helper()
	s, root, peer, key, now := commandFixture(t)
	for _, repo := range []string{repoB, repoC} {
		if err := s.AttachRepo(context.Background(), domainA, repo); err != nil {
			t.Fatal(err)
		}
	}
	f := &claimTestFixture{s: s, root: root, peer: peer, key: key, now: now, matter: claimTestID(20)}
	for index, repo := range []string{repoA, repoB, repoC} {
		id := claimTestID(20 + index)
		command := matterCommand(claimTestID(10+index), uint64(index+1), "alpha")
		command.Request.Context.Repo = repo
		status, err := s.SubmitCommand(context.Background(), command, hashCommand(t, command), peer, now)
		if err != nil || status.Owner == nil {
			t.Fatalf("Repo %s Matter submission: %+v %v", repo, status, err)
		}
		if _, err = s.CompleteCommand(context.Background(), status.Owner, success(id, "alpha"), id, claimTestID(100+index), now, signWith(key)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestDependencyCrossRepoHistoryRebuildAndReopen(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	other := claimTestID(21)
	edge := claimTestID(90)
	add := step12Command(f, 13, 4, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, "")
	add.Request.Context.Repo = repoC // Neither endpoint belongs to the command Repo.
	original := retainDependencyHistory(t, f, add, claimTestID(103), edge, "dependency.added", f.matter, other,
		map[string]any{"edge": edge, "blocked_id": f.matter, "blocker_id": other}, true)
	remove := step12Command(f, 14, 5, operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: f.matter, BlockerID: other}, "")
	remove.Request.Context.Repo = repoB
	retainDependencyHistory(t, f, remove, claimTestID(104), edge, "dependency.removed", f.matter, other,
		map[string]any{"blocked_id": f.matter, "blocker_id": other}, true)
	want := []dependencyEdge{{domainA, edge, repoC, f.matter, other, claimTestID(103), claimTestID(104), claimTestID(104)}}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readDependencyProjection(f.s.db)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cross-Repo retained identity: %+v %v", got, err)
	}
	replay, err := f.s.QueryCommand(context.Background(), domainA, add.ID, hashCommand(t, add), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, original.Receipt) || !bytes.Equal(replay.SignedReceipt, original.SignedReceipt) {
		t.Fatalf("cross-Repo signed receipt changed: %+v %v", replay, err)
	}
}

func TestDependencyCrossRepoReopenRejectsCycleAndDuplicate(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "multi-hop-cycle", true: "duplicate-pair"}[duplicate], func(t *testing.T) {
			f := newDependencyCrossRepoHistoryFixture(t)
			defer func() { _ = f.s.Close() }()
			pairs := [][2]string{{claimTestID(20), claimTestID(21)}, {claimTestID(21), claimTestID(22)}, {claimTestID(22), claimTestID(20)}}
			if duplicate {
				pairs = [][2]string{pairs[0], pairs[0]}
			}
			for index, pair := range pairs {
				command := step12Command(f, 13+index, uint64(4+index), operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: pair[0], BlockerID: pair[1]}, "")
				command.Request.Context.Repo = []string{repoC, repoA, repoB}[index]
				edge := claimTestID(90 + index)
				retainDependencyHistory(t, f, command, claimTestID(103+index), edge, "dependency.added", pair[0], pair[1],
					map[string]any{"edge": edge, "blocked_id": pair[0], "blocker_id": pair[1]}, index < len(pairs)-1)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenExisting(f.root)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("cross-Repo invalid graph reopened: %v", err)
			}
		})
	}
}

func TestDependencyHistoryBindsCommandContext(t *testing.T) {
	f, other := newDependencyHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	command := step12Command(f, 12, 3, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, "")
	command.Request.Context.Repo = repoC
	hash := hashCommand(t, command)
	event := dependencyFoldEvent(5, "dependency.added", 90, 20, 24).step12Event
	event.hash, event.command, event.environment, event.sequence, event.acted = hash, command.ID, envA, 3, command.ActedAt
	event.repo = repoC
	if err := validateDependencyHistoryEvent(event, storedSubmission{hash: hash}, command); err != nil {
		t.Fatal(err)
	}
	event.repo = repoB
	if err := validateDependencyHistoryEvent(event, storedSubmission{hash: hash}, command); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("event substituted another domain-valid context Repo: %v", err)
	}
}

func TestDependencyRefusalHistoryChecksContextMembershipOnReopen(t *testing.T) {
	for _, definition := range []operation.Definition{operation.DependencyAddV1, operation.DependencyRemoveV1} {
		t.Run(definition.Metadata().Operation.Name, func(t *testing.T) {
			f := newDependencyCrossRepoHistoryFixture(t)
			defer func() { _ = f.s.Close() }()
			// An absent endpoint may be named in a refused request; no
			// effect or graph row may be promoted from this terminal history.
			foreign := claimTestID(99)
			var input operation.Input = operation.DependencyAddInput{BlockedID: f.matter, BlockerID: foreign}
			if definition.Metadata().Operation == operation.DependencyRemoveV1.Metadata().Operation {
				input = operation.DependencyRemoveInput{BlockedID: f.matter, BlockerID: foreign}
			}
			command := step12Command(f, 13, 4, definition, input, "")
			command.Request.Context.Repo = repoC
			raw, err := command.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			submission := commandIdentity{
				domain: domainA, epoch: 7, environment: envA, sequence: 4, id: command.ID,
				name: command.Request.Operation.Name, version: 1, repo: repoC, encoded: raw, hash: hashCommand(t, command), m1: &command,
			}
			if _, err = f.s.submitIdentity(context.Background(), submission, f.peer, f.now, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			tx, err := f.s.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			original, err := f.s.finishCommandTx(context.Background(), tx, submission, 3, "result.refused", nil, "refusal.dependency-target-missing", nil, nil, nil, f.now, signWith(f.key), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.s.QueryCommand(context.Background(), domainA, command.ID, submission.hash, 7, f.peer, envA, f.now)
			if err != nil || !bytes.Equal(replay.SignedReceipt, original.SignedReceipt) {
				t.Fatalf("refusal receipt changed: %+v %v", replay, err)
			}
			if edges, err := readDependencyProjection(f.s.db); err != nil || len(edges) != 0 {
				t.Fatalf("refusal promoted graph state: %+v %v", edges, err)
			}
			otherDomain, _ := identity(domainB, 1)
			if err = f.s.BootstrapDomain(context.Background(), otherDomain, claimTestID(98)); err != nil {
				t.Fatal(err)
			}
			// Bypass only membership immutability, restore the exact trigger,
			// and leave the original request/hash/receipt/signature untouched.
			if _, err = f.s.db.Exec(`DROP TRIGGER membership_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.db.Exec(`UPDATE repo_memberships SET domain_id=? WHERE repo_id=?`, domainB, repoC); err != nil {
				t.Fatal(err)
			}
			for _, object := range baselineSchema {
				if object.name == "membership_immutable" {
					if _, err = f.s.db.Exec(object.sql); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = checkDependencyState(f.s.db); err != nil {
				t.Fatalf("empty graph must not mask the command-context check: %v", err)
			}
			if err = f.s.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenExisting(f.root)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("refusal with foreign command context reopened: %v", err)
			}
		})
	}
}

// Test-only historical terminal construction uses the existing signing and
// receipt transaction primitive to build event histories independently.
func retainDependencyHistory(t *testing.T, f *claimTestFixture, command operation.Command, eventID, edgeID, kind, subject, blocker string, outputFields map[string]any, rebuild bool) CommandStatus {
	t.Helper()
	ctx := context.Background()
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hash := hashCommand(t, command)
	identity := commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch, environment: command.EnvironmentID,
		sequence: command.EnvironmentSequence, id: command.ID, name: command.Request.Operation.Name, version: uint64(command.Request.Operation.Version),
		repo: command.Request.Context.Repo, encoded: raw, hash: hash, m1: &command,
	}
	if _, err = f.s.submitIdentity(ctx, identity, f.peer, f.now, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	position, err := appendCommandEvent(ctx, tx, eventIdentity{identity.domain, identity.id, hash, identity.environment, identity.sequence, command.ActedAt, identity.repo},
		f.now, eventID, kind, subject, map[string]any{"edge": edgeID, "blocker": blocker})
	if err != nil {
		t.Fatal(err)
	}
	if rebuild {
		if err = dependencyProjectionTx(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	output, err := artifactEncoder.Marshal(outputFields)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.s.finishCommandTx(ctx, tx, identity, command.EnvironmentSequence-1, string(operation.ResultSucceeded), output, nil,
		map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}, position, position, f.now, signWith(f.key), nil)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestDependencyProjectionRebuildAndReopen(t *testing.T) {
	f, other := newDependencyHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	add := step12Command(f, 12, 3, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, "")
	edge := claimTestID(90)
	original := retainDependencyHistory(t, f, add, claimTestID(102), edge, "dependency.added", f.matter, other,
		map[string]any{"edge": edge, "blocked_id": f.matter, "blocker_id": other}, true)
	if _, err := f.s.db.Exec(`UPDATE m6_dependencies SET last_event_id=? WHERE edge_id=?`, claimTestID(101), edge); err == nil {
		t.Fatal("schema allowed an active edge's last event to differ from its birth")
	}
	remove := step12Command(f, 13, 4, operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: f.matter, BlockerID: other}, "")
	retainDependencyHistory(t, f, remove, claimTestID(103), edge, "dependency.removed", f.matter, other,
		map[string]any{"blocked_id": f.matter, "blocker_id": other}, true)
	freshEdge := claimTestID(91)
	readd := step12Command(f, 14, 5, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, "")
	retainDependencyHistory(t, f, readd, claimTestID(104), freshEdge, "dependency.added", f.matter, other,
		map[string]any{"edge": freshEdge, "blocked_id": f.matter, "blocker_id": other}, true)
	want := []dependencyEdge{
		{domainA, edge, repoA, f.matter, other, claimTestID(102), claimTestID(103), claimTestID(103)},
		{domainA, freshEdge, repoA, f.matter, other, claimTestID(104), claimTestID(104), ""},
	}
	for iteration := 0; iteration < 2; iteration++ {
		tx, err := f.s.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = dependencyProjectionTx(context.Background(), tx); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		got, err := readDependencyProjection(f.s.db)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("rebuild %d: %+v %v", iteration, got, err)
		}
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.s.QueryCommand(context.Background(), domainA, add.ID, hashCommand(t, add), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, original.Receipt) || !bytes.Equal(replay.SignedReceipt, original.SignedReceipt) {
		t.Fatalf("reopen changed original signed terminal: %+v %v", replay, err)
	}
	if _, err = f.s.db.Exec(`DELETE FROM m6_dependencies`); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("reopen repaired missing projection: %v", err)
	}
}

func TestDependencyReopenRejectsInvalidSignedHistory(t *testing.T) {
	for _, mutation := range []string{"cycle", "wrong-removal-edge", "wrong-subject", "wrong-event-direction", "wrong-output-edge", "extra-output"} {
		t.Run(mutation, func(t *testing.T) {
			f, other := newDependencyHistoryFixture(t)
			defer func() { _ = f.s.Close() }()
			edge := claimTestID(90)
			add := step12Command(f, 12, 3, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, "")
			output := map[string]any{"edge": edge, "blocked_id": f.matter, "blocker_id": other}
			subject := f.matter
			if mutation == "wrong-output-edge" {
				output["edge"] = claimTestID(91)
			}
			if mutation == "extra-output" {
				output["unknown"] = true
			}
			if mutation == "wrong-subject" {
				subject = other
			}
			blocker := other
			if mutation == "wrong-event-direction" {
				// A valid DAG and internally consistent receipt/projection, but
				// the opposite of the exact signed request's intended edge.
				subject, blocker = other, f.matter
				output["blocked_id"], output["blocker_id"] = subject, blocker
			}
			retainDependencyHistory(t, f, add, claimTestID(102), edge, "dependency.added", subject, blocker, output, mutation != "wrong-subject")
			if mutation == "cycle" {
				reverse := step12Command(f, 13, 4, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: other, BlockerID: f.matter}, "")
				retainDependencyHistory(t, f, reverse, claimTestID(103), claimTestID(91), "dependency.added", other, f.matter,
					map[string]any{"edge": claimTestID(91), "blocked_id": other, "blocker_id": f.matter}, false)
			}
			if mutation == "wrong-removal-edge" {
				remove := step12Command(f, 13, 4, operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: f.matter, BlockerID: other}, "")
				retainDependencyHistory(t, f, remove, claimTestID(103), claimTestID(91), "dependency.removed", f.matter, other,
					map[string]any{"blocked_id": f.matter, "blocker_id": other}, false)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			if store, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatalf("reopen accepted %s: %v", mutation, err)
			}
		})
	}
}

func TestDependencyReopenRejectsSubstitutedProjection(t *testing.T) {
	f, other := newDependencyHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	edge := claimTestID(90)
	add := step12Command(f, 12, 3, operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, "")
	retainDependencyHistory(t, f, add, claimTestID(102), edge, "dependency.added", f.matter, other,
		map[string]any{"edge": edge, "blocked_id": f.matter, "blocker_id": other}, true)
	if _, err := f.s.db.Exec(`UPDATE m6_dependencies SET blocked_id=?,blocker_id=? WHERE edge_id=?`, other, f.matter, edge); err == nil {
		t.Fatal("schema allowed edge identity substitution")
	}
	// A replacement row satisfies all SQL constraints, but reverses the exact
	// event-derived identity. Reopen must compare the complete projection.
	if _, err := f.s.db.Exec(`DELETE FROM m6_dependencies WHERE edge_id=?`, edge); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`INSERT INTO m6_dependencies VALUES(?,?,?,?,?,?,?,NULL)`, domainA, edge, repoA, other, f.matter, claimTestID(102), claimTestID(102)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("reopen accepted reversed projection: %v", err)
	}
}
