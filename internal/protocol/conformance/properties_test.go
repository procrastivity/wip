package conformance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestBoundedConformanceProperties(t *testing.T) {
	t.Run("hash identity and independent canonical determinism", propertyHashIdentity)
	t.Run("same ID retry never reexecutes", propertyRetryNonReexecution)
	t.Run("pagination remains pinned", propertyPinnedPagination)
	t.Run("blob digest length and duplicate rules", propertyBlobIntegrity)
	t.Run("claim transitions remain fenced", propertyClaimTransitions)
}

func propertyHashIdentity(t *testing.T) {
	seen := make(map[string]struct{}, 256)
	for i := range 256 {
		title := fmt.Sprintf("Property case %03d — Café", i)
		command := originCommand(title, uint64(i+1))
		client, err := clientCanonical(command)
		if err != nil {
			t.Fatal(err)
		}
		server, err := serverCanonical(command)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(client, server) {
			t.Fatalf("case %d canonical encoders disagree", i)
		}
		hash := canonicalHash(client)
		if _, duplicate := seen[hash]; duplicate {
			t.Fatalf("case %d collided at %s", i, hash)
		}
		seen[hash] = struct{}{}

		changed, err := clientCanonical(originCommand(title+"!", uint64(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		if canonicalHash(changed) == hash {
			t.Fatalf("case %d changed title retained hash", i)
		}
	}

	base, err := clientCanonical(originCommand("Café protocol identity", 7))
	if err != nil {
		t.Fatal(err)
	}
	baseHash := canonicalHash(base)
	mutations := []struct {
		name string
		edit func(map[string]any)
	}{
		{"command ID", func(c map[string]any) { c["command_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"domain", func(c map[string]any) { c["authority"].(map[string]any)["domain_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"authority epoch", func(c map[string]any) { c["authority"].(map[string]any)["expected_epoch"] = uint64(8) }},
		{"Environment", func(c map[string]any) { c["environment"].(map[string]any)["id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"Environment sequence", func(c map[string]any) { c["environment"].(map[string]any)["sequence"] = uint64(43) }},
		{"acted time", func(c map[string]any) { c["acted_at"] = "2026-09-22T17:31:43.123456789Z" }},
		{"actor", func(c map[string]any) { c["actor"] = "human" }},
		{"causation", func(c map[string]any) { c["causation_command_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"correlation", func(c map[string]any) { c["correlation_command_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"operation", func(c map[string]any) { c["operation"].(map[string]any)["version"] = uint64(2) }},
		{"Repo context", func(c map[string]any) { c["context"].(map[string]any)["repo_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"Clone context", func(c map[string]any) { c["context"].(map[string]any)["clone_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"Worktree context", func(c map[string]any) { c["context"].(map[string]any)["worktree_id"] = "01K5V8M2C4D6F8G0H2J4K6M8NP" }},
		{"claim", func(c map[string]any) {
			c["claim"] = map[string]any{"id": "01K5V8M2C4D6F8G0H2J4K6M8NP", "epoch": uint64(1)}
		}},
		{"input title", func(c map[string]any) { c["input"].(map[string]any)["title"] = "Changed" }},
		{"input locator", func(c map[string]any) { c["input"].(map[string]any)["requested_locator"] = "changed" }},
		{"blob set", func(c map[string]any) {
			c["blobs"] = []any{map[string]any{
				"name": "body", "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "byte_length": uint64(1),
			}}
		}},
	}
	for _, mutation := range mutations {
		command := originCommand("Café protocol identity", 7)
		mutation.edit(command)
		client, err := clientCanonical(command)
		if err != nil {
			t.Fatalf("%s client canonical: %v", mutation.name, err)
		}
		server, err := serverCanonical(command)
		if err != nil {
			t.Fatalf("%s server canonical: %v", mutation.name, err)
		}
		if !bytes.Equal(client, server) {
			t.Errorf("%s canonical encoders disagree", mutation.name)
		}
		if canonicalHash(client) == baseHash {
			t.Errorf("%s retained request hash", mutation.name)
		}
	}
}

func propertyRetryNonReexecution(t *testing.T) {
	for i := range 200 {
		id := fmt.Sprintf("command-%03d", i)
		digest := sha256.Sum256([]byte(id))
		hash := "sha256:" + hex.EncodeToString(digest[:])
		script := []action{
			{Op: "submit", ID: id, Hash: hash, Result: "result.succeeded"},
			{Op: "retry", ID: id, Hash: hash},
			{Op: "receipt-query", ID: id, Hash: hash},
		}
		want := []string{
			"terminal:result.succeeded:executions=1:effects=1",
			"replay:result.succeeded:executions=1:effects=1",
			"terminal:result.succeeded:executions=1:effects=1",
		}
		client, server := runClientDouble(script), runServerDouble(script)
		if !reflect.DeepEqual(client, want) || !reflect.DeepEqual(server, want) {
			t.Fatalf("case %d retry transcript client/server = %v / %v", i, client, server)
		}
	}
}

func propertyPinnedPagination(t *testing.T) {
	random := rand.New(rand.NewSource(0x57595044))
	for i := range 150 {
		initialCount := 1 + random.Intn(30)
		pageSize := 1 + random.Intn(7)
		items := make([]string, initialCount)
		for item := range items {
			items[item] = fmt.Sprintf("%03d", item)
		}
		script := []action{
			{Op: "snapshot-open", ID: "snapshot", Items: items},
			{Op: "snapshot-mutate", Items: []string{"later-a", "later-b"}},
		}
		for offset := 0; offset < initialCount; offset += pageSize {
			script = append(script, action{Op: "snapshot-page", ID: "snapshot", Offset: uint64(offset), Length: uint64(pageSize)})
		}
		client, server := runClientDouble(script), runServerDouble(script)
		if !reflect.DeepEqual(client, server) {
			t.Fatalf("case %d pagination doubles disagree: %v / %v", i, client, server)
		}
		var paged []string
		for _, outcome := range client[2:] {
			_, page, ok := strings.Cut(outcome, "page:snapshot:")
			if !ok {
				t.Fatalf("case %d invalid page outcome %q", i, outcome)
			}
			if page != "" {
				paged = append(paged, strings.Split(page, ",")...)
			}
		}
		if !reflect.DeepEqual(paged, items) {
			t.Fatalf("case %d pinned result = %v, want %v", i, paged, items)
		}
	}
}

func propertyBlobIntegrity(t *testing.T) {
	random := rand.New(rand.NewSource(0x424c4f42))
	for i := range 150 {
		content := make([]byte, random.Intn(128))
		if _, err := random.Read(content); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		hash := "sha256:" + hex.EncodeToString(digest[:])
		wrong := sha256.Sum256(append(append([]byte(nil), content...), 0))
		script := []action{
			{Op: "blob-upload", Content: string(content), Hash: hash, Length: uint64(len(content))},
			{Op: "blob-upload", Content: string(content), Hash: hash, Length: uint64(len(content))},
			{Op: "blob-upload", Content: string(content), Hash: "sha256:" + hex.EncodeToString(wrong[:]), Length: uint64(len(content))},
			{Op: "blob-upload", Content: string(content), Hash: hash, Length: uint64(len(content)) + 1},
		}
		want := []string{"blob.staged", "blob.available", "blob.digest-mismatch", "blob.length-mismatch"}
		client, server := runClientDouble(script), runServerDouble(script)
		if !reflect.DeepEqual(client, want) || !reflect.DeepEqual(server, want) {
			t.Fatalf("case %d blob transcript client/server = %v / %v", i, client, server)
		}
	}
}

func propertyClaimTransitions(t *testing.T) {
	for i := range 150 {
		epoch := uint64(i + 1)
		script := []action{
			{Op: "claim-acquire", ID: "claim", Epoch: epoch},
			{Op: "claim-hydrate", ID: "claim", Epoch: epoch},
			{Op: "claim-append", ID: "claim", Epoch: epoch},
			{Op: "claim-release", ID: "claim", Epoch: epoch},
			{Op: "claim-fold", ID: "claim", Epoch: epoch},
			{Op: "claim-release", ID: "claim", Epoch: epoch},
			{Op: "claim-append", ID: "claim", Epoch: epoch},
			{Op: "claim-append", ID: "claim", Epoch: epoch + 1},
		}
		want := []string{
			"claim:hydrating", "claim:offline-ready", "journal:pending=1",
			"refusal.claim-release-barrier", "journal:terminal=1", "claim:closed",
			"claim.fenced", "claim.fenced",
		}
		client, server := runClientDouble(script), runServerDouble(script)
		if !reflect.DeepEqual(client, want) || !reflect.DeepEqual(server, want) {
			t.Fatalf("case %d claim transcript client/server = %v / %v", i, client, server)
		}
	}
}
