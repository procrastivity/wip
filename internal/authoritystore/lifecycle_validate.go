package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type expectedLifecycleEvent struct {
	kind, subject string
	payload       map[string]any
}

func checkM6LifecycleEvents(db *sql.DB, c *lifecycleCommand, receipt receiptRecord, events []lifecycleEvent) error {
	return checkM6LifecycleEventsForSchema(db, c, receipt, events, true)
}

func checkM6LifecycleEventsForSchema(db *sql.DB, c *lifecycleCommand, receipt receiptRecord, events []lifecycleEvent, step13 bool) error {
	if receipt.Range == nil || len(events) == 0 || uint64(len(events)) != receipt.Range.Count ||
		events[0].id != receipt.Range.First || events[len(events)-1].id != receipt.Range.Last {
		return fmt.Errorf("lifecycle receipt range mismatch: %w", ErrInvalidStore)
	}
	target := c.nodeID
	if c.stepID != "" {
		target = c.stepID
	}
	if target == "" {
		target = c.matter
	}
	var kind, matter string
	if err := db.QueryRow(`SELECT kind,matter_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, c.domain, target).
		Scan(&kind, &matter); err != nil || matter != c.matter || kind != strings.SplitN(c.name, ".", 2)[0] {
		return fmt.Errorf("lifecycle target projection mismatch: %w", ErrInvalidStore)
	}
	if err := lifecycleNodeLiveForRange(db, c.domain, target, events[0].position, events[len(events)-1].position); err != nil {
		return err
	}
	verb := strings.SplitN(c.name, ".", 2)[1]
	from, to := map[string]string{"start": "planned", "finish": "in-progress", "pause": "in-progress", "cancel": "in-progress", "resume": "paused"}[verb],
		map[string]string{"start": "in-progress", "finish": "done", "pause": "paused", "cancel": "canceled", "resume": "in-progress"}[verb]
	state, err := lifecycleStateBefore(db, c.domain, target, kind, events[0].position)
	if err != nil || state != from {
		return fmt.Errorf("lifecycle target pre-state %q want %q: %w", state, from, ErrInvalidStore)
	}
	if c.name == "matter.finish" {
		return checkMatterFinishHistory(db, c, receipt, events, step13)
	}
	want := make([]expectedLifecycleEvent, 0, len(events))
	if verb == "start" {
		type node struct{ id, kind string }
		var planned []node
		current := target
		for {
			var currentKind string
			var currentParent sql.NullString
			if err = db.QueryRow(`SELECT kind,parent_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, c.domain, current).Scan(&currentKind, &currentParent); err != nil {
				return ErrInvalidStore
			}
			if err = lifecycleNodeLiveForRange(db, c.domain, current, events[0].position, events[len(events)-1].position); err != nil {
				return err
			}
			if current != target {
				ancestorState, stateErr := lifecycleStateBefore(db, c.domain, current, currentKind, events[0].position)
				if stateErr != nil {
					return stateErr
				}
				if ancestorState == "planned" {
					planned = append(planned, node{current, currentKind})
				}
			}
			if !currentParent.Valid {
				break
			}
			current = currentParent.String
		}
		for left, right := 0, len(planned)-1; left < right; left, right = left+1, right-1 {
			planned[left], planned[right] = planned[right], planned[left]
		}
		planned = append(planned, node{target, kind})
		var cause string
		for _, item := range planned {
			payload := map[string]any{"from": "planned", "to": "in-progress"}
			if item.id != target {
				payload["cascade"] = true
			}
			if cause != "" {
				payload["cause_event_id"] = cause
			}
			want = append(want, expectedLifecycleEvent{item.kind + ".started", item.id, payload})
			if len(want) <= len(events) {
				cause = events[len(want)-1].id
			}
		}
	} else {
		payload := map[string]any{"from": from, "to": to}
		if verb == "cancel" && c.reason != "" {
			payload["reason"] = c.reason
		}
		kindName := map[string]string{"finish": "finished", "cancel": "canceled", "pause": "paused", "resume": "resumed"}[verb]
		want = append(want, expectedLifecycleEvent{kind + "." + kindName, target, payload})
	}
	if len(want) != len(events) {
		return fmt.Errorf("lifecycle expected %d events, got %d: %w", len(want), len(events), ErrInvalidStore)
	}
	var output map[string]any
	switch c.name {
	case "step.start", "step.finish", "step.pause", "step.resume", "step.cancel":
		output = map[string]any{"step_id": target, "matter_id": matter, "state": to}
	case "stage.start", "stage.finish", "stage.pause", "stage.resume", "stage.cancel":
		output = map[string]any{"node_id": target, "matter_id": matter, "state": to}
	default:
		output = map[string]any{"matter_id": matter, "state": to}
	}
	encoded, err := artifactEncoder.Marshal(output)
	if err != nil || string(encoded) != string(receipt.Result.Output) {
		return fmt.Errorf("lifecycle receipt output mismatch: %w", ErrInvalidStore)
	}
	for index, expected := range want {
		item := events[index]
		if index > 0 && item.position != events[index-1].position+1 {
			return fmt.Errorf("lifecycle event %d identity mismatch: %w", index, ErrInvalidStore)
		}
		var event struct {
			Kind    string         `cbor:"kind"`
			Subject string         `cbor:"subject_id"`
			Payload map[string]any `cbor:"payload"`
		}
		if artifactDecoder.Unmarshal(item.raw, &event) != nil || event.Kind != expected.kind || event.Subject != expected.subject {
			return fmt.Errorf("lifecycle event %d payload mismatch got=%v want=%v: %w", index, event.Payload, expected.payload, ErrInvalidStore)
		}
		got, marshalErr := artifactEncoder.Marshal(event.Payload)
		wantBytes, wantErr := artifactEncoder.Marshal(expected.payload)
		if marshalErr != nil || wantErr != nil || string(got) != string(wantBytes) {
			return ErrInvalidStore
		}
	}
	return nil
}

