package wipdseed

import (
	"fmt"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/wipdwire"
)

type step7GateProjection struct {
	Declarations []step7GateDeclaration `json:"declarations"`
	States       []step7GateState       `json:"states"`
}

type step7GateDeclaration struct {
	RepoID  string `json:"repo_id"`
	Gate    string `json:"gate"`
	Scale   string `json:"scale"`
	EventID string `json:"event_id"`
}

type step7GateState struct {
	RepoID        string `json:"repo_id"`
	NodeID        string `json:"node_id"`
	Gate          string `json:"gate"`
	Scale         string `json:"scale"`
	State         string `json:"state"`
	Reason        string `json:"reason"`
	SourceEventID string `json:"source_event_id"`
}

type step7GateNode struct {
	repo, id, kind, parent string
	birth, tombstone       uint64
}

type step7GateDeclarationAt struct {
	step7GateDeclaration
	position      uint64
	eligibleNodes map[string]bool
}

func step7ClientTransferBoundary(kind string, fields map[string]any) error {
	switch kind {
	case "config.set":
		return fmt.Errorf("%w: Repo config projection is deferred for client-state/1", ErrInvalidClientState)
	case "reference.bound", "reference.added", "reference.removed", "reference.rebound":
		return fmt.Errorf("%w: shared-reference, aggregate, and candidate projections are deferred for client-state/1", ErrInvalidClientState)
	}
	if kind == "gate.closed" || kind == "gate.dismissed" {
		payload, ok := fields["payload"].(map[string]any)
		if !ok {
			return ErrInvalidClientState
		}
		if level, exists := payload["tracker_push_level"]; exists {
			value, ok := level.(string)
			if !ok {
				return ErrInvalidClientState
			}
			if value != "off" {
				return fmt.Errorf("%w: tracker candidate projection is deferred for client-state/1", ErrInvalidClientState)
			}
		}
	}
	if step7LifecycleEvent(kind) {
		payload, ok := fields["payload"].(map[string]any)
		if !ok {
			return ErrInvalidClientState
		}
		if level, exists := payload["tracker_push_level"]; exists {
			value, ok := level.(string)
			if !ok || value != "off" {
				return fmt.Errorf("%w: tracker candidate projection is deferred for client-state/1", ErrInvalidClientState)
			}
		}
	}
	return nil
}

func step7LifecycleEvent(kind string) bool {
	scale, verb, ok := strings.Cut(kind, ".")
	return ok && (scale == "matter" || scale == "stage" || scale == "step") &&
		(verb == "started" || verb == "finished" || verb == "paused" || verb == "resumed" || verb == "canceled")
}

