package authoritystore

import (
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
	var kind, matter, parent string
	var tombstone sql.NullString
	if err := db.QueryRow(`SELECT kind,matter_id,coalesce(parent_id,''),tombstone_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, c.domain, target).
		Scan(&kind, &matter, &parent, &tombstone); err != nil || tombstone.Valid || matter != c.matter || kind != strings.SplitN(c.name, ".", 2)[0] {
		return fmt.Errorf("lifecycle target projection mismatch: %w", ErrInvalidStore)
	}
	verb := strings.SplitN(c.name, ".", 2)[1]
	from, to := map[string]string{"start": "planned", "finish": "in-progress", "pause": "in-progress", "cancel": "in-progress", "resume": "paused"}[verb],
		map[string]string{"start": "in-progress", "finish": "done", "pause": "paused", "cancel": "canceled", "resume": "in-progress"}[verb]
	state, err := lifecycleStateBefore(db, c.domain, target, kind, events[0].position)
	if err != nil || state != from {
		return fmt.Errorf("lifecycle target pre-state %q want %q: %w", state, from, ErrInvalidStore)
	}
	want := make([]expectedLifecycleEvent, 0, len(events))
	if verb == "start" {
		type node struct{ id, kind string }
		var planned []node
		current := target
		for {
			var currentKind string
			var currentParent sql.NullString
			if err = db.QueryRow(`SELECT kind,parent_id FROM m6_nodes WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`, c.domain, current).Scan(&currentKind, &currentParent); err != nil {
				return ErrInvalidStore
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
	} else if c.name == "matter.finish" {
		sealed, sealErr := matterSubtreeCompleteBefore(db, c.domain, c.matter, events[0].position)
		if sealErr != nil {
			return sealErr
		}
		want = append(want, expectedLifecycleEvent{"matter.finished", target, map[string]any{"from": "in-progress", "to": "done"}})
		var batch string
		batchErr := db.QueryRow(`SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&batch)
		if batchErr != nil && !errors.Is(batchErr, sql.ErrNoRows) {
			return batchErr
		}
		if sealed && !errors.Is(batchErr, sql.ErrNoRows) {
			want = append(want, expectedLifecycleEvent{"batch.swept", batch, map[string]any{}})
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
	case "matter.finish":
		sealed, err := matterSubtreeCompleteBefore(db, c.domain, c.matter, events[0].position)
		if err != nil {
			return err
		}
		output = map[string]any{"matter_id": c.matter, "state": "done", "became_sealed": sealed}
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
