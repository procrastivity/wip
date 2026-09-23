package conformance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type clientCommand struct {
	hash   string
	result string
}

type clientDouble struct {
	commands   map[string]clientCommand
	executions uint64
	effects    uint64
	liveItems  []string
	snapshots  map[string][]string
	blobs      map[string]struct{}
	claimID    string
	claimEpoch uint64
	claimState string
	pending    uint64
	terminal   uint64
}

func runClientDouble(script []action) []string {
	c := clientDouble{
		commands:  make(map[string]clientCommand),
		snapshots: make(map[string][]string),
		blobs:     make(map[string]struct{}),
	}
	transcript := make([]string, 0, len(script))
	for _, next := range script {
		transcript = append(transcript, c.apply(next))
	}
	return transcript
}

func (c *clientDouble) apply(next action) string {
	switch next.Op {
	case "submit", "submit-lost":
		if prior, ok := c.commands[next.ID]; ok {
			if prior.hash != next.Hash {
				return "command.id-conflict"
			}
			return fmt.Sprintf("replay:%s:executions=%d:effects=%d", prior.result, c.executions, c.effects)
		}
		c.executions++
		if next.Result == "result.succeeded" {
			c.effects++
		}
		c.commands[next.ID] = clientCommand{hash: next.Hash, result: next.Result}
		if next.Op == "submit-lost" {
			return fmt.Sprintf("outcome-unknown:executions=%d:effects=%d", c.executions, c.effects)
		}
		return fmt.Sprintf("terminal:%s:executions=%d:effects=%d", next.Result, c.executions, c.effects)
	case "retry":
		prior, ok := c.commands[next.ID]
		if !ok {
			return "receipt.not-found"
		}
		if prior.hash != next.Hash {
			return "command.id-conflict"
		}
		return fmt.Sprintf("replay:%s:executions=%d:effects=%d", prior.result, c.executions, c.effects)
	case "receipt-query":
		prior, ok := c.commands[next.ID]
		if !ok {
			return "receipt.not-found"
		}
		if prior.hash != next.Hash {
			return "command.id-conflict"
		}
		return fmt.Sprintf("terminal:%s:executions=%d:effects=%d", prior.result, c.executions, c.effects)
	case "snapshot-open":
		c.liveItems = append([]string(nil), next.Items...)
		c.snapshots[next.ID] = append([]string(nil), next.Items...)
		return fmt.Sprintf("snapshot:%s:count=%d", next.ID, len(next.Items))
	case "snapshot-mutate":
		c.liveItems = append(c.liveItems, next.Items...)
		return fmt.Sprintf("state:count=%d", len(c.liveItems))
	case "snapshot-page":
		items := c.snapshots[next.ID]
		start := min(int(next.Offset), len(items))
		end := min(start+int(next.Length), len(items))
		return fmt.Sprintf("page:%s:%s", next.ID, strings.Join(items[start:end], ","))
	case "blob-upload":
		actual := sha256.Sum256([]byte(next.Content))
		actualHash := "sha256:" + hex.EncodeToString(actual[:])
		if actualHash != next.Hash {
			return "blob.digest-mismatch"
		}
		if uint64(len(next.Content)) != next.Length {
			return "blob.length-mismatch"
		}
		if _, ok := c.blobs[next.Hash]; ok {
			return "blob.available"
		}
		c.blobs[next.Hash] = struct{}{}
		return "blob.staged"
	case "claim-acquire":
		if c.claimState != "" && c.claimState != "closed" {
			return "refusal.claim-contended"
		}
		c.claimID, c.claimEpoch, c.claimState = next.ID, next.Epoch, "hydrating"
		c.pending, c.terminal = 0, 0
		return "claim:hydrating"
	case "claim-hydrate":
		if !c.sameClaim(next) {
			return "claim.fenced"
		}
		c.claimState = "offline-ready"
		return "claim:offline-ready"
	case "claim-append":
		if !c.sameClaim(next) {
			return "claim.fenced"
		}
		if c.claimState != "offline-ready" {
			return "claim.not-ready"
		}
		c.pending++
		return fmt.Sprintf("journal:pending=%d", c.pending)
	case "claim-fold":
		if !c.sameClaim(next) {
			return "claim.fenced"
		}
		if c.pending == 0 {
			return "journal.noncontiguous"
		}
		c.pending--
		c.terminal++
		return fmt.Sprintf("journal:terminal=%d", c.terminal)
	case "claim-release":
		if !c.sameClaim(next) {
			return "claim.fenced"
		}
		if c.pending != 0 || c.terminal == 0 {
			return "refusal.claim-release-barrier"
		}
		c.claimState = "closed"
		return "claim:closed"
	case "negotiate":
		if next.ClientMajor != next.ServerMajor {
			return "protocol.incompatible-version"
		}
		return fmt.Sprintf("protocol:%d", next.ClientMajor)
	case "operation-version":
		if next.Requested != next.Supported {
			return "operation.unsupported-version"
		}
		return fmt.Sprintf("operation:%d", next.Requested)
	case "reseed":
		return fmt.Sprintf("store.reseed-required:preserved=%d", len(next.Preserve))
	default:
		return "protocol.unknown-test-action"
	}
}

