package authoritystore

import (
	"database/sql"
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
)

func checkStep13GateCommands(db *sql.DB) error {
	rows, err := db.Query(`SELECT domain_id,command_id,request_hash,command,operation_name,operation_version,state
		FROM submissions WHERE operation_name IN ('gate.declare','gate.close','gate.dismiss')`)
	if err != nil {
		return err
	}
	type submission struct {
		domain, id, hash, name, state string
		command                       []byte
		version                       uint64
	}
	var submissions []submission
	for rows.Next() {
		var value submission
		if err = rows.Scan(&value.domain, &value.id, &value.hash, &value.command, &value.name, &value.version, &value.state); err != nil {
			_ = rows.Close()
			return err
		}
		submissions = append(submissions, value)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, stored := range submissions {
		command, decodeErr := operation.DecodeCanonicalCommand(stored.command)
		definition, known := gateDefinition(command.Request.Operation)
		if decodeErr != nil || !known || stored.version != 1 || stored.name != command.Request.Operation.Name ||
			operation.VerifyRequestHash(command, stored.hash) != nil || definition.ValidateRequest(command.Request) != nil {
			return fmt.Errorf("%w: gate command %s identity is invalid", ErrInvalidStore, stored.id)
		}
		var events []step12Event
		eventRows, queryErr := db.Query(`SELECT position,event_id,record FROM authority_events WHERE domain_id=? AND command_id=? ORDER BY position`, stored.domain, stored.id)
		if queryErr != nil {
			return queryErr
		}
		for eventRows.Next() {
			var position uint64
			var id string
			var raw []byte
			if err = eventRows.Scan(&position, &id, &raw); err != nil {
				_ = eventRows.Close()
				return err
			}
			event, parseErr := parseStep12Event(raw, stored.domain, position, id, stored.id)
			if parseErr != nil {
				_ = eventRows.Close()
				return parseErr
			}
			events = append(events, event)
		}
		queryErr = eventRows.Err()
		_ = eventRows.Close()
		if queryErr != nil {
			return queryErr
		}
		if stored.state != "terminal" {
			if len(events) != 0 {
				return fmt.Errorf("%w: pending gate command %s has effects", ErrInvalidStore, stored.id)
			}
			continue
		}
		var receiptBytes []byte
		if err = db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, stored.domain, stored.id).Scan(&receiptBytes); err != nil {
			return err
		}
		receipt, receiptErr := readReceipt(receiptBytes)
		if receiptErr != nil || receipt.ID != stored.id || receipt.Hash != stored.hash || receipt.Operation.Name != command.Request.Operation.Name || receipt.Operation.Version != uint64(command.Request.Operation.Version) ||
			receipt.Environment.ID != command.EnvironmentID || receipt.Environment.Sequence != command.EnvironmentSequence {
			return fmt.Errorf("%w: gate command %s receipt identity mismatch", ErrInvalidStore, stored.id)
		}
		if receipt.Result.Code != string(operation.ResultSucceeded) {
			if len(events) != 0 || receipt.Range != nil || len(receipt.Result.Output) != 0 {
				return fmt.Errorf("%w: refused gate command %s carries effects", ErrInvalidStore, stored.id)
			}
			continue
		}
		if err = validateGateCommandEffects(db, command, events, receipt); err != nil {
			return fmt.Errorf("%w: gate command %s effects: %v", ErrInvalidStore, stored.id, err)
		}
	}
	return nil
}

