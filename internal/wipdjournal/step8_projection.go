package wipdjournal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/wipdwire"
)

// Step8Projection is a disposable Environment view of canonical authority
// records, never command admission or a second authority. ConfigHistory keeps
// event-time policy evidence; candidates retain their source-event identity.
type Step8Projection struct {
	Dependencies  []DependencyProjection `json:"dependencies"`
	References    []ReferenceProjection  `json:"references"`
	Aggregates    []ReferenceAggregate   `json:"aggregates"`
	Candidates    []TrackerCandidate     `json:"candidates"`
	ConfigHistory []ConfigHistoryEntry   `json:"config_history"`
}

// DependencyProjection retains the current and historical identity of one
// authority dependency edge.
type DependencyProjection struct {
	DomainID         string `json:"domain_id"`
	EdgeID           string `json:"edge_id"`
	RepoID           string `json:"repo_id"`
	BlockedID        string `json:"blocked_id"`
	BlockerID        string `json:"blocker_id"`
	BirthEventID     string `json:"birth_event_id"`
	LastEventID      string `json:"last_event_id"`
	TombstoneEventID string `json:"tombstone_event_id,omitempty"`
	Live             bool   `json:"live"`
}

// ReferenceProjection retains one Matter/reference membership history.
type ReferenceProjection struct {
	DomainID       string `json:"domain_id"`
	MatterID       string `json:"matter_id"`
	Reference      string `json:"reference"`
	BirthEventID   string `json:"birth_event_id"`
	LastEventID    string `json:"last_event_id"`
	RemovedEventID string `json:"removed_event_id,omitempty"`
}

// ReferenceAggregate summarizes active reference membership in one domain.
type ReferenceAggregate struct {
	DomainID    string `json:"domain_id"`
	Reference   string `json:"reference"`
	Disposition string `json:"disposition"`
	Members     int64  `json:"members"`
}

// TrackerCandidate records a policy-approved tracker effect candidate.
type TrackerCandidate struct {
	DomainID  string `json:"domain_id"`
	ID        string `json:"id"`
	RepoID    string `json:"repo_id"`
	Kind      string `json:"kind"`
	SubjectID string `json:"subject_id"`
	Reference string `json:"reference"`
	Key       string `json:"key"`
	Payload   string `json:"payload"`
	EventID   string `json:"event_id"`
}

