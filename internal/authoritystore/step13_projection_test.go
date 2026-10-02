package authoritystore

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func downgradeStep13ToV11(t *testing.T, root string) {
	t.Helper()
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	dropStep17BoundarySchemaForTest(t, db)
	for index := len(step15Schema) - 1; index >= 0; index-- {
		object := step15Schema[index]
		if _, err = db.Exec("DROP " + object.kind + " IF EXISTS " + object.name); err != nil {
			_ = db.Close()
			t.Fatalf("drop v14 object %s: %v", object.name, err)
		}
	}
	for _, object := range step4Schema {
		if object.name == "environment_sequence_terminal" {
			if _, err = db.Exec(object.sql); err != nil {
				_ = db.Close()
				t.Fatalf("restore v13 sequence trigger: %v", err)
			}
		}
	}
	for index := len(step14Schema) - 1; index >= 0; index-- {
		object := step14Schema[index]
		if _, err = db.Exec("DROP " + object.kind + " IF EXISTS " + object.name); err != nil {
			_ = db.Close()
			t.Fatalf("drop v13 object %s: %v", object.name, err)
		}
	}
	for index := len(step13Schema) - 1; index >= 0; index-- {
		object := step13Schema[index]
		if _, err = db.Exec("DROP " + object.kind + " IF EXISTS " + object.name); err != nil {
			_ = db.Close()
			t.Fatalf("drop v12 object %s: %v", object.name, err)
		}
	}
	if _, err = db.Exec(`DROP TABLE schema_migrations`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(step12MigrationMarker.sql); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	markers := []string{
		"baseline", "environment-and-artifacts", "submissions-and-receipts", "prefix-snapshot-blob-transfer",
		"claims-grants-journals-close", "m5-lab-genesis-grant-consumption", "step-8-provisional-birth-projection",
		"step-9-birth-journal-receipt-barrier", "step-10-content-and-findings", "step-16-claim-journal-sequence-order",
		"step-4-stage-step-operations",
	}
	for index, marker := range markers {
		if _, err = db.Exec(`INSERT INTO schema_migrations(version,name) VALUES(?,?)`, index+1, marker); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`PRAGMA user_version=11`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = checkSchemaVersion(db, 11); err != nil {
		_ = db.Close()
		t.Fatalf("downgraded v11 fixture validation: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func createLegacyInlineSweepFinish(t *testing.T, f *claimTestFixture, allocation AcquireAllocation) (operation.Command, string, CommandStatus) {
	t.Helper()
	ctx := context.Background()
	start := step12Command(f, 12, 3, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: f.matter}, allocation.ClaimID)
	started := completeLifecycleCommand(t, f, start, []string{claimTestID(105)})
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)
	finish := step12Command(f, 13, 4, operation.MatterFinishV1,
		operation.MatterFinishInput{MatterID: f.matter}, allocation.ClaimID)
	hash := hashCommand(t, finish)
	pending, err := f.s.SubmitCommand(ctx, finish, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit historical Matter finish: status=%+v err=%v", pending, err)
	}
	canonical, err := finish.CanonicalBytes()
	if err != nil {
		t.Fatalf("encode historical Matter finish: %v", err)
	}
	parsed, err := parseLifecycle(canonical, hash)
	if err != nil {
		t.Fatalf("parse historical Matter finish: %v", err)
	}
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var batch string
	if err = tx.QueryRowContext(ctx, `SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, domainA, f.matter).Scan(&batch); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	identity := eventIdentity{parsed.domain, parsed.id, parsed.hash, parsed.environment, parsed.sequence, parsed.actedAt, parsed.repo}
	firstID, sweepID := claimTestID(106), claimTestID(107)
	first, err := appendCommandEvent(ctx, tx, identity, f.now, firstID, "matter.finished", f.matter,
		map[string]any{"from": "in-progress", "to": "done"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	last, err := appendCommandEvent(ctx, tx, identity, f.now, sweepID, "batch.swept", batch, map[string]any{})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE m6_nodes SET last_event_id=? WHERE domain_id=? AND node_id=?`, firstID, domainA, f.matter); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	output, err := artifactEncoder.Marshal(map[string]any{
		"matter_id": f.matter, "state": "done", "became_sealed": true,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	rangeValue := map[string]any{"first_event_id": firstID, "last_event_id": sweepID, "event_count": uint64(2)}
	completed, err := f.s.finishCommandTx(ctx, tx, parsed.commandIdentity, finish.EnvironmentSequence-1,
		string(operation.ResultSucceeded), output, nil, rangeValue, first, last, f.now, signWith(f.key), nil)
	if err != nil {
		t.Fatalf("record historical inline-sweep finish receipt: %v", err)
	}
	return finish, hash, completed
}

func corruptAuthorityEventSubject(t *testing.T, root, eventID, subject string) {
	t.Helper()
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var domain string
	var position uint64
	var record []byte
	if err = db.QueryRow(`SELECT domain_id,position,record FROM authority_events WHERE event_id=?`, eventID).Scan(&domain, &position, &record); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = artifactDecoder.Unmarshal(record, &fields); err != nil {
		t.Fatal(err)
	}
	fields["subject_id"] = subject
	record, err = artifactEncoder.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	if err = db.QueryRow(`SELECT prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, domain, position-1).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	previousBytes, err := hex.DecodeString(strings.TrimPrefix(previous, "sha256:"))
	if err != nil {
		t.Fatal(err)
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(record)))
	h := sha256.New()
	_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
	_, _ = h.Write(previousBytes)
	_, _ = h.Write(length[:])
	_, _ = h.Write(record)
	if _, err = db.Exec(`DROP TRIGGER authority_events_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE authority_events SET record=?,prefix_digest=? WHERE domain_id=? AND position=?`, record, digestRawBytes(h.Sum(nil)), domain, position); err != nil {
		t.Fatal(err)
	}
}

func TestStep13UpgradeV11AcceptsHistoricalInlineSweepFinishAndReopens(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	finish, hash, historical := createLegacyInlineSweepFinish(t, f, allocation)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	downgradeStep13ToV11(t, f.root)
	if err := UpgradeV11(f.root); err != nil {
		t.Fatalf("upgrade valid v11 inline-sweep history: %v", err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("open upgraded v13 history: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	replay, err := reopened.SubmitCommand(context.Background(), finish, hash, f.peer, f.now)
	if err != nil || replay.Pending || !reflect.DeepEqual(replay.Receipt, historical.Receipt) {
		t.Fatalf("historical finish receipt replay: pending=%t same=%t err=%v", replay.Pending, reflect.DeepEqual(replay.Receipt, historical.Receipt), err)
	}
	receipt, err := readReceipt(replay.Receipt)
	if err != nil || receipt.Range == nil || receipt.Range.Count != 2 {
		t.Fatalf("historical inline-sweep receipt = %+v, %v", receipt.Range, err)
	}
}

func TestStep13UpgradeV11RejectsMalformedHistoricalInlineSweepFinish(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	_, _, _ = createLegacyInlineSweepFinish(t, f, allocation)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	downgradeStep13ToV11(t, f.root)
	corruptAuthorityEventSubject(t, f.root, claimTestID(107), claimTestID(999))
	if err := UpgradeV11(f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("upgrade accepted malformed inline-sweep history: %v", err)
	}
}

func TestStep13UpgradeV11PreservesRetainedStateAndReopens(t *testing.T) {
	f := newClaimTestFixture(t)
	var beforeEvent []byte
	if err := f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, claimTestID(100)).Scan(&beforeEvent); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	downgradeStep13ToV11(t, f.root)
	if _, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open silently accepted v11: %v", err)
	}
	if err := UpgradeV11(f.root); err != nil {
		t.Fatalf("upgrade v11: %v", err)
	}
	backup, err := connect(filepath.Join(f.root, "authority-v11.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaVersion(backup, 11); err != nil {
		_ = backup.Close()
		t.Fatalf("retained backup is not validated v11: %v", err)
	}
	var backupEvents, backupMatters, backupNodes int
	if err = backup.QueryRow(`SELECT count(*) FROM authority_events`).Scan(&backupEvents); err != nil {
		t.Fatal(err)
	}
	if err = backup.QueryRow(`SELECT count(*) FROM matters`).Scan(&backupMatters); err != nil {
		t.Fatal(err)
	}
	if err = backup.QueryRow(`SELECT count(*) FROM m6_nodes`).Scan(&backupNodes); err != nil {
		t.Fatal(err)
	}
	if err = backup.Close(); err != nil {
		t.Fatal(err)
	}
	if backupEvents != 1 || backupMatters != 1 || backupNodes != 1 {
		t.Fatalf("retained v11 state counts events/matters/nodes = %d/%d/%d, want 1/1/1", backupEvents, backupMatters, backupNodes)
	}
	upgraded, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("open upgraded v12: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	var afterEvent []byte
	if err = upgraded.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, claimTestID(100)).Scan(&afterEvent); err != nil {
		t.Fatal(err)
	}
	var matters, nodes int
	if err = upgraded.db.QueryRow(`SELECT count(*) FROM matters`).Scan(&matters); err != nil {
		t.Fatal(err)
	}
	if err = upgraded.db.QueryRow(`SELECT count(*) FROM m6_nodes`).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterEvent, beforeEvent) || matters != 1 || nodes != 1 {
		t.Fatalf("v12 upgrade changed prior authority state: event-equal=%t matters/nodes=%d/%d", reflect.DeepEqual(afterEvent, beforeEvent), matters, nodes)
	}
}

func TestStep13GateStatesReferenceDeclarations(t *testing.T) {
	f := newClaimTestFixture(t)
	rows, err := f.s.db.Query(`PRAGMA foreign_key_list(m6_gate_states)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]bool)
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err = rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "m6_gate_declarations" {
			columns[from+"="+to] = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"domain_id", "repo_id", "gate"} {
		if !columns[column+"="+column] {
			t.Fatalf("m6_gate_states missing declaration FK column %q: %v", column, columns)
		}
	}
}

func step13TestEvent(position int, kind, subject string, payload map[string]any) step13Event {
	fields := make(map[string]cbor.RawMessage, len(payload))
	for key, value := range payload {
		encoded, err := artifactEncoder.Marshal(value)
		if err != nil {
			panic(err)
		}
		fields[key] = encoded
	}
	event := step12Event{
		domain: domainA, id: claimTestID(300 + position), kind: kind, subject: subject,
		repo: repoA, position: uint64(position), payload: fields,
	}
	return step13Event{step12Event: event}
}

func step13FoldFixture() (map[string]step13Node, []step13Event, string, string, string, string) {
	exempt, closed, dismissed, ref := claimTestID(41), claimTestID(42), claimTestID(43), "TRACKER-17"
	nodes := make(map[string]step13Node)
	for _, id := range []string{exempt, closed, dismissed} {
		node := step12Node{domain: domainA, id: id, kind: "matter", repo: repoA, matter: id, birth: claimTestID(301), last: claimTestID(301)}
		nodes[ownerKey(domainA, id)] = step13Node{node: node, birthPos: 1}
	}
	var events []step13Event
	add := func(position int, kind, subject string, payload map[string]any) {
		events = append(events, step13TestEvent(position, kind, subject, payload))
	}
	add(2, "matter.started", exempt, map[string]any{"from": "planned", "to": "in-progress"})
	add(3, "matter.finished", exempt, map[string]any{"from": "in-progress", "to": "done"})
	add(4, "gate.declared", repoA, map[string]any{"gate": "reviewed", "scale": "matter", "exempt": []string{exempt}})
	add(5, "config.set", repoA, map[string]any{"key": "tracker.push-level", "value": "boundary"})
	add(6, "config.set", repoA, map[string]any{"key": "tracker.push-level", "value": "off"})
	add(7, "config.set", repoA, map[string]any{"key": "tracker.push-level", "value": "off"})
	add(8, "reference.added", exempt, map[string]any{"ref": ref})
	add(9, "reference.added", closed, map[string]any{"ref": ref})
	add(10, "reference.added", dismissed, map[string]any{"ref": ref})
	add(11, "matter.started", closed, map[string]any{"from": "planned", "to": "in-progress", "tracker_push_level": "boundary"})
	add(12, "matter.finished", closed, map[string]any{"from": "in-progress", "to": "done"})
	add(13, "gate.closed", closed, map[string]any{"gate": "reviewed", "scale": "matter", "tracker_push_level": "boundary"})
	add(14, "matter.started", dismissed, map[string]any{"from": "planned", "to": "in-progress", "tracker_push_level": "off"})
	add(15, "matter.finished", dismissed, map[string]any{"from": "in-progress", "to": "done"})
	add(16, "gate.dismissed", dismissed, map[string]any{"gate": "reviewed", "scale": "matter", "reason": "owner exception", "tracker_push_level": "narrated"})
	return nodes, events, exempt, closed, dismissed, ref
}

func TestStep13FoldPreservesDeclarationSnapshotDistinctStatesAndCandidates(t *testing.T) {
	nodes, events, exempt, closed, dismissed, ref := step13FoldFixture()
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		t.Fatalf("derive event fold: %v", err)
	}
	rebuilt, err := deriveStep13Projection(nodes, events)
	if err != nil || !sameStep13Projection(projection, rebuilt) {
		t.Fatalf("event fold is not deterministic: err=%v equal=%t", err, sameStep13Projection(projection, rebuilt))
	}
	if len(projection.config) != 1 || projection.config[0].value != "off" || projection.config[0].event != claimTestID(307) {
		t.Fatalf("Repo config did not retain last-write-wins value/source: %+v", projection.config)
	}
	if len(projection.declarations) != 1 || projection.declarations[0].event != claimTestID(304) {
		t.Fatalf("declaration projection: %+v", projection.declarations)
	}
	states := make(map[string]step13GateState)
	for _, state := range projection.gateStates {
		states[state.node] = state
	}
	if len(states) != 3 || states[exempt].state != "exempt" || states[exempt].event != claimTestID(304) ||
		states[closed].state != "closed" || states[closed].reason != "" ||
		states[dismissed].state != "dismissed" || states[dismissed].reason != "owner exception" {
		t.Fatalf("gate states lost snapshot/close/dismiss distinctions: %+v", states)
	}
	if len(projection.references) != 3 || len(projection.aggregates) != 1 || projection.aggregates[0].ref != ref ||
		projection.aggregates[0].disposition != "completed" || projection.aggregates[0].members != 3 {
		t.Fatalf("shared tracker projection: references=%+v aggregates=%+v", projection.references, projection.aggregates)
	}
	wantCandidates := map[string]string{
		"tracker:" + claimTestID(311) + ":state:" + ref: `{"disposition":"active"}`,
		"tracker:" + claimTestID(313) + ":state:" + ref: `{"disposition":"active"}`,
		"tracker:" + claimTestID(316) + ":state:" + ref: `{"disposition":"completed"}`,
	}
	// Independent fixed vector: SHA-256(key), first 16 bytes, ULID Crockford encoding.
	wantCandidateIDs := map[string]string{
		"tracker:" + claimTestID(311) + ":state:" + ref: "4E2HDXN2R6H3D8JFCAKEZ50JNT",
	}
	if len(projection.candidates) != len(wantCandidates) {
		t.Fatalf("candidate count %d, want %d: %+v", len(projection.candidates), len(wantCandidates), projection.candidates)
	}
	for _, candidate := range projection.candidates {
		if want, ok := wantCandidates[candidate.key]; !ok || candidate.payload != want || len(candidate.id) != 26 {
			t.Fatalf("unexpected or nondeterministic candidate: %+v", candidate)
		}
		if wantID, fixed := wantCandidateIDs[candidate.key]; fixed && candidate.id != wantID {
			t.Fatalf("candidate ID %q, want fixed ULID vector %q", candidate.id, wantID)
		}
		delete(wantCandidates, candidate.key)
		delete(wantCandidateIDs, candidate.key)
	}
	if len(wantCandidates) != 0 || len(wantCandidateIDs) != 0 {
		t.Fatalf("missing candidate keys/IDs: keys=%v IDs=%v", wantCandidates, wantCandidateIDs)
	}

	badNodes, badEvents, _, _, _, _ := step13FoldFixture()
	for index := range badEvents {
		if badEvents[index].kind == "gate.declared" {
			badEvents[index] = step13TestEvent(4, "gate.declared", repoA,
				map[string]any{"gate": "reviewed", "scale": "matter", "exempt": []string{exempt, exempt}})
		}
	}
	if _, err = deriveStep13Projection(badNodes, badEvents); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("duplicate declaration snapshot accepted: %v", err)
	}
}

func TestStep13FoldAllowsDeclaredGateCloseAndFinishInEitherOrder(t *testing.T) {
	closedBeforeFinish, finishedBeforeClose := claimTestID(51), claimTestID(52)
	nodes := make(map[string]step13Node)
	for _, id := range []string{closedBeforeFinish, finishedBeforeClose} {
		node := step12Node{domain: domainA, id: id, kind: "matter", repo: repoA, matter: id, birth: claimTestID(301), last: claimTestID(301)}
		nodes[ownerKey(domainA, id)] = step13Node{node: node, birthPos: 1}
	}
	events := []step13Event{
		step13TestEvent(2, "gate.declared", repoA, map[string]any{"gate": "reviewed", "scale": "matter"}),
		step13TestEvent(3, "matter.started", closedBeforeFinish, map[string]any{"from": "planned", "to": "in-progress"}),
		step13TestEvent(4, "matter.started", finishedBeforeClose, map[string]any{"from": "planned", "to": "in-progress"}),
		step13TestEvent(5, "gate.closed", closedBeforeFinish, map[string]any{"gate": "reviewed", "scale": "matter"}),
		step13TestEvent(6, "matter.finished", finishedBeforeClose, map[string]any{"from": "in-progress", "to": "done"}),
		step13TestEvent(7, "gate.closed", finishedBeforeClose, map[string]any{"gate": "reviewed", "scale": "matter"}),
		step13TestEvent(8, "matter.finished", closedBeforeFinish, map[string]any{"from": "in-progress", "to": "done"}),
	}
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		t.Fatalf("declared gate close/finish ordering rejected: %v", err)
	}
	states := make(map[string]step13GateState)
	for _, state := range projection.gateStates {
		states[state.node] = state
	}
	if len(states) != 2 || states[closedBeforeFinish].state != "closed" || states[closedBeforeFinish].event != claimTestID(305) ||
		states[finishedBeforeClose].state != "closed" || states[finishedBeforeClose].event != claimTestID(307) {
		t.Fatalf("declared gate close states lost exact event evidence: %+v", states)
	}
}

