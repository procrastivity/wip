package authoritystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

type step13Config struct {
	domain, repo, key, value, event string
}

type step13Declaration struct {
	domain, repo, gate, scale, event string
}

type step13GateState struct {
	domain, repo, node, gate, scale, state, reason, event string
}

type step13Reference struct {
	domain, matter, ref, removed, birth, last string
}

type step13AggregateRow struct {
	domain, ref, disposition string
	members                  int64
}

type step13Candidate struct {
	domain, id, repo, kind, subject, ref, key, payload, event string
}

type step13Projection struct {
	config       []step13Config
	declarations []step13Declaration
	gateStates   []step13GateState
	references   []step13Reference
	aggregates   []step13AggregateRow
	candidates   []step13Candidate
}

type step13Event struct {
	step12Event
}

type step13Node struct {
	node         step12Node
	birthPos     uint64
	tombstonePos int64
}

type step13ProjectionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func checkStep13State(db *sql.DB) error {
	nodes, err := step13Nodes(db)
	if err != nil {
		return err
	}
	events, err := step13Events(db)
	if err != nil {
		return err
	}
	want, err := deriveStep13Projection(nodes, events)
	if err != nil {
		return fmt.Errorf("%w: Step 7 projection fold: %v", ErrInvalidStore, err)
	}
	got, err := readStep13Projection(db)
	if err != nil {
		return err
	}
	if !sameStep13Projection(got, want) {
		return fmt.Errorf("%w: Step 7 projections differ from the event fold", ErrInvalidStore)
	}
	if err := checkStep13GateCommands(db); err != nil {
		return err
	}
	return nil
}

