package authoritystore

import (
	"errors"
	"sort"
	"testing"
)

func TestClaimBlobClosureIncludesMatterAndDirectStepsOnly(t *testing.T) {
	matterA := "01KZ7XHAQT1S46NYPN1PW1DX31"
	matterB := "01KZ7XHAQT1S46NYPN1PW1DX32"
	commandMatterA := "01KZ7XHAQT1S46NYPN1PW1DX41"
	commandStepA := "01KZ7XHAQT1S46NYPN1PW1DX42"
	commandStepB := "01KZ7XHAQT1S46NYPN1PW1DX43"
	commandMatterB := "01KZ7XHAQT1S46NYPN1PW1DX44"
	var members []string
	for _, event := range []struct {
		kind, subject, parent, command string
	}{
		{"matter.created", matterA, "", commandMatterA},
		{"step.created", "01KZ7XHAQT1S46NYPN1PW1DX51", matterA, commandStepA},
		{"step.created", "01KZ7XHAQT1S46NYPN1PW1DX52", matterB, commandStepB},
		{"matter.created", matterB, "", commandMatterB},
	} {
		member, included, err := subtreeEventMember(
			subtreeTestEvent(t, event.kind, event.subject, event.parent, event.command), domainA, event.command, event.command, matterA,
		)
		if err != nil {
			t.Fatalf("classify %s event: %v", event.kind, err)
		}
		if included {
			members = append(members, member)
		}
	}
	wantMembers := []string{commandMatterA, commandStepA}
	if len(members) != len(wantMembers) || members[0] != wantMembers[0] || members[1] != wantMembers[1] {
		t.Fatalf("subtree command membership = %v, want Matter birth and its direct Step only %v", members, wantMembers)
	}

	shared := digest('b')
	closure, err := mergeBlobClosures([]map[string]uint64{
		{digest('c'): 5, shared: 3},
		{digest('a'): 11, shared: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(closure))
	for value := range closure {
		got = append(got, value)
	}
	sort.Strings(got)
	want := []string{digest('a'), digest('b'), digest('c')}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || closure[shared] != 3 {
		t.Fatalf("deduplicated complete command closure = %v (%v), want shared digest once with stable lengths", got, closure)
	}
	if _, err = mergeBlobClosures([]map[string]uint64{{shared: 3}, {shared: 4}}); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("same digest with conflicting length accepted: %v", err)
	}
	if !sameDigestSet([]string{digest('b'), digest('a')}, []string{digest('a'), digest('b')}) ||
		sameDigestSet([]string{digest('a'), digest('a')}, []string{digest('a'), digest('b')}) {
		t.Fatal("caller assertion comparison did not treat the derived closure as a set")
	}
}

func subtreeTestEvent(t *testing.T, kind, subject, parent, command string) []byte {
	t.Helper()
	payload := map[string]any{"id": subject, "locator": "m", "title": "Matter"}
	if kind == "step.created" {
		payload = map[string]any{"title": "Step", "locator": "step-01", "parent": parent, "sort_key": int64(1000)}
	}
	encoded, err := artifactEncoder.Marshal(map[string]any{
		"schema": "wipd.event/1", "event_id": command, "domain_id": domainA, "command_id": command,
		"request_hash": digest('e'), "environment": map[string]any{"id": repoA, "sequence": uint64(1)},
		"acted_at": "2026-09-28T00:00:00Z", "occurred_at": "2026-09-28T00:00:01Z",
		"kind": kind, "subject_id": subject, "repo_id": repoA, "payload": payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
