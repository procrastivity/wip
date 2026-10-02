package wipdjournal

import (
	"database/sql"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func repairClaimJournalMember(entry Entry) bool {
	return entry.Delivery == operation.DeliveryClaim ||
		entry.Delivery == operation.DeliveryAuthority && entry.Command.Request.Operation == operation.GateExemptionRepairV1.Metadata().Operation
}

func repairOutput(entry Entry, raw []byte) (operation.GateExemptionRepairOutput, bool) {
	input, ok := entry.Command.Request.Input.(operation.GateExemptionRepairInput)
	if !ok || entry.Command.Request.Operation != operation.GateExemptionRepairV1.Metadata().Operation {
		return operation.GateExemptionRepairOutput{}, false
	}
	fields, err := wipdwire.DecodeCanonicalMap(raw, "gate", "node_id", "already_exempt")
	gate, gateOK := fields["gate"].(string)
	node, nodeOK := fields["node_id"].(string)
	already, alreadyOK := fields["already_exempt"].(bool)
	return operation.GateExemptionRepairOutput{Gate: gate, NodeID: node, AlreadyExempt: already},
		err == nil && gateOK && nodeOK && alreadyOK && gate == input.Gate && node == input.NodeID
}

func repairReceiptNoEvent(entry Entry, raw []byte) bool {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["accepted_events"] != nil {
		return false
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") || result["code"] != string(operation.ResultSucceeded) || result["problem_code"] != nil {
		return false
	}
	output, ok := result["output"].([]byte)
	if !ok {
		return false
	}
	decoded, valid := repairOutput(entry, output)
	return valid && decoded.AlreadyExempt
}

func installedRepairReceiptRange(tx *sql.Tx, installed wipdwire.PrefixAnchor, entry Entry, receipt InstalledReceipt) (*wipdwire.JournalBarrierRange, error) {
	fields, err := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return nil, ErrInvalidJournal
	}
	accepted, ok := fields["accepted_events"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") || accepted["event_count"] != uint64(1) || accepted["first_event_id"] != accepted["last_event_id"] {
		return nil, ErrInvalidJournal
	}
	result, ok := fields["result"].(map[string]any)
	if !ok {
		return nil, ErrInvalidJournal
	}
	output, ok := result["output"].([]byte)
	if !ok {
		return nil, ErrInvalidJournal
	}
	decoded, valid := repairOutput(entry, output)
	if !valid || decoded.AlreadyExempt {
		return nil, ErrInvalidJournal
	}
	var id string
	var record []byte
	var position int64
	if err = tx.QueryRow(`SELECT event_id,record,position FROM installed_events WHERE event_id=?`, accepted["first_event_id"]).Scan(&id, &record, &position); err != nil || position <= 0 || uint64(position) > installed.EventCount ||
		!repairEventMatches(record, entry, id) {
		return nil, ErrInvalidJournal
	}
	return &wipdwire.JournalBarrierRange{First: id, Last: id, Count: 1}, nil
}

func repairEventMatches(record []byte, entry Entry, id string) bool {
	input, ok := entry.Command.Request.Input.(operation.GateExemptionRepairInput)
	if !ok || !eventMatchesCommand(record, entry) {
		return false
	}
	fields, err := wipdwire.DecodeCanonicalMap(record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["event_id"] != id || fields["kind"] != "gate.exemption-repaired" || fields["subject_id"] != input.NodeID || fields["repo_id"] != entry.Command.Request.Context.Repo {
		return false
	}
	payload, ok := fields["payload"].(map[string]any)
	return ok && wipdwire.ExactMapKeys(payload, "gate") && payload["gate"] == input.Gate
}