func checkMatterFinishHistory(db *sql.DB, c *lifecycleCommand, receipt receiptRecord, events []lifecycleEvent, step13 bool) error {
	legacySealed, err := matterSubtreeCompleteBefore(db, c.domain, c.matter, events[0].position)
	if err != nil {
		return err
	}
	var sealed bool
	if step13 {
		nodes, nodesErr := step13Nodes(db)
		if nodesErr != nil {
			return nodesErr
		}
		sealed, err = step13ProjectedSealed(context.Background(), db, nodes, c.domain, c.matter, events[0].position)
		if err != nil {
			return err
		}
	}
	var batch string
	batchErr := db.QueryRow(`SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&batch)
	if batchErr != nil && !errors.Is(batchErr, sql.ErrNoRows) {
		return batchErr
	}

	type expectation struct {
		sealed bool
		swept  bool
	}
	legacy := expectation{sealed: legacySealed, swept: legacySealed && !errors.Is(batchErr, sql.ErrNoRows)}
	expectations := []expectation{legacy}
	if step13 {
		expectations = append([]expectation{{sealed: sealed}}, expectations...)
	}
	for _, expected := range expectations {
		if !matterFinishHistoryMatches(receipt, events, c.matter, batch, expected) {
			continue
		}
		return nil
	}
	return fmt.Errorf("matter finish history matches neither current nor legacy contract: %w", ErrInvalidStore)
}

func matterFinishHistoryMatches(receipt receiptRecord, events []lifecycleEvent, matter, batch string, expected struct {
	sealed bool
	swept  bool
},
) bool {
	expectedCount := 1
	if expected.swept {
		expectedCount++
	}
	if len(events) != expectedCount {
		return false
	}
	output, err := artifactEncoder.Marshal(map[string]any{
		"matter_id": matter, "state": "done", "became_sealed": expected.sealed,
	})
	if err != nil || !bytes.Equal(output, receipt.Result.Output) {
		return false
	}
	want := []expectedLifecycleEvent{{"matter.finished", matter, map[string]any{"from": "in-progress", "to": "done"}}}
	if expected.swept {
		want = append(want, expectedLifecycleEvent{"batch.swept", batch, map[string]any{}})
	}
	for index, event := range events {
		if index > 0 && event.position != events[index-1].position+1 {
			return false
		}
		var got struct {
			Kind    string         `cbor:"kind"`
			Subject string         `cbor:"subject_id"`
			Payload map[string]any `cbor:"payload"`
		}
		if artifactDecoder.Unmarshal(event.raw, &got) != nil || got.Kind != want[index].kind || got.Subject != want[index].subject {
			return false
		}
		gotPayload, gotErr := artifactEncoder.Marshal(got.Payload)
		wantPayload, wantErr := artifactEncoder.Marshal(want[index].payload)
		if gotErr != nil || wantErr != nil || !bytes.Equal(gotPayload, wantPayload) {
			return false
		}
	}
	return true
}

var errNodeNotLiveAtLifecycle = errors.New("authoritystore: node not live at lifecycle event")

func lifecycleNodeLiveForRange(db *sql.DB, domain, node string, first, last uint64) error {
	var birth string
	var tombstone sql.NullString
	if err := db.QueryRow(`SELECT birth_event_id,tombstone_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, domain, node).
		Scan(&birth, &tombstone); err != nil {
		return fmt.Errorf("lifecycle node membership: %w", ErrInvalidStore)
	}
	var birthPosition uint64
	if err := db.QueryRow(`SELECT position FROM authority_events WHERE domain_id=? AND event_id=?`, domain, birth).Scan(&birthPosition); err != nil || birthPosition > first {
		return fmt.Errorf("lifecycle node birth position: %w", ErrInvalidStore)
	}
	if tombstone.Valid {
		var tombstonePosition uint64
		if err := db.QueryRow(`SELECT position FROM authority_events WHERE domain_id=? AND event_id=?`, domain, tombstone.String).Scan(&tombstonePosition); err != nil {
			return fmt.Errorf("lifecycle node tombstone position: %w", ErrInvalidStore)
		}
		if tombstonePosition <= last {
			return errNodeNotLiveAtLifecycle
		}
	}
	return nil
}