func TestStep13FoldRejectsUndeclaredAndPreDeclarationClose(t *testing.T) {
	matter := claimTestID(55)
	node := step12Node{domain: domainA, id: matter, kind: "matter", repo: repoA, matter: matter, birth: claimTestID(301), last: claimTestID(301)}
	nodes := map[string]step13Node{ownerKey(domainA, matter): {node: node, birthPos: 1}}
	close := step13TestEvent(2, "gate.closed", matter, map[string]any{"gate": "reviewed", "scale": "matter"})
	t.Run("undeclared", func(t *testing.T) {
		if _, err := deriveStep13Projection(nodes, []step13Event{close}); !errors.Is(err, ErrInvalidStore) {
			t.Fatalf("undeclared normal close accepted: %v", err)
		}
	})
	t.Run("pre-declaration", func(t *testing.T) {
		declaration := step13TestEvent(3, "gate.declared", repoA, map[string]any{"gate": "reviewed", "scale": "matter"})
		if _, err := deriveStep13Projection(nodes, []step13Event{close, declaration}); !errors.Is(err, ErrInvalidStore) {
			t.Fatalf("pre-declaration close accepted: %v", err)
		}
	})
}

func TestStep13FoldRejectsPostTombstoneCloseAtEveryPushLevel(t *testing.T) {
	build := func(level string) (map[string]step13Node, []step13Event, string) {
		const matter = "00000000000000000000000061"
		node := step12Node{
			domain: domainA, id: matter, kind: "matter", repo: repoA, matter: matter,
			birth: claimTestID(302), last: claimTestID(306), tombstone: claimTestID(306),
		}
		nodes := map[string]step13Node{ownerKey(domainA, matter): {node: node, birthPos: 2, tombstonePos: 6}}
		closePayload := map[string]any{"gate": "reviewed", "scale": "matter"}
		if level != "" {
			closePayload["tracker_push_level"] = level
		}
		events := []step13Event{
			step13TestEvent(1, "gate.declared", repoA, map[string]any{"gate": "reviewed", "scale": "matter"}),
			step13TestEvent(2, "matter.created", matter, map[string]any{}),
			step13TestEvent(3, "matter.started", matter, map[string]any{"from": "planned", "to": "in-progress"}),
			step13TestEvent(4, "matter.finished", matter, map[string]any{"from": "in-progress", "to": "done"}),
			step13TestEvent(5, "reference.added", matter, map[string]any{"ref": "TRACKER-TOMBSTONE"}),
			step13TestEvent(6, "step.removed", matter, map[string]any{"reason": "folded into another Matter"}),
			step13TestEvent(7, "gate.closed", matter, closePayload),
		}
		return nodes, events, matter
	}

	for _, level := range []string{"", "off", "boundary", "narrated"} {
		t.Run("push-level-"+level, func(t *testing.T) {
			nodes, events, _ := build(level)
			if _, err := deriveStep13Projection(nodes, events); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("post-tombstone normal close accepted at level %q: %v", level, err)
			}
		})
	}

	nodes, events, matter := build("")
	dismissal := step13TestEvent(7, "gate.dismissed", matter, map[string]any{
		"gate": "reviewed", "scale": "matter", "reason": "owner exception",
	})
	events[len(events)-1] = dismissal
	if _, err := deriveStep13Projection(nodes, events); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("post-tombstone dismissal bypassed its liveness guard: %v", err)
	}
}

