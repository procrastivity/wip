package wipdjournal

import (
	"fmt"
	"testing"

	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestVerifyTransferAcceptsCompleteStep4EventPrefix(t *testing.T) {
	records := step4TransferRecords(t)
	end := hydrationEventAnchor(records)
	manifest := hydrationManifest(testIdentity, end, []wipdwire.BlobManifestEntry{})
	transfer, err := VerifyTransfer(testIdentity.DomainID, testIdentity.AuthorityEpoch, emptyTransferAnchor(), end, records, manifest)
	if err != nil || !transfer.Valid() || len(transfer.Records()) != len(records) {
		t.Fatalf("verify complete M6 Step 4 transfer: valid=%t records=%d err=%v", transfer.Valid(), len(transfer.Records()), err)
	}
	wantKinds := []string{
		"matter.created", "matter.locator-repair-required", "matter.locator-repaired", "stage.created",
		"step.created", "step.created", "step.inserted", "step.reordered", "step.replaced", "step.removed",
	}
	for index, record := range transfer.Records() {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil || fields["kind"] != wantKinds[index] {
			t.Fatalf("verified Step 4 event %d kind=%v err=%v; want %s", index, fields["kind"], decodeErr, wantKinds[index])
		}
	}
}

func TestVerifyTransferRejectsMalformedStep4EventPayloads(t *testing.T) {
	for _, test := range []struct {
		name   string
		index  int
		mutate func(map[string]any)
	}{
		{"extra Stage field", 3, func(payload map[string]any) { payload["raw_sql"] = "UPDATE matters" }},
		{"duplicate reordered Step", 7, func(payload map[string]any) {
			payload["order"] = []any{testCommandPrefix + "56", testCommandPrefix + "56"}
		}},
		{"invalid locator repair action", 2, func(payload map[string]any) { payload["action"] = "force" }},
		{"malformed inserted Step locator", 6, func(payload map[string]any) { payload["locator"] = "arbitrary/path" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := step4TransferRecords(t)
			fields, err := wipdwire.DecodeCanonicalMap(records[test.index].Record,
				"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			if err != nil {
				t.Fatal(err)
			}
			payload := fields["payload"].(map[string]any)
			test.mutate(payload)
			fields["payload"] = payload
			records[test.index].Record, err = wipdwire.EncodeCanonical(fields)
			if err != nil {
				t.Fatal(err)
			}
			end := hydrationEventAnchor(records)
			manifest := hydrationManifest(testIdentity, end, []wipdwire.BlobManifestEntry{})
			if _, err = VerifyTransfer(testIdentity.DomainID, testIdentity.AuthorityEpoch, emptyTransferAnchor(), end, records, manifest); err == nil {
				t.Fatal("accepted malformed Step 4 event payload")
			}
		})
	}
}

func step4TransferRecords(t *testing.T) []wipdwire.EventRecord {
	t.Helper()
	const (
		matter       = testCommandPrefix + "50"
		stage        = testCommandPrefix + "55"
		stepA        = testCommandPrefix + "56"
		stepB        = testCommandPrefix + "57"
		stepInserted = testCommandPrefix + "58"
		stepReplaced = testCommandPrefix + "59"
		locator      = "roadmap-01kz7x"
	)
	specs := []struct {
		kind, subject string
		payload       map[string]any
	}{
		{"matter.created", matter, map[string]any{"id": matter, "locator": locator, "title": "Roadmap"}},
		{"matter.locator-repair-required", matter, map[string]any{"requested_locator": "roadmap", "assigned_locator": locator}},
		{"matter.locator-repaired", matter, map[string]any{"action": "accept", "requested_locator": "roadmap", "previous_locator": locator, "assigned_locator": locator}},
		{"stage.created", stage, map[string]any{"matter_id": matter, "locator": "planning", "title": " Planning ", "sort_key": uint64(2000)}},
		{"step.created", stepA, map[string]any{"title": " First ", "locator": "step-01", "parent": stage, "sort_key": uint64(1000)}},
		{"step.created", stepB, map[string]any{"title": "Second", "locator": "step-02", "parent": stage, "sort_key": uint64(2000)}},
		{"step.inserted", stepInserted, map[string]any{"title": "Between", "locator": "step-03", "parent": stage, "sort_key": uint64(1500)}},
		{"step.reordered", stage, map[string]any{"order": []any{stepB, stepInserted, stepA}}},
		{"step.replaced", stepA, map[string]any{"replacement": stepReplaced, "title": "First replacement", "locator": "step-04"}},
		{"step.removed", stepB, map[string]any{"reason": "superseded"}},
	}
	records := make([]wipdwire.EventRecord, 0, len(specs))
	for index, spec := range specs {
		eventID := testCommandPrefix + fmt.Sprintf("%02d", index+60)
		record, err := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": eventID, "domain_id": testIdentity.DomainID,
			"command_id": testCommandPrefix + "90", "request_hash": hydrationDigest([]byte(fmt.Sprintf("Step 4 transfer command %d", index))),
			"environment": map[string]any{"id": testIdentity.EnvironmentID, "sequence": uint64(index + 1)},
			"acted_at":    "2026-09-29T00:00:00Z", "occurred_at": "2026-09-29T00:00:00Z",
			"kind": spec.kind, "subject_id": spec.subject, "repo_id": testIdentity.RepoID, "payload": spec.payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, wipdwire.EventRecord{EventID: eventID, Record: record})
	}
	return records
}