// ConfigHistoryEntry records a Repo configuration value at its source event.
type ConfigHistoryEntry struct {
	RepoID  string `json:"repo_id"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	EventID string `json:"event_id"`
}

type referenceNode struct {
	repo, kind, parent, matter, title, state string
	removed                                  bool
}

// FoldStep8Projection refolds the full retained domain history. A delta alone
// cannot prove endpoint liveness, cycles, reference membership, or a policy
// snapshot. Repo membership admission belongs to the authenticated authority:
// an empty member Repo may supply dependency context without owning a node.
func FoldStep8Projection(records []wipdwire.EventRecord, domain string) (*Step8Projection, error) {
	p := &Step8Projection{}
	nodes := make(map[string]referenceNode)
	edges := make(map[string]DependencyProjection)
	refs := make(map[string]ReferenceProjection)
	config := make(map[string]string)
	declarations := make(map[string]string)
	gates := make(map[string]bool)
	narrated := make(map[string]bool)
	activeRefs := func(matter string) []string {
		var result []string
		for _, r := range refs {
			if r.MatterID == matter && r.RemovedEventID == "" {
				result = append(result, r.Reference)
			}
		}
		sort.Strings(result)
		return result
	}
	sealed := func(id string) bool {
		if nodes[id].state != "done" {
			return false
		}
		for id != "" {
			n, ok := nodes[id]
			if !ok || n.removed {
				return false
			}
			for key, scale := range declarations {
				repo, gate, _ := strings.Cut(key, "\x00")
				if repo == n.repo && scale == n.kind && !gates[id+"\x00"+gate] {
					return false
				}
			}
			id = n.parent
		}
		return true
	}
	aggregate := func(ref string) ReferenceAggregate {
		a := ReferenceAggregate{DomainID: domain, Reference: ref}
		allPlanned, completed, active := true, false, false
		for _, r := range refs {
			n, ok := nodes[r.MatterID]
			if r.Reference != ref || r.RemovedEventID != "" || !ok || n.removed {
				continue
			}
			a.Members++
			allPlanned = allPlanned && n.state == "planned"
			if n.state != "canceled" {
				if n.state == "done" && sealed(r.MatterID) {
					completed = true
				} else {
					active = true
				}
			}
		}
		if a.Members > 0 && !allPlanned {
			switch {
			case active:
				a.Disposition = "active"
			case completed:
				a.Disposition = "completed"
			default:
				a.Disposition = "canceled"
			}
		}
		return a
	}
	addCandidate := func(event, repo, kind, subject, ref, discriminator string, payload any) error {
		key := "tracker:" + event + ":" + kind + ":" + ref
		if discriminator != "" {
			key += ":" + discriminator
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		p.Candidates = append(p.Candidates, TrackerCandidate{domain, trackerCandidateID(key), repo, kind, subject, ref, key, string(encoded), event})
		return nil
	}
	previous := ""
	for _, record := range records {
		if record.EventID <= previous || !validAuthorityEvent(record.Record, domain, record.EventID) {
			return nil, ErrInvalidTransfer
		}
		previous = record.EventID
		f, err := wipdwire.DecodeCanonicalMap(record.Record, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			return nil, ErrInvalidTransfer
		}
		kind, subject, repo := asString(f["kind"]), asString(f["subject_id"]), asString(f["repo_id"])
		payload := f["payload"].(map[string]any)
		n, exists := nodes[subject]
		level, hasLevel := payload["tracker_push_level"]
		if hasLevel {
			if !trackerPushLevel(asString(level)) {
				return nil, ErrInvalidTransfer
			}
			effective, configured := config[repo+"\x00tracker.push-level"]
			if !configured {
				effective = "off"
				if config[repo+"\x00tracker.backend"] != "" {
					effective = "boundary"
				}
			}
			if !trackerPushLevel(effective) || level != effective {
				return nil, ErrInvalidTransfer
			}
		}
		affected := []string{}
		switch kind {
		case "matter.created", "stage.created", "step.created", "step.inserted":
			if exists {
				return nil, ErrInvalidTransfer
			}
			scale, _, _ := strings.Cut(kind, ".")
			parent := asString(payload["parent"])
			if scale == "stage" {
				parent = asString(payload["matter_id"])
			}
			matter := subject
			if scale != "matter" {
				ancestor, known := nodes[parent]
				if !known || ancestor.removed || ancestor.repo != repo ||
					(scale == "stage" && ancestor.kind != "matter") ||
					(scale == "step" && ancestor.kind != "matter" && ancestor.kind != "stage" && ancestor.kind != "step") {
					return nil, ErrInvalidTransfer
				}
				matter = ancestor.matter
			}
			nodes[subject] = referenceNode{repo: repo, kind: scale, parent: parent, matter: matter, title: asString(payload["title"]), state: "planned"}
		case "step.removed":
			if !exists || n.removed || n.kind != "step" || n.repo != repo {
				return nil, ErrInvalidTransfer
			}
			n.removed = true
			nodes[subject] = n
		case "step.replaced":
			if !exists || n.removed || n.kind != "step" || n.repo != repo {
				return nil, ErrInvalidTransfer
			}
			if _, known := nodes[asString(payload["replacement"])]; known {
				return nil, ErrInvalidTransfer
			}
			n.removed = true
			nodes[subject] = n
			n.removed = false
			n.state = "planned"
			n.title = asString(payload["title"])
			nodes[asString(payload["replacement"])] = n
		case "config.set":
			config[repo+"\x00"+asString(payload["key"])] = asString(payload["value"])
			p.ConfigHistory = append(p.ConfigHistory, ConfigHistoryEntry{repo, asString(payload["key"]), asString(payload["value"]), record.EventID})
		case "dependency.added", "dependency.removed":
			blocker, edgeID := asString(payload["blocker"]), asString(payload["edge"])
			other, found := nodes[blocker]
			if !exists || !found || n.removed || other.removed || subject == blocker ||
				(n.kind != "matter" && n.kind != "step") || (other.kind != "matter" && other.kind != "step") {
				return nil, ErrInvalidTransfer
			}
			prior, known := edges[edgeID]
			if kind == "dependency.removed" {
				if !known || prior.TombstoneEventID != "" || prior.BlockedID != subject || prior.BlockerID != blocker {
					return nil, ErrInvalidTransfer
				}
				prior.TombstoneEventID, prior.LastEventID = record.EventID, record.EventID
				edges[edgeID] = prior
				break
			}
			if known {
				return nil, ErrInvalidTransfer
			}
			adjacency := make(map[string][]string)
			for _, edge := range edges {
				if edge.TombstoneEventID != "" || nodes[edge.BlockedID].removed || nodes[edge.BlockerID].removed {
					continue
				}
				if edge.BlockedID == subject && edge.BlockerID == blocker {
					return nil, ErrInvalidTransfer
				}
				adjacency[edge.BlockedID] = append(adjacency[edge.BlockedID], edge.BlockerID)
			}
			pending, seen := []string{blocker}, make(map[string]bool)
			for len(pending) != 0 {
				id := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				if id == subject {
					return nil, ErrInvalidTransfer
				}
				if !seen[id] {
					seen[id] = true
					pending = append(pending, adjacency[id]...)
				}
			}
			edges[edgeID] = DependencyProjection{DomainID: domain, EdgeID: edgeID, RepoID: repo, BlockedID: subject, BlockerID: blocker, BirthEventID: record.EventID, LastEventID: record.EventID}
		case "gate.declared":
			gate := asString(payload["gate"])
			if _, known := declarations[repo+"\x00"+gate]; known {
				return nil, ErrInvalidTransfer
			}
			declarations[repo+"\x00"+gate] = asString(payload["scale"])
			if exempt, ok := payload["exempt"].([]any); ok {
				for _, id := range exempt {
					node, known := nodes[asString(id)]
					if !known || node.removed || node.repo != repo || node.kind != payload["scale"] {
						return nil, ErrInvalidTransfer
					}
					gates[asString(id)+"\x00"+gate] = true
				}
			}
		case "gate.closed", "gate.dismissed", "gate.exemption-repaired":
			gate := asString(payload["gate"])
			if !exists || n.removed || n.repo != repo || declarations[repo+"\x00"+gate] != n.kind || gates[subject+"\x00"+gate] ||
				(kind != "gate.exemption-repaired" && payload["scale"] != n.kind) || (kind == "gate.dismissed" && n.state != "done") {
				return nil, ErrInvalidTransfer
			}
			gates[subject+"\x00"+gate] = true
		case "reference.bound", "reference.added", "reference.removed", "reference.rebound":
			if !exists || n.kind != "matter" || n.repo != repo || n.removed {
				return nil, ErrInvalidTransfer
			}
			ref, from := asString(payload["ref"]), ""
			if kind == "reference.rebound" {
				ref, from = asString(payload["to"]), asString(payload["from"])
			}
			key := subject + "\x00" + ref
			prior, known := refs[key]
			switch kind {
			case "reference.bound":
				for k, r := range refs {
					if r.MatterID == subject && r.Reference != ref && r.RemovedEventID == "" {
						r.RemovedEventID, r.LastEventID = record.EventID, record.EventID
						refs[k] = r
					}
				}
			case "reference.removed":
				if !known || prior.RemovedEventID != "" {
					return nil, ErrInvalidTransfer
				}
				prior.RemovedEventID, prior.LastEventID = record.EventID, record.EventID
				refs[key] = prior
			default:
				if known && prior.RemovedEventID == "" {
					return nil, ErrInvalidTransfer
				}
				if from != "" {
					k := subject + "\x00" + from
					r, ok := refs[k]
					if !ok || r.RemovedEventID != "" {
						return nil, ErrInvalidTransfer
					}
					r.RemovedEventID, r.LastEventID = record.EventID, record.EventID
					refs[k] = r
				}
			}
			if kind != "reference.removed" {
				birth := record.EventID
				if known {
					birth = prior.BirthEventID
				}
				refs[key] = ReferenceProjection{domain, subject, ref, birth, record.EventID, ""}
			}
			affected = append(affected, ref)
			if from != "" {
				affected = append([]string{from}, affected...)
			}
		default:
			if transferLifecycleEventKind(kind) && exists {
				scale, _, _ := strings.Cut(kind, ".")
				if n.removed || n.repo != repo || n.kind != scale || n.state != payload["from"] {
					return nil, ErrInvalidTransfer
				}
				n.state = asString(payload["to"])
				nodes[subject] = n
			}
		}
		if !hasLevel || level == "off" {
			continue
		}
		if !exists && kind != "config.set" {
			return nil, ErrInvalidTransfer
		}
		if kind == "matter.started" || kind == "matter.canceled" || kind == "matter.finished" && sealed(subject) ||
			(kind == "gate.closed" || kind == "gate.dismissed") && n.kind == "matter" && sealed(subject) {
			affected = activeRefs(n.matter)
		}
		for _, ref := range affected {
			a := aggregate(ref)
			if a.Members > 0 && a.Disposition != "" {
				if err := addCandidate(record.EventID, repo, "state", subject, ref, "", map[string]string{"disposition": a.Disposition}); err != nil {
					return nil, err
				}
			}
		}
		if level == "narrated" && (kind == "stage.finished" || kind == "gate.closed" || kind == "gate.dismissed") {
			var stages []string
			switch n.kind {
			case "stage":
				stages = append(stages, subject)
			case "matter":
				for id, stage := range nodes {
					if stage.kind == "stage" && stage.matter == subject {
						stages = append(stages, id)
					}
				}
			}
			sort.Strings(stages)
			for _, id := range stages {
				stage := nodes[id]
				if !sealed(id) || narrated[id] {
					continue
				}
				active := activeRefs(stage.matter)
				for _, ref := range active {
					payload := struct {
						Stage  string `json:"stage"`
						Title  string `json:"title"`
						Action string `json:"action"`
					}{id, stage.title, "closed"}
					if err := addCandidate(record.EventID, stage.repo, "comment", stage.matter, ref, id, payload); err != nil {
						return nil, err
					}
				}
				if len(active) > 0 {
					narrated[id] = true
				}
			}
		}
	}
	for _, edge := range edges {
		edge.Live = edge.TombstoneEventID == "" && !nodes[edge.BlockedID].removed && !nodes[edge.BlockerID].removed
		p.Dependencies = append(p.Dependencies, edge)
	}
	active := make(map[string]bool)
	for _, ref := range refs {
		p.References = append(p.References, ref)
		if ref.RemovedEventID == "" {
			active[ref.Reference] = true
		}
	}
	for ref := range active {
		if a := aggregate(ref); a.Members > 0 {
			p.Aggregates = append(p.Aggregates, a)
		}
	}
	sort.Slice(p.Dependencies, func(i, j int) bool { return p.Dependencies[i].EdgeID < p.Dependencies[j].EdgeID })
	sort.Slice(p.References, func(i, j int) bool {
		a, b := p.References[i], p.References[j]
		return a.MatterID+"\x00"+a.Reference < b.MatterID+"\x00"+b.Reference
	})
	sort.Slice(p.Aggregates, func(i, j int) bool { return p.Aggregates[i].Reference < p.Aggregates[j].Reference })
	sort.Slice(p.Candidates, func(i, j int) bool { return p.Candidates[i].ID < p.Candidates[j].ID })
	if len(p.Dependencies)+len(p.References)+len(p.ConfigHistory)+len(p.Candidates) == 0 {
		return nil, nil
	}
	return p, nil
}

func trackerPushLevel(level string) bool {
	return level == "off" || level == "boundary" || level == "narrated"
}

func trackerCandidateID(key string) string {
	sum := sha256.Sum256([]byte(key))
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	out := make([]byte, 26)
	out[0] = alphabet[(sum[0]&0xe0)>>5]
	bit := uint(3)
	for i := 1; i < len(out); i++ {
		var v byte
		for j := 0; j < 5; j++ {
			v = v<<1 | (sum[bit>>3]>>(7-(bit&7)))&1
			bit++
		}
		out[i] = alphabet[v]
	}
	return string(out)
}

func installedStep8Projection(tx *sql.Tx, domain string) (*Step8Projection, error) {
	rows, err := tx.Query(`SELECT event_id,record FROM installed_events ORDER BY position`)
	if err != nil {
		return nil, err
	}
	var records []wipdwire.EventRecord
	for rows.Next() {
		var r wipdwire.EventRecord
		if err = rows.Scan(&r.EventID, &r.Record); err != nil {
			break
		}
		records = append(records, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return FoldStep8Projection(records, domain)
}