func TestStep13FoldRejectsCloseAfterSnapshotExemption(t *testing.T) {
	matter := claimTestID(56)
	node := step12Node{domain: domainA, id: matter, kind: "matter", repo: repoA, matter: matter, birth: claimTestID(301), last: claimTestID(301)}
	nodes := map[string]step13Node{ownerKey(domainA, matter): {node: node, birthPos: 1}}
	events := []step13Event{
		step13TestEvent(2, "gate.declared", repoA, map[string]any{
			"gate": "reviewed", "scale": "matter", "exempt": []string{matter},
		}),
		step13TestEvent(3, "gate.closed", matter, map[string]any{"gate": "reviewed", "scale": "matter"}),
	}
	if _, err := deriveStep13Projection(nodes, events); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("close after snapshot exemption accepted: %v", err)
	}
}

func TestStep13ReferenceReboundCarriesLevelAndProjectsBothCandidates(t *testing.T) {
	nodes, events, _, _, dismissed, from := step13FoldFixture()
	const to = "TRACKER-18"
	events = append(events, step13TestEvent(17, "reference.rebound", dismissed, map[string]any{
		"from": from, "to": to, "tracker_push_level": "narrated",
	}))
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		t.Fatalf("valid narrated reference.rebound rejected: %v", err)
	}
	references := make(map[string]step13Reference)
	for _, reference := range projection.references {
		references[ownerKey(reference.domain, reference.matter)+"/"+reference.ref] = reference
	}
	oldReference := references[ownerKey(domainA, dismissed)+"/"+from]
	newReference := references[ownerKey(domainA, dismissed)+"/"+to]
	if oldReference.removed != claimTestID(317) || oldReference.last != claimTestID(317) ||
		newReference.removed != "" || newReference.birth != claimTestID(317) || newReference.last != claimTestID(317) {
		t.Fatalf("rebound reference history was not retained: old=%+v new=%+v", oldReference, newReference)
	}
	aggregates := make(map[string]step13AggregateRow)
	for _, aggregate := range projection.aggregates {
		aggregates[aggregate.ref] = aggregate
	}
	if aggregates[from].disposition != "completed" || aggregates[from].members != 2 ||
		aggregates[to].disposition != "completed" || aggregates[to].members != 1 {
		t.Fatalf("rebound did not split the shared tracker aggregate: %+v", aggregates)
	}
	want := map[string]string{
		"tracker:" + claimTestID(317) + ":state:" + from: `{"disposition":"completed"}`,
		"tracker:" + claimTestID(317) + ":state:" + to:   `{"disposition":"completed"}`,
	}
	for _, candidate := range projection.candidates {
		if payload, ok := want[candidate.key]; ok {
			if candidate.payload != payload || candidate.event != claimTestID(317) {
				t.Fatalf("wrong rebound candidate: %+v", candidate)
			}
			delete(want, candidate.key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("rebound did not queue candidates for both references: %v", want)
	}

	badNodes, badEvents, _, _, badDismissed, badFrom := step13FoldFixture()
	badEvents = append(badEvents, step13TestEvent(17, "reference.rebound", badDismissed, map[string]any{
		"from": badFrom, "to": to, "tracker_push_level": "",
	}))
	if _, err = deriveStep13Projection(badNodes, badEvents); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("reference.rebound accepted explicit empty tracker_push_level: %v", err)
	}
}

