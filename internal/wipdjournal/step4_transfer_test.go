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

func TestValidAuthorityEventPinsM6LifecyclePayloadShapes(t *testing.T) {
	const subject = testCommandPrefix + "50"
	for _, test := range []struct {
		name    string
		kind    string
		payload map[string]any
	}{
		{"standalone Matter start", "matter.started", map[string]any{"from": "planned", "to": "in-progress"}},
		{"cascade Matter start", "matter.started", map[string]any{"from": "planned", "to": "in-progress", "cascade": true}},
		{"causal Stage ancestor", "stage.started", map[string]any{"from": "planned", "to": "in-progress", "cascade": true, "cause_event_id": testCommandPrefix + "60"}},
		{"causal Step target", "step.started", map[string]any{"from": "planned", "to": "in-progress", "cause_event_id": testCommandPrefix + "61"}},
		{"Matter canceled without reason", "matter.canceled", map[string]any{"from": "in-progress", "to": "canceled"}},
		{"Stage canceled with reason", "stage.canceled", map[string]any{"from": "in-progress", "to": "canceled", "reason": "superseded"}},
		{"Step paused", "step.paused", map[string]any{"from": "in-progress", "to": "paused"}},
		{"Stage resumed", "stage.resumed", map[string]any{"from": "paused", "to": "in-progress"}},
		{"Matter finished", "matter.finished", map[string]any{"from": "in-progress", "to": "done"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !validAuthorityEvent(m6LifecycleEventRecord(t, test.kind, subject, test.payload), testIdentity.DomainID, testCommandPrefix+"70") {
				t.Fatal("rejected a valid M6 lifecycle payload shape")
			}
		})
	}
	for _, test := range []struct {
		name    string
		kind    string
		payload map[string]any
	}{
		{"unknown lifecycle kind", "stage.started.extra", map[string]any{"from": "planned", "to": "in-progress"}},
		{"wrong transition", "step.paused", map[string]any{"from": "planned", "to": "paused"}},
		{"unexpected field", "matter.resumed", map[string]any{"from": "paused", "to": "in-progress", "raw_sql": "UPDATE matters"}},
		{"Matter start cannot have causal child fields", "matter.started", map[string]any{"from": "planned", "to": "in-progress", "cause_event_id": testCommandPrefix + "61"}},
		{"empty cancellation reason", "step.canceled", map[string]any{"from": "in-progress", "to": "canceled", "reason": " "}},
		{"non-Boolean cascade", "stage.started", map[string]any{"from": "planned", "to": "in-progress", "cascade": "true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if validAuthorityEvent(m6LifecycleEventRecord(t, test.kind, subject, test.payload), testIdentity.DomainID, testCommandPrefix+"70") {
				t.Fatal("accepted a malformed M6 lifecycle payload shape")
			}
		})
	}
}

func TestValidAuthorityEventPinsStep11AttachPayload(t *testing.T) {
	subject := testIdentity.RepoID
	payload := map[string]any{
		"repo_id": testIdentity.RepoID, "environment_id": testIdentity.EnvironmentID,
		"normalized_repo_key": "github.com/acme/widget", "clone_id": testCommandPrefix + "51", "worktree_id": testCommandPrefix + "52",
		"common_dir_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"git_dir_digest":    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"worktree_name":     "widget", "grant_id": testCommandPrefix + "53",
		"grant_digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	if !validAuthorityEvent(m6LifecycleEventRecord(t, "environment.checkout-attached/1", subject, payload), testIdentity.DomainID, testCommandPrefix+"70") {
		t.Fatal("rejected a valid Step 11 attach event")
	}
	payload["worktree_name"] = ""
	if !validAuthorityEvent(m6LifecycleEventRecord(t, "environment.checkout-attached/1", subject, payload), testIdentity.DomainID, testCommandPrefix+"70") {
		t.Fatal("rejected a main-checkout Step 11 attach event with no named worktree")
	}
	payload["caller_path"] = "/untrusted"
	if validAuthorityEvent(m6LifecycleEventRecord(t, "environment.checkout-attached/1", subject, payload), testIdentity.DomainID, testCommandPrefix+"70") {
		t.Fatal("accepted an attach event with an unknown proof field")
	}
}

func TestVerifyTransferRejectsMalformedStep11AttachIdentityAndTypes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{
			name: "payload Environment differs from envelope",
			mutate: func(_, payload map[string]any) {
				payload["environment_id"] = testCommandPrefix + "54"
			},
		},
		{
			name: "payload Repo differs from envelope",
			mutate: func(fields, _ map[string]any) {
				fields["repo_id"] = testCommandPrefix + "55"
			},
		},
		{
			name: "worktree name is not a string",
			mutate: func(_ map[string]any, payload map[string]any) {
				payload["worktree_name"] = uint64(1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{
				"repo_id": testIdentity.RepoID, "environment_id": testIdentity.EnvironmentID,
				"normalized_repo_key": "github.com/acme/widget", "clone_id": testCommandPrefix + "51", "worktree_id": testCommandPrefix + "52",
				"common_dir_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"git_dir_digest":    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"worktree_name":     "", "grant_id": testCommandPrefix + "53",
				"grant_digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			}
			fields, err := wipdwire.DecodeCanonicalMap(m6LifecycleEventRecord(t,
				"environment.checkout-attached/1", testIdentity.RepoID, payload),
				"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			if err != nil {
				t.Fatal(err)
			}
			decodedPayload := fields["payload"].(map[string]any)
			test.mutate(fields, decodedPayload)
			fields["payload"] = decodedPayload
			record, err := wipdwire.EncodeCanonical(fields)
			if err != nil {
				t.Fatal(err)
			}
			records := []wipdwire.EventRecord{{EventID: testCommandPrefix + "70", Record: record}}
			end := hydrationEventAnchor(records)
			manifest := hydrationManifest(testIdentity, end, []wipdwire.BlobManifestEntry{})
			if _, err = VerifyTransfer(testIdentity.DomainID, testIdentity.AuthorityEpoch, emptyTransferAnchor(), end, records, manifest); err == nil {
				t.Fatal("accepted canonical malformed Step 11 attach transfer")
			}
		})
	}
}

func m6LifecycleEventRecord(t *testing.T, kind, subject string, payload map[string]any) []byte {
	t.Helper()
	record, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.event/1", "event_id": testCommandPrefix + "70", "domain_id": testIdentity.DomainID,
		"command_id": testCommandPrefix + "90", "request_hash": hydrationDigest([]byte("M6 lifecycle event")),
		"environment": map[string]any{"id": testIdentity.EnvironmentID, "sequence": uint64(1)},
		"acted_at":    "2026-09-29T00:00:00Z", "occurred_at": "2026-09-29T00:00:00Z",
		"kind": kind, "subject_id": subject, "repo_id": testIdentity.RepoID, "payload": payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
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
