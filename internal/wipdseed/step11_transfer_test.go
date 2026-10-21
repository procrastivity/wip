package wipdseed

import (
	"testing"

	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestFoldEventRecordsAcceptsAttachedCheckoutAndRejectsMalformedProof(t *testing.T) {
	record := func(payload map[string]any) wipdwire.EventRecord {
		t.Helper()
		raw, err := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": "01KZ7XHAQT1S46NYPN1PW1DX3E", "domain_id": testDomainID,
			"command_id": "01KZ7XHAQT1S46NYPN1PW1DX3F", "request_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"environment": map[string]any{"id": testRepoID, "sequence": uint64(1)},
			"acted_at":    "2026-10-01T00:00:00Z", "occurred_at": "2026-10-01T00:00:00Z",
			"kind": "environment.checkout-attached/1", "subject_id": testRepoID, "repo_id": testRepoID, "payload": payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		return wipdwire.EventRecord{EventID: "01KZ7XHAQT1S46NYPN1PW1DX3E", Record: raw}
	}
	payload := map[string]any{
		"repo_id": testRepoID, "environment_id": testRepoID, "normalized_repo_key": "github.com/acme/widget",
		"clone_id": "01KZ7XHAQT1S46NYPN1PW1DX3G", "worktree_id": "01KZ7XHAQT1S46NYPN1PW1DX3H",
		"common_dir_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"git_dir_digest":    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"worktree_name":     "widget", "grant_id": testGrantID,
		"grant_digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	if _, _, _, err := foldEventRecordsCore([]wipdwire.EventRecord{record(payload)}, testDomainID); err != nil {
		t.Fatalf("fold attached checkout event: %v", err)
	}
	malformed := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		malformed[key] = value
	}
	malformed["caller_path"] = "/untrusted"
	if _, _, _, err := foldEventRecordsCore([]wipdwire.EventRecord{record(malformed)}, testDomainID); err == nil {
		t.Fatal("fold accepted an attached checkout event with an unknown proof field")
	}
	payload["worktree_name"] = ""
	if _, _, _, err := foldEventRecordsCore([]wipdwire.EventRecord{record(payload)}, testDomainID); err != nil {
		t.Fatalf("fold main checkout with empty worktree name: %v", err)
	}
}
