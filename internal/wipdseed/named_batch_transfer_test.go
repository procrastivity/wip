package wipdseed

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/wipdwire"
)

func namedBatchClientTestEvent(t *testing.T, eventNumber, commandNumber int, sequence uint64, batchID, name string) wipdwire.EventRecord {
	t.Helper()
	record := step7ClientTestEvent(t, eventNumber, commandNumber, sequence, "batch.created", batchID, map[string]any{"name": name})
	return changeSweepFoldEvent(t, record, func(fields map[string]any) { fields["repo_id"] = nil })
}

func TestNamedBatchTransferSeedAndPullPreserveDomainFold(t *testing.T) {
	fixture := newClientFixture(t)
	first := namedBatchClientTestEvent(t, 620, 560, 1, "00000000000000000000000061", "release train")
	second := namedBatchClientTestEvent(t, 621, 561, 2, "00000000000000000000000062", "incident response")
	records := []wipdwire.EventRecord{first, second}
	want := step7IndependentPrefixAnchor(records)
	for _, test := range []struct {
		name  string
		kind  string
		prior []wipdwire.EventRecord
	}{
		{name: "seed", kind: "seed"},
		{name: "incremental pull", kind: "pull", prior: records[:1]},
	} {
		t.Run(test.name, func(t *testing.T) {
			frames, installed := step7ClientRepairTransferFrames(t, test.kind, records, test.prior)
			state, err := verifyTransferFrames(frames, test.kind, fixture.profile, testRepoID, sweepFoldEnv,
				"sha256:"+strings.Repeat("a", 64), installed, test.prior)
			if err != nil || !anchorEqual(state.Prefix, want) || !reflect.DeepEqual(state.EventRecords, records) {
				t.Fatalf("%s did not preserve the named-Batch history: prefix=%+v records=%d err=%v", test.kind, state.Prefix, len(state.EventRecords), err)
			}
		})
	}
}

func TestNamedBatchTransferRejectsUnboundOrDomainDuplicateBirths(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "Repo-bound", mutate: func(fields map[string]any) { fields["repo_id"] = testRepoID }},
		{name: "empty name", mutate: func(fields map[string]any) { fields["payload"].(map[string]any)["name"] = "" }},
		{name: "untrimmed name", mutate: func(fields map[string]any) { fields["payload"].(map[string]any)["name"] = " release train " }},
		{name: "extra payload field", mutate: func(fields map[string]any) { fields["payload"].(map[string]any)["extra"] = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := namedBatchClientTestEvent(t, 620, 560, 1, "00000000000000000000000061", "release train")
			record = changeSweepFoldEvent(t, record, test.mutate)
			if _, _, _, err := foldEventRecords([]wipdwire.EventRecord{record}, testDomainID); !errors.Is(err, ErrInvalidClientState) {
				t.Fatalf("invalid named-Batch event was accepted: %v", err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		second string
		name2  string
	}{
		{name: "duplicate domain name", second: "00000000000000000000000062", name2: "release train"},
		{name: "duplicate Batch identity", second: "00000000000000000000000061", name2: "incident response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := namedBatchClientTestEvent(t, 620, 560, 1, "00000000000000000000000061", "release train")
			second := namedBatchClientTestEvent(t, 621, 561, 2, test.second, test.name2)
			if _, _, _, err := foldEventRecords([]wipdwire.EventRecord{first, second}, testDomainID); !errors.Is(err, ErrInvalidClientState) {
				t.Fatalf("duplicate named-Batch birth was accepted: %v", err)
			}
		})
	}

	t.Run("Batch identity collides with another domain entity", func(t *testing.T) {
		batchID := "00000000000000000000000061"
		matter := step7ClientTestEvent(t, 619, 559, 1, "matter.created", batchID,
			map[string]any{"id": batchID, "locator": "other-entity", "title": "Other entity"})
		batch := namedBatchClientTestEvent(t, 620, 560, 2, batchID, "release train")
		if _, _, _, err := foldEventRecords([]wipdwire.EventRecord{matter, batch}, testDomainID); !errors.Is(err, ErrInvalidClientState) {
			t.Fatalf("named-Batch ID reused another domain entity: %v", err)
		}
	})
}