func foldStep7GateProjection(records []wipdwire.EventRecord, domainID string) (*step7GateProjection, error) {
	nodes := make(map[string]step7GateNode)
	events := make([]map[string]any, len(records))
	for index, record := range records {
		fields, err := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil || fields["domain_id"] != domainID || fields["event_id"] != record.EventID {
			return nil, ErrInvalidClientState
		}
		kind, ok := fields["kind"].(string)
		if !ok {
			return nil, ErrInvalidClientState
		}
		if err = step7ClientTransferBoundary(kind, fields); err != nil {
			return nil, err
		}
		events[index] = fields
		payload, ok := fields["payload"].(map[string]any)
		if !ok {
			continue
		}
		repo, _ := fields["repo_id"].(string)
		subject, _ := fields["subject_id"].(string)
		position := uint64(index + 1)
		switch kind {
		case "matter.created":
			id, _ := payload["id"].(string)
			nodes[id] = step7GateNode{repo: repo, id: id, kind: "matter", birth: position}
		case "stage.created":
			id, _ := fields["subject_id"].(string)
			parent, _ := payload["matter_id"].(string)
			nodes[id] = step7GateNode{repo: repo, id: id, kind: "stage", parent: parent, birth: position}
		case "step.created":
			id, _ := fields["subject_id"].(string)
			parent, _ := payload["parent"].(string)
			nodes[id] = step7GateNode{repo: repo, id: id, kind: "step", parent: parent, birth: position}
		case "gate.declared", "gate.closed", "gate.dismissed", "gate.exemption-repaired":
			if _, valid := decodeFoldedLifecycleEvent(fields, record, domainID); !valid {
				return nil, ErrInvalidClientState
			}
		case "step.removed":
			node, exists := nodes[subject]
			if !exists || node.kind != "step" || node.birth >= position || node.tombstone != 0 {
				return nil, ErrInvalidClientState
			}
			node.tombstone = position
			nodes[subject] = node
		case "config.set", "reference.bound", "reference.added", "reference.removed", "reference.rebound":
			// The boundary helper above returns a typed fail-closed result.
			return nil, ErrInvalidClientState
		}
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	repairTargets := make(map[string]map[string]struct{})
	for _, fields := range events {
		if fields["kind"] != "gate.exemption-repaired" {
			continue
		}
		payload, payloadOK := fields["payload"].(map[string]any)
		gate, gateOK := payload["gate"].(string)
		repo, repoOK := fields["repo_id"].(string)
		nodeID, nodeOK := fields["subject_id"].(string)
		if !payloadOK || !wipdwire.ExactMapKeys(payload, "gate") || !gateOK || gate == "" || !repoOK || !nodeOK {
			continue
		}
		key := repo + "\x00" + gate
		if repairTargets[key] == nil {
			repairTargets[key] = make(map[string]struct{})
		}
		repairTargets[key][nodeID] = struct{}{}
	}

	declarations := make(map[string]step7GateDeclarationAt)
	states := make(map[string]step7GateState)
	lifecycle := make(map[string]string, len(nodes))
	for id := range nodes {
		lifecycle[id] = "planned"
	}
	projection := &step7GateProjection{Declarations: make([]step7GateDeclaration, 0), States: make([]step7GateState, 0)}
	for index, fields := range events {
		kind, _ := fields["kind"].(string)
		payload, _ := fields["payload"].(map[string]any)
		subject, _ := fields["subject_id"].(string)
		repo, _ := fields["repo_id"].(string)
		eventID, _ := fields["event_id"].(string)
		position := uint64(index + 1)
		if step7LifecycleEvent(kind) {
			if to, ok := payload["to"].(string); ok {
				lifecycle[subject] = to
			}
		}
		switch kind {
		case "gate.declared":
			gate, _ := payload["gate"].(string)
			scale, _ := payload["scale"].(string)
			validKeys := wipdwire.ExactMapKeys(payload, "gate", "scale") || wipdwire.ExactMapKeys(payload, "gate", "scale", "exempt")
			if !validKeys || gate == "" || !step7Scale(scale) || subject != repo {
				return nil, ErrInvalidClientState
			}
			declarationKey := repo + "\x00" + gate
			if _, duplicate := declarations[declarationKey]; duplicate {
				return nil, ErrInvalidClientState
			}
			declaration := step7GateDeclarationAt{step7GateDeclaration: step7GateDeclaration{
				RepoID: repo, Gate: gate, Scale: scale, EventID: eventID,
			}, position: position}
			declarations[declarationKey] = declaration
			projection.Declarations = append(projection.Declarations, declaration.step7GateDeclaration)
			if rawExempt, exists := payload["exempt"]; exists {
				exemptions, ok := rawExempt.([]any)
				if !ok || len(exemptions) == 0 {
					return nil, ErrInvalidClientState
				}
				seen := make(map[string]bool, len(exemptions))
				for _, rawID := range exemptions {
					id, ok := rawID.(string)
					node, found := nodes[id]
					if !ok || !clientULIDPattern.MatchString(id) || !found || node.repo != repo || node.kind != scale ||
						node.birth >= position || !step7ClientNodeLiveAt(node, position) || seen[id] {
						return nil, ErrInvalidClientState
					}
					seen[id] = true
					key := id + "\x00" + gate
					if _, duplicate := states[key]; duplicate {
						return nil, ErrInvalidClientState
					}
					states[key] = step7GateState{RepoID: repo, NodeID: id, Gate: gate, Scale: scale, State: "exempt", SourceEventID: eventID}
					projection.States = append(projection.States, states[key])
				}
			}
			declaration.eligibleNodes = make(map[string]bool)
			for nodeID := range repairTargets[declarationKey] {
				if step7ClientRepairEligibleAtDeclaration(nodeID, repo, scale, gate, position, nodes, declarations, states, lifecycle) {
					declaration.eligibleNodes[nodeID] = true
				}
			}
			declarations[declarationKey] = declaration
		case "gate.closed", "gate.dismissed":
			gate, _ := payload["gate"].(string)
			scale, _ := payload["scale"].(string)
			rawLevel, hasLevel := payload["tracker_push_level"]
			level, levelOK := rawLevel.(string)
			required := []string{"gate", "scale"}
			if kind == "gate.dismissed" {
				required = append(required, "reason")
			}
			withTrackerLevel := append(append([]string(nil), required...), "tracker_push_level")
			node, found := nodes[subject]
			declaration, declared := declarations[repo+"\x00"+gate]
			key := subject + "\x00" + gate
			if !wipdwire.ExactMapKeys(payload, required...) && !wipdwire.ExactMapKeys(payload, withTrackerLevel...) ||
				gate == "" || !step7Scale(scale) ||
				hasLevel && (!levelOK || level != "off") ||
				!found || node.repo != repo || node.kind != scale || node.birth >= position || !step7ClientNodeLiveAt(node, position) ||
				!declared || declaration.Scale != scale {
				return nil, ErrInvalidClientState
			}
			if _, duplicate := states[key]; duplicate {
				return nil, ErrInvalidClientState
			}
			state, reason := "closed", ""
			if kind == "gate.dismissed" {
				reason, _ = payload["reason"].(string)
				if strings.TrimSpace(reason) == "" || lifecycle[subject] != "done" {
					return nil, ErrInvalidClientState
				}
				state = "dismissed"
			}
			states[key] = step7GateState{RepoID: repo, NodeID: subject, Gate: gate, Scale: scale, State: state, Reason: reason, SourceEventID: eventID}
			projection.States = append(projection.States, states[key])
		case "gate.exemption-repaired":
			gate, _ := payload["gate"].(string)
			node, found := nodes[subject]
			declaration, declared := declarations[repo+"\x00"+gate]
			key := subject + "\x00" + gate
			if !wipdwire.ExactMapKeys(payload, "gate") || gate == "" || !found || node.repo != repo || node.birth >= position ||
				!step7ClientNodeLiveAt(node, position) ||
				!declared || declaration.Scale != node.kind || !declaration.eligibleNodes[subject] || lifecycle[subject] != "done" {
				return nil, ErrInvalidClientState
			}
			if _, duplicate := states[key]; duplicate || !step7ClientGateSealed(subject, gate, position, nodes, declarations, states, lifecycle) {
				return nil, ErrInvalidClientState
			}
			states[key] = step7GateState{RepoID: repo, NodeID: subject, Gate: gate, Scale: declaration.Scale, State: "exempt", SourceEventID: eventID}
			projection.States = append(projection.States, states[key])
		}
	}
	if len(projection.Declarations) == 0 {
		return nil, nil
	}
	sort.Slice(projection.Declarations, func(i, j int) bool {
		left := projection.Declarations[i]
		right := projection.Declarations[j]
		if left.RepoID != right.RepoID {
			return left.RepoID < right.RepoID
		}
		return left.Gate < right.Gate
	})
	sort.Slice(projection.States, func(i, j int) bool {
		left := projection.States[i]
		right := projection.States[j]
		if left.RepoID != right.RepoID {
			return left.RepoID < right.RepoID
		}
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		return left.Gate < right.Gate
	})
	return projection, nil
}

func step7ClientGateSealed(nodeID, targetGate string, position uint64, nodes map[string]step7GateNode,
	declarations map[string]step7GateDeclarationAt, states map[string]step7GateState, lifecycle map[string]string,
) bool {
	if lifecycle[nodeID] != "done" {
		return false
	}
	for currentID := nodeID; currentID != ""; {
		current, found := nodes[currentID]
		if !found || !step7ClientNodeLiveAt(current, position) {
			return false
		}
		for _, declaration := range declarations {
			if declaration.RepoID != current.repo || declaration.Scale != current.kind || declaration.position > position ||
				currentID == nodeID && declaration.Gate == targetGate {
				continue
			}
			if _, satisfied := states[currentID+"\x00"+declaration.Gate]; !satisfied {
				return false
			}
		}
		currentID = current.parent
	}
	return true
}

func step7ClientRepairEligibleAtDeclaration(nodeID, repo, scale, gate string, position uint64, nodes map[string]step7GateNode,
	declarations map[string]step7GateDeclarationAt, states map[string]step7GateState, lifecycle map[string]string,
) bool {
	node, found := nodes[nodeID]
	return found && node.repo == repo && node.kind == scale && node.birth < position &&
		step7ClientNodeLiveAt(node, position) && lifecycle[nodeID] == "done" &&
		step7ClientGateSealed(nodeID, gate, position, nodes, declarations, states, lifecycle)
}

func step7ClientNodeLiveAt(node step7GateNode, position uint64) bool {
	return node.tombstone == 0 || node.tombstone > position
}

func step7Scale(value string) bool { return value == "matter" || value == "stage" || value == "step" }