func (c *clientDouble) sameClaim(next action) bool {
	return c.claimState != "closed" && c.claimID == next.ID && c.claimEpoch == next.Epoch
}

type serverReceipt struct {
	requestHash string
	disposition string
}

type serverDouble struct {
	receipts     map[string]serverReceipt
	runs         map[string]uint64
	modelEffects uint64
	base         []string
	views        map[string][]string
	staged       map[string][]byte
	claim        struct {
		id, phase      string
		epoch          uint64
		open, complete uint64
	}
}

func runServerDouble(script []action) []string {
	s := serverDouble{
		receipts: make(map[string]serverReceipt),
		runs:     make(map[string]uint64),
		views:    make(map[string][]string),
		staged:   make(map[string][]byte),
	}
	result := make([]string, len(script))
	for i := range script {
		result[i] = s.handle(script[i])
	}
	return result
}

func (s *serverDouble) handle(request action) string {
	switch request.Op {
	case "submit", "submit-lost":
		if stored, found := s.receipts[request.ID]; found {
			if stored.requestHash != request.Hash {
				return "command.id-conflict"
			}
			return fmt.Sprintf("replay:%s:executions=%d:effects=%d", stored.disposition, s.totalRuns(), s.modelEffects)
		}
		s.runs[request.ID]++
		if request.Result == "result.succeeded" {
			s.modelEffects++
		}
		s.receipts[request.ID] = serverReceipt{requestHash: request.Hash, disposition: request.Result}
		if request.Op == "submit-lost" {
			return fmt.Sprintf("outcome-unknown:executions=%d:effects=%d", s.totalRuns(), s.modelEffects)
		}
		return fmt.Sprintf("terminal:%s:executions=%d:effects=%d", request.Result, s.totalRuns(), s.modelEffects)
	case "retry", "receipt-query":
		stored, found := s.receipts[request.ID]
		if !found {
			return "receipt.not-found"
		}
		if stored.requestHash != request.Hash {
			return "command.id-conflict"
		}
		prefix := "replay"
		if request.Op == "receipt-query" {
			prefix = "terminal"
		}
		return fmt.Sprintf("%s:%s:executions=%d:effects=%d", prefix, stored.disposition, s.totalRuns(), s.modelEffects)
	case "snapshot-open":
		s.base = append(s.base[:0], request.Items...)
		s.views[request.ID] = append([]string(nil), s.base...)
		return fmt.Sprintf("snapshot:%s:count=%d", request.ID, len(s.views[request.ID]))
	case "snapshot-mutate":
		s.base = append(s.base, request.Items...)
		return fmt.Sprintf("state:count=%d", len(s.base))
	case "snapshot-page":
		view := s.views[request.ID]
		if request.Offset >= uint64(len(view)) {
			return fmt.Sprintf("page:%s:", request.ID)
		}
		last := request.Offset + request.Length
		if last > uint64(len(view)) {
			last = uint64(len(view))
		}
		return "page:" + request.ID + ":" + strings.Join(view[request.Offset:last], ",")
	case "blob-upload":
		content := []byte(request.Content)
		digest := sha256.Sum256(content)
		if "sha256:"+hex.EncodeToString(digest[:]) != request.Hash {
			return "blob.digest-mismatch"
		}
		if uint64(len(content)) != request.Length {
			return "blob.length-mismatch"
		}
		if _, found := s.staged[request.Hash]; found {
			return "blob.available"
		}
		s.staged[request.Hash] = append([]byte(nil), content...)
		return "blob.staged"
	case "claim-acquire":
		if s.claim.phase != "" && s.claim.phase != "closed" {
			return "refusal.claim-contended"
		}
		s.claim.id, s.claim.epoch, s.claim.phase = request.ID, request.Epoch, "hydrating"
		s.claim.open, s.claim.complete = 0, 0
		return "claim:hydrating"
	case "claim-hydrate":
		if !s.claimMatches(request) {
			return "claim.fenced"
		}
		s.claim.phase = "offline-ready"
		return "claim:offline-ready"
	case "claim-append":
		if !s.claimMatches(request) {
			return "claim.fenced"
		}
		if s.claim.phase != "offline-ready" {
			return "claim.not-ready"
		}
		s.claim.open++
		return fmt.Sprintf("journal:pending=%d", s.claim.open)
	case "claim-fold":
		if !s.claimMatches(request) {
			return "claim.fenced"
		}
		if s.claim.open == 0 {
			return "journal.noncontiguous"
		}
		s.claim.open--
		s.claim.complete++
		return fmt.Sprintf("journal:terminal=%d", s.claim.complete)
	case "claim-release":
		if !s.claimMatches(request) {
			return "claim.fenced"
		}
		if s.claim.open > 0 || s.claim.complete == 0 {
			return "refusal.claim-release-barrier"
		}
		s.claim.phase = "closed"
		return "claim:closed"
	case "negotiate":
		if request.ClientMajor == request.ServerMajor {
			return fmt.Sprintf("protocol:%d", request.ServerMajor)
		}
		return "protocol.incompatible-version"
	case "operation-version":
		if request.Requested == request.Supported {
			return fmt.Sprintf("operation:%d", request.Supported)
		}
		return "operation.unsupported-version"
	case "reseed":
		preserved := make(map[string]bool, len(request.Preserve))
		for _, evidence := range request.Preserve {
			preserved[evidence] = true
		}
		return fmt.Sprintf("store.reseed-required:preserved=%d", len(preserved))
	default:
		return "protocol.unknown-test-action"
	}
}

func (s *serverDouble) totalRuns() uint64 {
	var total uint64
	for _, count := range s.runs {
		total += count
	}
	return total
}

func (s *serverDouble) claimMatches(request action) bool {
	return s.claim.phase != "closed" && request.ID == s.claim.id && request.Epoch == s.claim.epoch
}

func TestIndependentClientServerAgreement(t *testing.T) {
	fixture := loadConformanceFixture(t)
	for _, vector := range fixture.AgreementVectors {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			client := runClientDouble(vector.Script)
			server := runServerDouble(vector.Script)
			if !reflect.DeepEqual(client, server) {
				t.Fatalf("independent doubles disagree\nclient: %v\nserver: %v", client, server)
			}
			if !reflect.DeepEqual(client, vector.Expected) {
				t.Fatalf("transcript differs from published vector\n got: %v\nwant: %v", client, vector.Expected)
			}
			t.Logf("%s agreement: %s", vector.Family, strings.Join(client, " -> "))
		})
	}
}