func rebuildStep13Projection(db *sql.DB) error {
	nodes, err := step13Nodes(db)
	if err != nil {
		return err
	}
	events, err := step13Events(db)
	if err != nil {
		return err
	}
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"m6_tracker_candidates", "m6_tracker_aggregates", "m6_tracker_references", "m6_gate_states", "m6_gate_declarations", "m6_repo_config"} {
		if _, err = tx.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	for _, value := range projection.config {
		if _, err = tx.Exec(`INSERT INTO m6_repo_config VALUES(?,?,?,?,?)`, value.domain, value.repo, value.key, value.value, value.event); err != nil {
			return err
		}
	}
	for _, value := range projection.declarations {
		if _, err = tx.Exec(`INSERT INTO m6_gate_declarations VALUES(?,?,?,?,?)`, value.domain, value.repo, value.gate, value.scale, value.event); err != nil {
			return err
		}
	}
	for _, value := range projection.gateStates {
		if _, err = tx.Exec(`INSERT INTO m6_gate_states VALUES(?,?,?,?,?,?,?,?)`, value.domain, value.repo, value.node, value.gate, value.scale, value.state, nullableString(value.reason), value.event); err != nil {
			return err
		}
	}
	for _, value := range projection.references {
		if _, err = tx.Exec(`INSERT INTO m6_tracker_references VALUES(?,?,?,?,?,?)`, value.domain, value.matter, value.ref, nullableString(value.removed), value.birth, value.last); err != nil {
			return err
		}
	}
	for _, value := range projection.aggregates {
		if _, err = tx.Exec(`INSERT INTO m6_tracker_aggregates VALUES(?,?,?,?)`, value.domain, value.ref, nullableString(value.disposition), value.members); err != nil {
			return err
		}
	}
	for _, value := range projection.candidates {
		if _, err = tx.Exec(`INSERT INTO m6_tracker_candidates VALUES(?,?,?,?,?,?,?,?,?)`, value.domain, value.id, value.repo, value.kind, value.subject, value.ref, value.key, value.payload, value.event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func step13Nodes(db *sql.DB) (map[string]step13Node, error) {
	rows, err := db.Query(`SELECT n.domain_id,n.node_id,n.kind,n.repo_id,n.matter_id,coalesce(n.parent_id,''),n.locator,n.title,n.sort_key,
		n.birth_event_id,n.last_event_id,coalesce(n.tombstone_event_id,''),n.repair_required,coalesce(n.requested_locator,''),e.position,t.position
		FROM m6_nodes n JOIN authority_events e ON e.domain_id=n.domain_id AND e.event_id=n.birth_event_id
		LEFT JOIN authority_events t ON t.domain_id=n.domain_id AND t.event_id=n.tombstone_event_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	nodes := make(map[string]step13Node)
	for rows.Next() {
		var node step12Node
		var repair int
		var position uint64
		var tombstonePosition sql.NullInt64
		if err := rows.Scan(&node.domain, &node.id, &node.kind, &node.repo, &node.matter, &node.parent,
			&node.locator, &node.title, &node.sortKey, &node.birth, &node.last, &node.tombstone, &repair, &node.requested, &position, &tombstonePosition); err != nil {
			return nil, err
		}
		node.repair = repair == 1
		item := step13Node{node: node, birthPos: position}
		if tombstonePosition.Valid {
			item.tombstonePos = tombstonePosition.Int64
		}
		nodes[ownerKey(node.domain, node.id)] = item
	}
	return nodes, rows.Err()
}

func step13Events(db *sql.DB) ([]step13Event, error) {
	rows, err := db.Query(`SELECT domain_id,position,event_id,command_id,record FROM authority_events ORDER BY domain_id,position`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []step13Event
	for rows.Next() {
		var domain, id, command string
		var position uint64
		var raw []byte
		if err := rows.Scan(&domain, &position, &id, &command, &raw); err != nil {
			return nil, err
		}
		event, err := parseStep12Event(raw, domain, position, id, command)
		if err != nil {
			return nil, err
		}
		events = append(events, step13Event{step12Event: event})
	}
	return events, rows.Err()
}

func step13EffectivePushLevel(config map[string]step13Config, domain, repo string) (string, error) {
	key := ownerKey(domain, repo) + "/"
	if level, exists := config[key+"tracker.push-level"]; exists {
		if !referencePushLevel(level.value) {
			return "", ErrInvalidStore
		}
		return level.value, nil
	}
	if config[key+"tracker.backend"].value != "" {
		return "boundary", nil
	}
	return "off", nil
}

func deriveStep13Projection(nodes map[string]step13Node, events []step13Event) (step13Projection, error) {
	config := make(map[string]step13Config)
	declarations := make(map[string]step13Declaration)
	states := make(map[string]step13GateState)
	references := make(map[string]step13Reference)
	lifecycle := make(map[string]string)
	for key := range nodes {
		lifecycle[key] = "planned"
	}
	var candidates []step13Candidate
	narrated := make(map[string]bool)

	for _, event := range events {
		subjectKey := ownerKey(event.domain, event.subject)
		node, isNode := nodes[subjectKey]
		if from, to, scale, level, ok := step13LifecycleTransition(event.kind, event.payload); ok {
			if from == "" || to == "" || !isNode || node.node.kind != scale || node.node.repo != event.repo || node.birthPos >= event.position ||
				!step13NodeLiveAt(node, event.position) || lifecycle[subjectKey] != from {
				return step13Projection{}, ErrInvalidStore
			}
			lifecycle[subjectKey] = to
			if err := step13LifecycleCandidates(&candidates, narrated, nodes, lifecycle, declarations, states, references, event, node, level); err != nil {
				return step13Projection{}, err
			}
			continue
		}
		switch event.kind {
		case "config.set":
			var payload struct {
				Key   string `cbor:"key"`
				Value string `cbor:"value"`
			}
			if !step13ClosedPayload(event.payload, &payload, []string{"key", "value"}) || payload.Key == "" || event.subject != event.repo {
				return step13Projection{}, ErrInvalidStore
			}
			key := ownerKey(event.domain, event.repo) + "/" + payload.Key
			config[key] = step13Config{event.domain, event.repo, payload.Key, payload.Value, event.id}

		case "gate.declared":
			var payload struct {
				Gate   string   `cbor:"gate"`
				Scale  string   `cbor:"scale"`
				Exempt []string `cbor:"exempt"`
			}
			if !step13ClosedPayload(event.payload, &payload, []string{"gate", "scale"}, []string{"exempt"}) ||
				payload.Gate == "" || !step13Scale(payload.Scale) || event.subject != event.repo {
				return step13Projection{}, ErrInvalidStore
			}
			key := ownerKey(event.domain, event.repo) + "/" + payload.Gate
			if _, exists := declarations[key]; exists {
				return step13Projection{}, ErrInvalidStore
			}
			declarations[key] = step13Declaration{event.domain, event.repo, payload.Gate, payload.Scale, event.id}
			seen := make(map[string]bool)
			for _, id := range payload.Exempt {
				exempt, ok := nodes[ownerKey(event.domain, id)]
				stateKey := ownerKey(event.domain, id) + "/" + payload.Gate
				if !ok || exempt.node.repo != event.repo || exempt.node.kind != payload.Scale || exempt.birthPos >= event.position || !step13NodeLiveAt(exempt, event.position) || seen[id] {
					return step13Projection{}, ErrInvalidStore
				}
				seen[id] = true
				if _, exists := states[stateKey]; exists {
					return step13Projection{}, ErrInvalidStore
				}
				states[stateKey] = step13GateState{event.domain, event.repo, id, payload.Gate, payload.Scale, "exempt", "", event.id}
			}

		case "gate.exemption-repaired":
			var payload struct {
				Gate string `cbor:"gate"`
			}
			if !step13ClosedPayload(event.payload, &payload, []string{"gate"}) || payload.Gate == "" || !isNode || node.node.repo != event.repo || node.birthPos >= event.position || !step13NodeLiveAt(node, event.position) {
				return step13Projection{}, ErrInvalidStore
			}
			declaration, ok := declarations[ownerKey(event.domain, event.repo)+"/"+payload.Gate]
			stateKey := subjectKey + "/" + payload.Gate
			if !ok || declaration.scale != node.node.kind {
				return step13Projection{}, ErrInvalidStore
			}
			if _, exists := states[stateKey]; exists {
				return step13Projection{}, ErrInvalidStore
			}
			states[stateKey] = step13GateState{event.domain, event.repo, event.subject, payload.Gate, declaration.scale, "exempt", "", event.id}

		case "gate.closed", "gate.dismissed":
			var payload struct {
				Gate             string `cbor:"gate"`
				Scale            string `cbor:"scale"`
				Reason           string `cbor:"reason"`
				TrackerPushLevel string `cbor:"tracker_push_level"`
			}
			required := []string{"gate", "scale"}
			if event.kind == "gate.dismissed" {
				required = append(required, "reason")
			}
			if !step13ClosedPayload(event.payload, &payload, required, []string{"tracker_push_level"}) ||
				payload.Gate == "" || !step13Scale(payload.Scale) || !isNode || node.node.repo != event.repo ||
				node.node.kind != payload.Scale || node.birthPos >= event.position {
				return step13Projection{}, ErrInvalidStore
			}
			declaration, declared := declarations[ownerKey(event.domain, event.repo)+"/"+payload.Gate]
			stateKey := subjectKey + "/" + payload.Gate
			if !declared || declaration.scale != payload.Scale || !step13NodeLiveAt(node, event.position) {
				return step13Projection{}, ErrInvalidStore
			}
			if _, exists := states[stateKey]; exists {
				return step13Projection{}, ErrInvalidStore
			}
			state, reason := "closed", ""
			if event.kind == "gate.dismissed" {
				if strings.TrimSpace(payload.Reason) == "" || lifecycle[subjectKey] != "done" {
					return step13Projection{}, ErrInvalidStore
				}
				state, reason = "dismissed", payload.Reason
			}
			states[stateKey] = step13GateState{event.domain, event.repo, event.subject, payload.Gate, payload.Scale, state, reason, event.id}
			if err := step13GateCandidates(&candidates, narrated, nodes, lifecycle, declarations, states, references, event, node, payload.TrackerPushLevel); err != nil {
				return step13Projection{}, err
			}

		case "reference.bound", "reference.added", "reference.removed", "reference.rebound":
			if !isNode || node.node.kind != "matter" || node.node.repo != event.repo || node.birthPos >= event.position || !step13NodeLiveAt(node, event.position) {
				return step13Projection{}, ErrInvalidStore
			}
			var payload struct {
				Ref              string `cbor:"ref"`
				From             string `cbor:"from"`
				To               string `cbor:"to"`
				TrackerPushLevel string `cbor:"tracker_push_level"`
			}
			required := []string{"ref"}
			var optional [][]string
			if event.kind == "reference.rebound" {
				required = []string{"from", "to"}
				optional = [][]string{{"tracker_push_level"}}
			} else if event.kind != "reference.bound" {
				optional = [][]string{{"tracker_push_level"}}
			}
			if !step13ClosedPayload(event.payload, &payload, required, optional...) {
				return step13Projection{}, ErrInvalidStore
			}
			if payload.TrackerPushLevel != "" {
				level, err := step13EffectivePushLevel(config, event.domain, event.repo)
				if err != nil || payload.TrackerPushLevel != level {
					return step13Projection{}, ErrInvalidStore
				}
			}
			refs := []string{payload.Ref}
			if event.kind == "reference.rebound" {
				if payload.From == "" || payload.To == "" || payload.From == payload.To {
					return step13Projection{}, ErrInvalidStore
				}
				refs = []string{payload.From, payload.To}
			} else if payload.Ref == "" {
				return step13Projection{}, ErrInvalidStore
			}
			if !step13PushLevel(payload.TrackerPushLevel) {
				return step13Projection{}, ErrInvalidStore
			}
			switch event.kind {
			case "reference.bound":
				for key, prior := range references {
					if prior.domain == event.domain && prior.matter == event.subject && prior.removed == "" && prior.ref != payload.Ref {
						prior.removed, prior.last = event.id, event.id
						references[key] = prior
					}
				}
				key := ownerKey(event.domain, event.subject) + "/" + payload.Ref
				prior, exists := references[key]
				birth := event.id
				if exists {
					birth = prior.birth
				}
				references[key] = step13Reference{event.domain, event.subject, payload.Ref, "", birth, event.id}
			case "reference.added":
				key := ownerKey(event.domain, event.subject) + "/" + payload.Ref
				prior, exists := references[key]
				if exists && prior.removed == "" {
					return step13Projection{}, ErrInvalidStore
				}
				birth := event.id
				if exists {
					birth = prior.birth
				}
				references[key] = step13Reference{event.domain, event.subject, payload.Ref, "", birth, event.id}
			case "reference.removed":
				key := ownerKey(event.domain, event.subject) + "/" + payload.Ref
				prior, exists := references[key]
				if !exists || prior.removed != "" {
					return step13Projection{}, ErrInvalidStore
				}
				prior.removed, prior.last = event.id, event.id
				references[key] = prior
			default:
				fromKey := ownerKey(event.domain, event.subject) + "/" + payload.From
				from, exists := references[fromKey]
				if !exists || from.removed != "" {
					return step13Projection{}, ErrInvalidStore
				}
				toKey := ownerKey(event.domain, event.subject) + "/" + payload.To
				to, exists := references[toKey]
				if exists && to.removed == "" {
					return step13Projection{}, ErrInvalidStore
				}
				from.removed, from.last = event.id, event.id
				references[fromKey] = from
				birth := event.id
				if exists {
					birth = to.birth
				}
				references[toKey] = step13Reference{event.domain, event.subject, payload.To, "", birth, event.id}
			}
			if payload.TrackerPushLevel != "" && payload.TrackerPushLevel != "off" {
				for _, ref := range refs {
					if disposition, found, members := step13Aggregate(event.domain, ref, nodes, lifecycle, declarations, states, references, event.position); found && disposition != "" {
						if err := addStep13Candidate(&candidates, event, node.node.repo, "state", event.subject, ref,
							map[string]string{"disposition": disposition}, ""); err != nil {
							return step13Projection{}, err
						} else if members < 1 {
							return step13Projection{}, ErrInvalidStore
						}
					}
				}
			}
		}
	}

	projection := step13Projection{}
	for _, value := range config {
		projection.config = append(projection.config, value)
	}
	for _, value := range declarations {
		projection.declarations = append(projection.declarations, value)
	}
	for _, value := range states {
		projection.gateStates = append(projection.gateStates, value)
	}
	for _, value := range references {
		projection.references = append(projection.references, value)
	}
	for _, ref := range step13ActiveRefs(references) {
		disposition, found, members := step13Aggregate(ref.domain, ref.ref, nodes, lifecycle, declarations, states, references)
		if found {
			projection.aggregates = append(projection.aggregates, step13AggregateRow{ref.domain, ref.ref, disposition, members})
		}
	}
	projection.candidates = candidates
	sortStep13Projection(&projection)
	return projection, nil
}

func step13GateCandidates(candidates *[]step13Candidate, narrated map[string]bool, nodes map[string]step13Node,
	lifecycle map[string]string, declarations map[string]step13Declaration, states map[string]step13GateState,
	references map[string]step13Reference, event step13Event, subject step13Node, level string,
) error {
	if !step13PushLevel(level) {
		return ErrInvalidStore
	}
	if level == "" || level == "off" {
		return nil
	}
	matterID := subject.node.matter
	if subject.node.kind == "matter" {
		matterID = subject.node.id
	}
	if subject.node.kind == "matter" && step13Sealed(event.domain, subject.node.id, nodes, lifecycle, declarations, states, event.position) {
		for _, ref := range step13RefsForMatter(event.domain, matterID, references) {
			disposition, found, _ := step13Aggregate(event.domain, ref, nodes, lifecycle, declarations, states, references, event.position)
			if found && disposition != "" {
				if err := addStep13Candidate(candidates, event, subject.node.repo, "state", matterID, ref, map[string]string{"disposition": disposition}, ""); err != nil {
					return err
				}
			}
		}
	}
	if level != "narrated" {
		return nil
	}
	var stages []step13Node
	switch subject.node.kind {
	case "stage":
		stages = append(stages, subject)
	case "matter":
		for _, node := range nodes {
			if node.node.domain == event.domain && node.node.matter == matterID && node.node.kind == "stage" {
				stages = append(stages, node)
			}
		}
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].node.id < stages[j].node.id })
	for _, stage := range stages {
		if lifecycle[ownerKey(event.domain, stage.node.id)] != "done" || !step13NodeLiveAt(stage, event.position) ||
			!step13Sealed(event.domain, stage.node.id, nodes, lifecycle, declarations, states, event.position) {
			continue
		}
		if err := step13StageComments(candidates, narrated, references, event, stage, matterID); err != nil {
			return err
		}
	}
	return nil
}

func step13LifecycleCandidates(candidates *[]step13Candidate, narrated map[string]bool, nodes map[string]step13Node,
	lifecycle map[string]string, declarations map[string]step13Declaration, states map[string]step13GateState,
	references map[string]step13Reference, event step13Event, subject step13Node, level string,
) error {
	if !step13PushLevel(level) {
		return ErrInvalidStore
	}
	if level == "" || level == "off" {
		return nil
	}
	switch event.kind {
	case "matter.started", "matter.canceled":
		if subject.node.kind == "matter" {
			return step13StateCandidates(candidates, nodes, lifecycle, declarations, states, references, event, subject.node.id)
		}
	case "matter.finished":
		if subject.node.kind == "matter" && step13Sealed(event.domain, subject.node.id, nodes, lifecycle, declarations, states, event.position) {
			return step13StateCandidates(candidates, nodes, lifecycle, declarations, states, references, event, subject.node.id)
		}
	case "stage.finished":
		if subject.node.kind == "stage" && level == "narrated" && step13NodeLiveAt(subject, event.position) &&
			step13Sealed(event.domain, subject.node.id, nodes, lifecycle, declarations, states, event.position) {
			return step13StageComments(candidates, narrated, references, event, subject, subject.node.matter)
		}
	}
	return nil
}

func step13StateCandidates(candidates *[]step13Candidate, nodes map[string]step13Node, lifecycle map[string]string,
	declarations map[string]step13Declaration, states map[string]step13GateState, references map[string]step13Reference,
	event step13Event, matterID string,
) error {
	for _, ref := range step13RefsForMatter(event.domain, matterID, references) {
		disposition, found, _ := step13Aggregate(event.domain, ref, nodes, lifecycle, declarations, states, references, event.position)
		if found && disposition != "" {
			if err := addStep13Candidate(candidates, event, event.repo, "state", matterID, ref, map[string]string{"disposition": disposition}, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func step13StageComments(candidates *[]step13Candidate, narrated map[string]bool, references map[string]step13Reference,
	event step13Event, stage step13Node, matterID string,
) error {
	stageKey := ownerKey(event.domain, matterID) + "/" + stage.node.id
	if narrated[stageKey] {
		return nil
	}
	refs := step13RefsForMatter(event.domain, matterID, references)
	for _, ref := range refs {
		payload := struct {
			Stage  string `json:"stage"`
			Title  string `json:"title"`
			Action string `json:"action"`
		}{stage.node.id, stage.node.title, "closed"}
		if err := addStep13Candidate(candidates, event, stage.node.repo, "comment", matterID, ref, payload, stage.node.id); err != nil {
			return err
		}
	}
	if len(refs) != 0 {
		narrated[stageKey] = true
	}
	return nil
}

func step13Aggregate(domain, ref string, nodes map[string]step13Node, lifecycle map[string]string,
	declarations map[string]step13Declaration, states map[string]step13GateState, references map[string]step13Reference,
	atPosition ...uint64,
) (string, bool, int64) {
	position := ^uint64(0)
	if len(atPosition) != 0 {
		position = atPosition[0]
	}
	var members []string
	for _, reference := range references {
		matter, exists := nodes[ownerKey(domain, reference.matter)]
		if reference.domain == domain && reference.ref == ref && reference.removed == "" && exists && matter.node.kind == "matter" &&
			(matter.tombstonePos == 0 || uint64(matter.tombstonePos) > position) {
			members = append(members, reference.matter)
		}
	}
	if len(members) == 0 {
		return "", false, 0
	}
	sort.Strings(members)
	allPlanned := true
	for _, matter := range members {
		if lifecycle[ownerKey(domain, matter)] != "planned" {
			allPlanned = false
			break
		}
	}
	if allPlanned {
		return "", true, int64(len(members))
	}
	sealedCount := 0
	for _, matter := range members {
		state := lifecycle[ownerKey(domain, matter)]
		if state == "canceled" {
			continue
		}
		if state != "done" || !step13Sealed(domain, matter, nodes, lifecycle, declarations, states, position) {
			return "active", true, int64(len(members))
		}
		sealedCount++
	}
	if sealedCount > 0 {
		return "completed", true, int64(len(members))
	}
	return "canceled", true, int64(len(members))
}

func step13NodeLiveAt(node step13Node, position uint64) bool {
	return node.tombstonePos == 0 || uint64(node.tombstonePos) > position
}

func step13Sealed(domain, id string, nodes map[string]step13Node, lifecycle map[string]string, declarations map[string]step13Declaration,
	states map[string]step13GateState, atPosition ...uint64,
) bool {
	position := ^uint64(0)
	if len(atPosition) != 0 {
		position = atPosition[0]
	}
	key := ownerKey(domain, id)
	if lifecycle[key] != "done" {
		return false
	}
	current, ok := nodes[key]
	if !ok || !step13NodeLiveAt(current, position) {
		return false
	}
	for {
		for _, declaration := range declarations {
			if declaration.domain != current.node.domain || declaration.repo != current.node.repo || declaration.scale != current.node.kind {
				continue
			}
			if _, satisfied := states[ownerKey(current.node.domain, current.node.id)+"/"+declaration.gate]; !satisfied {
				return false
			}
		}
		if current.node.parent == "" {
			return true
		}
		current, ok = nodes[ownerKey(current.node.domain, current.node.parent)]
		if !ok || !step13NodeLiveAt(current, position) {
			return false
		}
	}
}

func step13ProjectedSealed(ctx context.Context, queryer step13ProjectionQueryer, nodes map[string]step13Node,
	domain, id string, before uint64,
) (bool, error) {
	declarations := make(map[string]step13Declaration)
	rows, err := queryer.QueryContext(ctx, `SELECT d.domain_id,d.repo_id,d.gate,d.scale,d.declaration_event_id
		FROM m6_gate_declarations d JOIN authority_events e ON e.domain_id=d.domain_id AND e.event_id=d.declaration_event_id
		WHERE d.domain_id=? AND e.position<?`, domain, before)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var declaration step13Declaration
		if err = rows.Scan(&declaration.domain, &declaration.repo, &declaration.gate, &declaration.scale, &declaration.event); err != nil {
			_ = rows.Close()
			return false, err
		}
		declarations[ownerKey(declaration.domain, declaration.repo)+"/"+declaration.gate] = declaration
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	states := make(map[string]step13GateState)
	rows, err = queryer.QueryContext(ctx, `SELECT g.domain_id,g.repo_id,g.node_id,g.gate,g.scale,g.state,coalesce(g.reason,''),g.source_event_id
		FROM m6_gate_states g JOIN authority_events e ON e.domain_id=g.domain_id AND e.event_id=g.source_event_id
		WHERE g.domain_id=? AND e.position<?`, domain, before)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var state step13GateState
		if err = rows.Scan(&state.domain, &state.repo, &state.node, &state.gate, &state.scale, &state.state, &state.reason, &state.event); err != nil {
			_ = rows.Close()
			return false, err
		}
		states[ownerKey(state.domain, state.node)+"/"+state.gate] = state
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	return step13Sealed(domain, id, nodes, map[string]string{ownerKey(domain, id): "done"}, declarations, states, before), nil
}

func step13RefsForMatter(domain, matter string, references map[string]step13Reference) []string {
	var refs []string
	for _, ref := range references {
		if ref.domain == domain && ref.matter == matter && ref.removed == "" {
			refs = append(refs, ref.ref)
		}
	}
	sort.Strings(refs)
	return refs
}

func step13ActiveRefs(references map[string]step13Reference) []step13Reference {
	var refs []step13Reference
	seen := make(map[string]bool)
	for _, reference := range references {
		key := ownerKey(reference.domain, reference.ref)
		if reference.removed == "" && !seen[key] {
			seen[key] = true
			refs = append(refs, reference)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].domain != refs[j].domain {
			return refs[i].domain < refs[j].domain
		}
		return refs[i].ref < refs[j].ref
	})
	return refs
}

func addStep13Candidate(candidates *[]step13Candidate, event step13Event, repo, kind, subject, ref string, payload any, discriminator string) error {
	key := "tracker:" + event.id + ":" + kind + ":" + ref
	if discriminator != "" {
		key += ":" + discriminator
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	*candidates = append(*candidates, step13Candidate{
		domain: event.domain, id: step13CandidateID(key), repo: repo, kind: kind,
		subject: subject, ref: ref, key: key, payload: string(encoded), event: event.id,
	})
	return nil
}

func step13CandidateID(key string) string {
	sum := sha256.Sum256([]byte(key))
	var raw [16]byte
	copy(raw[:], sum[:16])
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	out := make([]byte, 26)
	out[0] = alphabet[(raw[0]&0xe0)>>5]
	bitPos := uint(3)
	for i := 1; i < len(out); i++ {
		var value byte
		for j := 0; j < 5; j++ {
			byteIndex := bitPos >> 3
			bitIndex := 7 - (bitPos & 7)
			value = value<<1 | (raw[byteIndex]>>bitIndex)&1
			bitPos++
		}
		out[i] = alphabet[value]
	}
	return string(out)
}

func step13LifecycleTransition(kind string, fields map[string]cbor.RawMessage) (from, to, scale, level string, ok bool) {
	scale, verb, found := strings.Cut(kind, ".")
	if !found || (scale != "matter" && scale != "stage" && scale != "step") {
		return "", "", "", "", false
	}
	target := ""
	switch verb {
	case "started":
		from, target = "planned", "in-progress"
	case "finished":
		from, target = "in-progress", "done"
	case "paused":
		from, target = "in-progress", "paused"
	case "resumed":
		from, target = "paused", "in-progress"
	case "canceled":
		from, target = "in-progress", "canceled"
	default:
		return "", "", "", "", false
	}
	var payload struct {
		From             string `cbor:"from"`
		To               string `cbor:"to"`
		TrackerPushLevel string `cbor:"tracker_push_level"`
	}
	if !step13ClosedPayload(fields, &payload, []string{"from", "to"}, []string{"cascade", "tracker_push_level", "reason", "cause_event_id"}) {
		return "", "", scale, "", true
	}
	if payload.From != from || payload.To != target || !step13PushLevel(payload.TrackerPushLevel) {
		return "", "", scale, "", true
	}
	return payload.From, payload.To, scale, payload.TrackerPushLevel, true
}

func step13ClosedPayload(fields map[string]cbor.RawMessage, value any, required []string, optional ...[]string) bool {
	allowed := make(map[string]bool)
	requiredFields := make(map[string]bool, len(required))
	for _, key := range required {
		allowed[key] = true
		requiredFields[key] = true
		if _, exists := fields[key]; !exists {
			return false
		}
	}
	for _, group := range optional {
		for _, key := range group {
			allowed[key] = true
		}
	}
	for key := range fields {
		if !allowed[key] {
			return false
		}
		if bytes.Equal(fields[key], []byte{0xf6}) || bytes.Equal(fields[key], []byte{0xf7}) {
			return false
		}
		if !requiredFields[key] && !step13OptionalPayloadValueIsNonzero(key, fields[key]) {
			return false
		}
	}
	return decodeStep13Map(fields, value) == nil
}

func step13OptionalPayloadValueIsNonzero(key string, raw cbor.RawMessage) bool {
	switch key {
	case "exempt":
		var value []string
		return artifactDecoder.Unmarshal(raw, &value) == nil && len(value) != 0
	case "tracker_push_level", "reason":
		var value string
		return artifactDecoder.Unmarshal(raw, &value) == nil && value != ""
	case "cascade":
		var value bool
		return artifactDecoder.Unmarshal(raw, &value) == nil && value
	default:
		return true
	}
}

func decodeStep13Map(fields map[string]cbor.RawMessage, value any) error {
	encoded, err := artifactEncoder.Marshal(fields)
	if err != nil {
		return err
	}
	return artifactDecoder.Unmarshal(encoded, value)
}

func step13Scale(value string) bool { return value == "matter" || value == "stage" || value == "step" }

func step13PushLevel(value string) bool {
	return value == "" || value == "off" || value == "boundary" || value == "narrated"
}

func readStep13Projection(db *sql.DB) (step13Projection, error) {
	return readStep13ProjectionTx(context.Background(), db)
}

func readStep13ProjectionTx(ctx context.Context, queryer step13ProjectionQueryer) (step13Projection, error) {
	var out step13Projection
	queries := []string{
		`SELECT domain_id,repo_id,config_key,value,last_event_id FROM m6_repo_config`,
		`SELECT domain_id,repo_id,gate,scale,declaration_event_id FROM m6_gate_declarations`,
		`SELECT domain_id,repo_id,node_id,gate,scale,state,coalesce(reason,''),source_event_id FROM m6_gate_states`,
		`SELECT domain_id,matter_id,ref,coalesce(removed_event_id,''),birth_event_id,last_event_id FROM m6_tracker_references`,
		`SELECT domain_id,ref,coalesce(disposition,''),member_count FROM m6_tracker_aggregates`,
		`SELECT domain_id,candidate_id,repo_id,kind,subject_id,ref,idempotency_key,payload,birth_event_id FROM m6_tracker_candidates`,
	}
	for index, query := range queries {
		rows, err := queryer.QueryContext(ctx, query)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			switch index {
			case 0:
				var value step13Config
				err = rows.Scan(&value.domain, &value.repo, &value.key, &value.value, &value.event)
				out.config = append(out.config, value)
			case 1:
				var value step13Declaration
				err = rows.Scan(&value.domain, &value.repo, &value.gate, &value.scale, &value.event)
				out.declarations = append(out.declarations, value)
			case 2:
				var value step13GateState
				err = rows.Scan(&value.domain, &value.repo, &value.node, &value.gate, &value.scale, &value.state, &value.reason, &value.event)
				out.gateStates = append(out.gateStates, value)
			case 3:
				var value step13Reference
				err = rows.Scan(&value.domain, &value.matter, &value.ref, &value.removed, &value.birth, &value.last)
				out.references = append(out.references, value)
			case 4:
				var value step13AggregateRow
				err = rows.Scan(&value.domain, &value.ref, &value.disposition, &value.members)
				out.aggregates = append(out.aggregates, value)
			case 5:
				var value step13Candidate
				err = rows.Scan(&value.domain, &value.id, &value.repo, &value.kind, &value.subject, &value.ref, &value.key, &value.payload, &value.event)
				out.candidates = append(out.candidates, value)
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return out, err
		}
	}
	sortStep13Projection(&out)
	return out, nil
}

func sameStep13Projection(a, b step13Projection) bool {
	return reflect.DeepEqual(a, b)
}

func sortStep13Projection(value *step13Projection) {
	sort.Slice(value.config, func(i, j int) bool {
		return value.config[i].domain+"/"+value.config[i].repo+"/"+value.config[i].key < value.config[j].domain+"/"+value.config[j].repo+"/"+value.config[j].key
	})
	sort.Slice(value.declarations, func(i, j int) bool {
		return value.declarations[i].domain+"/"+value.declarations[i].repo+"/"+value.declarations[i].gate < value.declarations[j].domain+"/"+value.declarations[j].repo+"/"+value.declarations[j].gate
	})
	sort.Slice(value.gateStates, func(i, j int) bool {
		return value.gateStates[i].domain+"/"+value.gateStates[i].node+"/"+value.gateStates[i].gate < value.gateStates[j].domain+"/"+value.gateStates[j].node+"/"+value.gateStates[j].gate
	})
	sort.Slice(value.references, func(i, j int) bool {
		return value.references[i].domain+"/"+value.references[i].matter+"/"+value.references[i].ref < value.references[j].domain+"/"+value.references[j].matter+"/"+value.references[j].ref
	})
	sort.Slice(value.aggregates, func(i, j int) bool {
		return value.aggregates[i].domain+"/"+value.aggregates[i].ref < value.aggregates[j].domain+"/"+value.aggregates[j].ref
	})
	sort.Slice(value.candidates, func(i, j int) bool {
		return value.candidates[i].domain+"/"+value.candidates[i].id < value.candidates[j].domain+"/"+value.candidates[j].id
	})
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