func TestStep13FoldRejectsExplicitZeroOptionalFields(t *testing.T) {
	tests := []struct {
		name, kind, key string
		value           any
	}{
		{name: "empty declaration snapshot", kind: "gate.declared", key: "exempt", value: []string{}},
		{name: "empty tracker push level", kind: "gate.closed", key: "tracker_push_level", value: ""},
		{name: "false cascade", kind: "matter.started", key: "cascade", value: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nodes, events, _, _, _, _ := step13FoldFixture()
			if test.key == "cascade" {
				for i := range events {
					if events[i].kind == test.kind {
						encoded, err := artifactEncoder.Marshal(true)
						if err != nil {
							t.Fatal(err)
						}
						events[i].payload[test.key] = encoded
						break
					}
				}
				if _, err := deriveStep13Projection(nodes, events); err != nil {
					t.Fatalf("nonzero cascade=true rejected: %v", err)
				}
			}
			encoded, err := artifactEncoder.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for i := range events {
				if events[i].kind == test.kind {
					events[i].payload[test.key] = encoded
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("fixture has no %s event", test.kind)
			}
			if _, err = deriveStep13Projection(nodes, events); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("explicit zero optional field %s accepted: %v", test.key, err)
			}
		})
	}
}

func TestStep13ConfigSetRejectsUndefinedValueAndAcceptsEmptyText(t *testing.T) {
	undefinedNodes, undefinedEvents, _, _, _, _ := step13FoldFixture()
	undefined := step13TestEvent(17, "config.set", repoA, map[string]any{
		"key": "tracker.push-level", "value": "",
	})
	undefined.payload["value"] = cbor.RawMessage{0xf7}
	undefinedEvents = append(undefinedEvents, undefined)
	if _, err := deriveStep13Projection(undefinedNodes, undefinedEvents); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("config.set payload with undefined value accepted: %v", err)
	}

	emptyNodes, emptyEvents, _, _, _, _ := step13FoldFixture()
	empty := step13TestEvent(17, "config.set", repoA, map[string]any{
		"key": "tracker.push-level", "value": "",
	})
	if value := empty.payload["value"]; len(value) != 1 || value[0] != 0x60 {
		t.Fatalf("config.set empty value is not CBOR empty text: %x", value)
	}
	emptyEvents = append(emptyEvents, empty)
	projection, err := deriveStep13Projection(emptyNodes, emptyEvents)
	if err != nil {
		t.Fatalf("config.set with an empty text value rejected: %v", err)
	}
	if len(projection.config) != 1 || projection.config[0].key != "tracker.push-level" ||
		projection.config[0].value != "" || projection.config[0].event != claimTestID(317) {
		t.Fatalf("empty config.set value/source not projected exactly: %+v", projection.config)
	}
}