func validateGateCommandEffects(db *sql.DB, command operation.Command, events []step12Event, receipt receiptRecord) error {
	expectOutput := func(value any) error {
		encoded, err := artifactEncoder.Marshal(value)
		if err != nil || string(encoded) != string(receipt.Result.Output) {
			return ErrInvalidStore
		}
		return nil
	}
	expectRange := func() error {
		if len(events) != 1 || receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != events[0].id || receipt.Range.Last != events[0].id {
			return ErrInvalidStore
		}
		return nil
	}
	if len(events) > 1 {
		return ErrInvalidStore
	}
	switch input := command.Request.Input.(type) {
	case operation.GateDeclareInput:
		if err := expectOutput(map[string]any{"gate": input.Gate, "scale": input.Scale}); err != nil {
			return err
		}
		if len(events) == 0 {
			if receipt.Range != nil {
				return ErrInvalidStore
			}
			var scale string
			if err := db.QueryRow(`SELECT scale FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate=?`, command.AuthorityDomainID, command.Request.Context.Repo, input.Gate).Scan(&scale); err != nil || scale != input.Scale {
				return ErrInvalidStore
			}
			return nil
		}
		event := events[0]
		if event.kind != "gate.declared" || event.subject != command.Request.Context.Repo || event.repo != command.Request.Context.Repo ||
			!step13ClosedPayload(event.payload, new(struct {
				Gate   string   `cbor:"gate"`
				Scale  string   `cbor:"scale"`
				Exempt []string `cbor:"exempt"`
			}), []string{"gate", "scale"}, []string{"exempt"}) {
			return ErrInvalidStore
		}
		var payload struct {
			Gate  string `cbor:"gate"`
			Scale string `cbor:"scale"`
		}
		if artifactDecoder.Unmarshal(event.payload["gate"], &payload.Gate) != nil || artifactDecoder.Unmarshal(event.payload["scale"], &payload.Scale) != nil ||
			payload.Gate != input.Gate || payload.Scale != input.Scale {
			return ErrInvalidStore
		}
		if string(command.Request.Actor) != "human" {
			return ErrInvalidStore
		}
		return expectRange()
	case operation.GateCloseInput:
		if len(events) != 1 || events[0].kind != "gate.closed" || events[0].subject != input.NodeID || events[0].repo != command.Request.Context.Repo {
			return ErrInvalidStore
		}
		var payload struct {
			Gate             string `cbor:"gate"`
			Scale            string `cbor:"scale"`
			TrackerPushLevel string `cbor:"tracker_push_level"`
		}
		if !step13ClosedPayload(events[0].payload, &payload, []string{"gate", "scale", "tracker_push_level"}) || payload.Gate != input.Gate ||
			!step13Scale(payload.Scale) || !step13PushLevel(payload.TrackerPushLevel) || !gateEventActorAllowed(string(command.Request.Actor), input.Gate, false) {
			return ErrInvalidStore
		}
		if err := expectOutput(map[string]any{"gate": input.Gate, "node_id": input.NodeID, "scale": payload.Scale}); err != nil {
			return err
		}
		return expectRange()
	case operation.GateDismissInput:
		if len(events) != 1 || events[0].kind != "gate.dismissed" || events[0].subject != input.NodeID || events[0].repo != command.Request.Context.Repo {
			return ErrInvalidStore
		}
		var payload struct {
			Gate             string `cbor:"gate"`
			Scale            string `cbor:"scale"`
			Reason           string `cbor:"reason"`
			TrackerPushLevel string `cbor:"tracker_push_level"`
		}
		if !step13ClosedPayload(events[0].payload, &payload, []string{"gate", "scale", "reason", "tracker_push_level"}) || payload.Gate != input.Gate ||
			payload.Reason != input.Reason || !step13Scale(payload.Scale) || !step13PushLevel(payload.TrackerPushLevel) ||
			!gateEventActorAllowed(string(command.Request.Actor), input.Gate, true) {
			return ErrInvalidStore
		}
		if err := expectOutput(map[string]any{"gate": input.Gate, "node_id": input.NodeID, "scale": payload.Scale}); err != nil {
			return err
		}
		return expectRange()
	default:
		return ErrInvalidStore
	}
}

func gateEventActorAllowed(actor, gate string, dismissal bool) bool {
	owner := map[string]string{"verified": "verifier", "reviewed": "warden", "ci-green": "warden"}[gate]
	if dismissal {
		return actor == "human"
	}
	return actor == "human" && owner == "" || owner != "" && actor == "role:"+owner
}
