package wipdjournal

import (
	"bytes"
	"database/sql"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func step8Definition(id operation.ID) (operation.Definition, bool) {
	for _, d := range operation.Step8HistoryCatalogue() {
		if d.Metadata().Operation == id {
			return d, true
		}
	}
	return operation.Definition{}, false
}

func step8HistoryKind(kind string) bool {
	switch kind {
	case "dependency.added", "dependency.removed", "reference.bound", "reference.added", "reference.removed", "reference.rebound", "config.set":
		return true
	}
	return false
}

func validStep8HistoryEvent(kind, subject, repo string, p map[string]any) bool {
	if !transferULID.MatchString(subject) {
		return false
	}
	switch kind {
	case "config.set":
		_, valueOK := p["value"].(string)
		return subject == repo && wipdwire.ExactMapKeys(p, "key", "value") && asString(p["key"]) != "" && valueOK
	case "dependency.added", "dependency.removed":
		return wipdwire.ExactMapKeys(p, "edge", "blocker") && transferULID.MatchString(asString(p["edge"])) && transferULID.MatchString(asString(p["blocker"])) && subject != p["blocker"]
	case "reference.bound":
		return wipdwire.ExactMapKeys(p, "ref") && asString(p["ref"]) != ""
	case "reference.added", "reference.removed", "reference.rebound":
		keys := []string{"ref"}
		valid := asString(p["ref"]) != ""
		if kind == "reference.rebound" {
			keys = []string{"from", "to"}
			valid = asString(p["from"]) != "" && asString(p["to"]) != "" && p["from"] != p["to"]
		}
		if level, exists := p["tracker_push_level"]; exists {
			if !trackerPushLevel(asString(level)) {
				return false
			}
			keys = append(keys, "tracker_push_level")
		}
		return valid && wipdwire.ExactMapKeys(p, keys...)
	}
	return false
}

// step8EventMatches binds a terminal effect to the exact typed command. Legacy
// reference.bound belongs to imported history, never a new reference.bind.
func step8EventMatches(f map[string]any, entry Entry) bool {
	d, known := step8Definition(entry.Command.Request.Operation)
	if !known || d.ValidateRequest(entry.Command.Request) != nil || entry.Command.Request.Claim != nil ||
		f["domain_id"] != entry.Command.AuthorityDomainID || f["repo_id"] != entry.Command.Request.Context.Repo || f["acted_at"] != entry.Command.ActedAt {
		return false
	}
	p, ok := f["payload"].(map[string]any)
	if !ok {
		return false
	}
	kind, subject := asString(f["kind"]), asString(f["subject_id"])
	if !validStep8HistoryEvent(kind, subject, asString(f["repo_id"]), p) {
		return false
	}
	switch input := entry.Command.Request.Input.(type) {
	case operation.DependencyAddInput:
		return kind == "dependency.added" && subject == input.BlockedID && p["blocker"] == input.BlockerID
	case operation.DependencyAddV2Input:
		return kind == "dependency.added" && subject == input.BlockedID && p["blocker"] == input.BlockerID
	case operation.DependencyRemoveInput:
		return kind == "dependency.removed" && subject == input.BlockedID && p["blocker"] == input.BlockerID
	case operation.DependencyRemoveV2Input:
		return kind == "dependency.removed" && subject == input.BlockedID && p["blocker"] == input.BlockerID
	case operation.ReferenceBindInput:
		return kind == "reference.added" && subject == input.MatterID && p["ref"] == input.Reference && trackerPushLevel(asString(p["tracker_push_level"]))
	case operation.ReferenceBindV2Input:
		return kind == "reference.added" && subject == input.MatterID && p["ref"] == input.Reference && trackerPushLevel(asString(p["tracker_push_level"]))
	case operation.ReferenceUnbindInput:
		return kind == "reference.removed" && subject == input.MatterID && p["ref"] == input.Reference && trackerPushLevel(asString(p["tracker_push_level"]))
	case operation.ReferenceUnbindV2Input:
		return kind == "reference.removed" && subject == input.MatterID && p["ref"] == input.Reference && trackerPushLevel(asString(p["tracker_push_level"]))
	case operation.ReferenceRebindInput:
		return kind == "reference.rebound" && subject == input.MatterID && p["from"] == input.From && p["to"] == input.To && trackerPushLevel(asString(p["tracker_push_level"]))
	case operation.ReferenceRebindV2Input:
		return kind == "reference.rebound" && subject == input.MatterID && p["from"] == input.From && p["to"] == input.To && trackerPushLevel(asString(p["tracker_push_level"]))
	}
	return false
}

func step8OutputMatches(entry Entry, raw []byte, edge string) bool {
	var want map[string]any
	switch input := entry.Command.Request.Input.(type) {
	case operation.DependencyAddInput:
		f, err := wipdwire.DecodeCanonicalMap(raw, "edge", "blocked_id", "blocker_id")
		if err != nil || !transferULID.MatchString(asString(f["edge"])) || edge != "" && f["edge"] != edge {
			return false
		}
		want = map[string]any{"edge": f["edge"], "blocked_id": input.BlockedID, "blocker_id": input.BlockerID}
	case operation.DependencyAddV2Input:
		f, err := wipdwire.DecodeCanonicalMap(raw, "edge", "blocked_id", "blocker_id")
		if err != nil || !transferULID.MatchString(asString(f["edge"])) || edge != "" && f["edge"] != edge {
			return false
		}
		want = map[string]any{"edge": f["edge"], "blocked_id": input.BlockedID, "blocker_id": input.BlockerID}
	case operation.DependencyRemoveInput:
		want = map[string]any{"blocked_id": input.BlockedID, "blocker_id": input.BlockerID}
	case operation.DependencyRemoveV2Input:
		want = map[string]any{"blocked_id": input.BlockedID, "blocker_id": input.BlockerID}
	case operation.ReferenceBindInput:
		want = map[string]any{"matter_id": input.MatterID, "reference": input.Reference}
	case operation.ReferenceBindV2Input:
		want = map[string]any{"matter_id": input.MatterID, "reference": input.Reference}
	case operation.ReferenceUnbindInput:
		want = map[string]any{"matter_id": input.MatterID, "reference": input.Reference}
	case operation.ReferenceUnbindV2Input:
		want = map[string]any{"matter_id": input.MatterID, "reference": input.Reference}
	case operation.ReferenceRebindInput:
		want = map[string]any{"matter_id": input.MatterID, "reference": input.To, "previous_reference": input.From}
	case operation.ReferenceRebindV2Input:
		want = map[string]any{"matter_id": input.MatterID, "reference": input.To, "previous_reference": input.From}
	default:
		return false
	}
	encoded, err := wipdwire.EncodeCanonical(want)
	return err == nil && bytes.Equal(raw, encoded)
}

// Already-pulled effects must belong to the accepted range too. In particular,
// a zero-effect refusal cannot hide an effect installed by an earlier pull.
func step8InstalledRangeMatches(tx *sql.Tx, entry Entry, ids []string) bool {
	rows, err := tx.Query(`SELECT event_id,record FROM installed_events ORDER BY position`)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	accepted := make(map[string]bool, len(ids))
	for _, id := range ids {
		accepted[id] = true
	}
	for rows.Next() {
		var id string
		var raw []byte
		if rows.Scan(&id, &raw) != nil {
			return false
		}
		f, err := wipdwire.DecodeCanonicalMap(raw, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			return false
		}
		if f["command_id"] == entry.Command.ID && (!accepted[id] || !eventMatchesCommand(raw, entry)) {
			return false
		}
	}
	return rows.Err() == nil
}