func TestStep13CancelTransitionOmitsEmptyReason(t *testing.T) {
	nodes, events, _, _, _, _ := step13FoldFixture()
	const canceled = "00000000000000000000000044"
	node := step12Node{domain: domainA, id: canceled, kind: "matter", repo: repoA, matter: canceled, birth: claimTestID(301), last: claimTestID(301)}
	nodes[ownerKey(domainA, canceled)] = step13Node{node: node, birthPos: 1}
	events = append(events,
		step13TestEvent(17, "matter.started", canceled, map[string]any{"from": "planned", "to": "in-progress"}),
		step13TestEvent(18, "matter.canceled", canceled, map[string]any{"from": "in-progress", "to": "canceled", "reason": "scope changed"}),
	)
	if _, err := deriveStep13Projection(nodes, events); err != nil {
		t.Fatalf("nonempty cancel reason rejected: %v", err)
	}
	emptyReason, err := artifactEncoder.Marshal("")
	if err != nil {
		t.Fatal(err)
	}
	events[len(events)-1].payload["reason"] = emptyReason
	if _, err = deriveStep13Projection(nodes, events); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("cancel transition accepted explicit empty reason: %v", err)
	}
}

func TestStep13RebuildIsIdempotentAndOpenRejectsDivergentProjection(t *testing.T) {
	f := newClaimTestFixture(t)
	otherDomain, _ := identity(domainB, 8)
	if err := f.s.BootstrapDomain(context.Background(), otherDomain, repoB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`INSERT INTO m6_tracker_aggregates(domain_id,ref,disposition,member_count) VALUES(?,?,?,?)`, domainA, "TRACKER-INVALID", "open", 1); err == nil {
		t.Fatal("schema accepted malformed tracker disposition")
	}
	if _, err := f.s.db.Exec(`INSERT INTO m6_repo_config(domain_id,repo_id,config_key,value,last_event_id) VALUES(?,?,?,?,?)`,
		domainA, repoB, "wrong-domain", "wrong", claimTestID(100)); err == nil {
		t.Fatal("schema accepted a Repo config row from another domain")
	}
	if _, err := f.s.db.Exec(`INSERT INTO m6_repo_config(domain_id,repo_id,config_key,value,last_event_id) VALUES(?,?,?,?,?)`,
		domainA, repoA, "divergent", "wrong", claimTestID(100)); err != nil {
		t.Fatal(err)
	}
	if err := checkStep13State(f.s.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("strict projection check accepted divergent Repo config: %v", err)
	}
	if err := rebuildStep13Projection(f.s.db); err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	first, err := readStep13Projection(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	if err = rebuildStep13Projection(f.s.db); err != nil {
		t.Fatalf("repeat projection rebuild: %v", err)
	}
	second, err := readStep13Projection(f.s.db)
	if err != nil || !sameStep13Projection(first, second) || len(second.config) != 0 || len(second.gateStates) != 0 ||
		len(second.references) != 0 || len(second.aggregates) != 0 || len(second.candidates) != 0 {
		t.Fatalf("event-derived projection rebuild changed across runs: first=%+v second=%+v err=%v", first, second, err)
	}
	if err = checkStep13State(f.s.db); err != nil {
		t.Fatalf("rebuilt projection does not validate: %v", err)
	}
	if _, err = f.s.db.Exec(`INSERT INTO m6_repo_config(domain_id,repo_id,config_key,value,last_event_id) VALUES(?,?,?,?,?)`,
		domainA, repoA, "divergent", "wrong", claimTestID(100)); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("reopen accepted divergent projection: %v", err)
	}
}
