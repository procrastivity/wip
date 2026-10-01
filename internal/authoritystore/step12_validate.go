package authoritystore

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

type step12Node struct {
	domain, id, kind, repo, matter, parent string
	locator, title, birth, last, tombstone string
	sortKey                                int64
	repair                                 bool
	requested                              string
}

type step12Event struct {
	domain, id, command, hash, kind, subject, repo string
	acted, occurred                                string
	environment                                    string
	sequence, position                             uint64
	payload                                        map[string]cbor.RawMessage
}

func checkStep12State(db *sql.DB) error {
	submissionRows, err := db.Query(`SELECT domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
		FROM submissions ORDER BY domain_id,environment_id,environment_sequence`)
	if err != nil {
		return err
	}
	submissions := make(map[string]storedSubmission)
	for submissionRows.Next() {
		var value storedSubmission
		if err = submissionRows.Scan(&value.domain, &value.id, &value.hash, &value.command, &value.epoch,
			&value.env, &value.seq, &value.operation, &value.version, &value.state); err != nil {
			break
		}
		submissions[ownerKey(value.domain, value.id)] = value
	}
	if err == nil {
		err = submissionRows.Err()
	}
	_ = submissionRows.Close()
	if err != nil {
		return err
	}

	nodes := make(map[string]step12Node)
	outputs := make(map[string]operation.Output)
	createdMatters := make(map[string]struct {
		requested, assigned string
		repair              bool
	})
	rows, err := db.Query(`SELECT domain_id,position,event_id,command_id,record FROM authority_events ORDER BY domain_id,position`)
	if err != nil {
		return err
	}
	var currentKey string
	var group []step12Event
	var groups [][]step12Event
	flush := func(group []step12Event) error {
		if len(group) == 0 {
			return nil
		}
		key := ownerKey(group[0].domain, group[0].command)
		submission, ok := submissions[key]
		if !ok {
			return ErrInvalidStore
		}
		id := operation.ID{Name: submission.operation, Version: uint16(submission.version)}
		if !step12Relevant(id) {
			return nil
		}
		command, decodeErr := operation.DecodeCanonicalCommand(submission.command)
		definition, defined := step12Definition(id)
		if decodeErr != nil || !defined || definition.ValidateRequest(command.Request) != nil ||
			command.ID != submission.id || command.AuthorityDomainID != submission.domain || command.EnvironmentID != submission.env ||
			command.EnvironmentSequence != submission.seq || command.ExpectedAuthorityEpoch != submission.epoch ||
			command.Request.Operation.Name != submission.operation || uint64(command.Request.Operation.Version) != submission.version {
			return ErrInvalidStore
		}
		if connectedLifecycleOperation(id) {
			for _, event := range group {
				if event.kind == "batch.swept" {
					continue
				}
				node, exists := nodes[ownerKey(event.domain, event.subject)]
				scale, _, validKind := strings.Cut(event.kind, ".")
				if !exists || node.tombstone != "" || !validKind || scale != node.kind {
					return ErrInvalidStore
				}
				node.last = event.id
				nodes[ownerKey(node.domain, node.id)] = node
			}
			return nil
		}
		for _, event := range group {
			if err := validateStep12Event(event, submission, command); err != nil {
				return err
			}
			output, err := foldStep12Event(nodes, createdMatters, event, command)
			if err != nil {
				return err
			}
			if output != nil {
				outputs[key] = output
			}
		}
		if err := validateStep12EventSequence(id, group); err != nil {
			return err
		}
		if id == operation.MatterCreateV2.Metadata().Operation {
			created := createdMatters[key]
			if created.repair != (len(group) == 2) {
				return ErrInvalidStore
			}
		}
		if submission.state == "submitted" {
			return ErrInvalidStore
		}
		var receiptBytes []byte
		if err := db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&receiptBytes); err != nil {
			return ErrInvalidStore
		}
		receipt, err := readReceipt(receiptBytes)
		if err != nil {
			return ErrInvalidStore
		}
		if receipt.Result.Code == string(operation.ResultSucceeded) && operation.Step4Operation(id) {
			want, exists := outputs[key]
			if id == operation.MatterCreateV2.Metadata().Operation {
				input := command.Request.Input.(operation.MatterCreateInput)
				created := createdMatters[key]
				var matterID string
				for _, event := range group {
					if event.kind == "matter.created" {
						matterID = event.subject
						break
					}
				}
				if matterID == "" || created.requested == "" || created.assigned == "" {
					return ErrInvalidStore
				}
				want = operation.MatterCreateV2Output{
					ID: matterID, Title: input.Title,
					RequestedLocator: created.requested, AssignedLocator: created.assigned, LocatorRepairRequired: created.repair,
				}
				exists = true
			}
			if !exists || receipt.Range == nil || receipt.Range.Count != uint64(len(group)) {
				return ErrInvalidStore
			}
			encoded, encodeErr := step4OutputBytes(want)
			if encodeErr != nil || string(encoded) != string(receipt.Result.Output) {
				return ErrInvalidStore
			}
		} else if receipt.Result.Code != string(operation.ResultSucceeded) && len(group) != 0 {
			return ErrInvalidStore
		}
		return nil
	}

	for rows.Next() {
		var domain, eventID, commandID string
		var position uint64
		var raw []byte
		if err = rows.Scan(&domain, &position, &eventID, &commandID, &raw); err != nil {
			break
		}
		key := ownerKey(domain, commandID)
		if currentKey != "" && key != currentKey {
			if len(group) != 0 {
				groups = append(groups, group)
				group = nil
			}
		}
		currentKey = key
		submission, ok := submissions[key]
		if !ok {
			err = ErrInvalidStore
			break
		}
		if !step12Relevant(operation.ID{Name: submission.operation, Version: uint16(submission.version)}) {
			continue
		}
		event, parseErr := parseStep12Event(raw, domain, position, eventID, commandID)
		if parseErr != nil {
			err = parseErr
			break
		}
		group = append(group, event)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return ErrInvalidStore
	}
	if len(group) != 0 {
		groups = append(groups, group)
	}
	for _, events := range groups {
		if err = flush(events); err != nil {
			return err
		}
	}

	storedRows, err := db.Query(`SELECT domain_id,node_id,kind,repo_id,matter_id,coalesce(parent_id,''),locator,title,sort_key,
		birth_event_id,last_event_id,coalesce(tombstone_event_id,''),repair_required,coalesce(requested_locator,'') FROM m6_nodes`)
	if err != nil {
		return err
	}
	stored := make(map[string]step12Node)
	for storedRows.Next() {
		var node step12Node
		var repair int
		if err = storedRows.Scan(&node.domain, &node.id, &node.kind, &node.repo, &node.matter, &node.parent,
			&node.locator, &node.title, &node.sortKey, &node.birth, &node.last, &node.tombstone, &repair, &node.requested); err != nil {
			break
		}
		node.repair = repair == 1
		stored[ownerKey(node.domain, node.id)] = node
	}
	if err == nil {
		err = storedRows.Err()
	}
	_ = storedRows.Close()
	if err != nil || len(stored) != len(nodes) {
		return ErrInvalidStore
	}
	for key, want := range nodes {
		if got, ok := stored[key]; !ok || got != want {
			return fmt.Errorf("%w: M6 node projection differs from event fold for %s", ErrInvalidStore, key)
		}
	}
	return nil
}

func step12Relevant(id operation.ID) bool {
	return id == operation.MatterCreateV1.Metadata().Operation || id == operation.StepCreateV1.Metadata().Operation || operation.Step4Operation(id) || connectedLifecycleOperation(id)
}

func step12Definition(id operation.ID) (operation.Definition, bool) {
	for _, definition := range operation.Catalogue() {
		if definition.Metadata().Operation == id {
			return definition, true
		}
	}
	return operation.Definition{}, false
}

// matterSnapshotItemsAt folds Matter births and locator repairs through the
// snapshot's retained event prefix. The legacy matters table stores the latest
// locator, while an immutable snapshot may intentionally predate a repair.
func matterSnapshotItemsAt(db *sql.DB, domain string, count uint64) ([]SnapshotItem, error) {
	rows, err := db.Query(`SELECT e.position,e.event_id,e.command_id,e.record,
		s.domain_id,s.command_id,s.request_hash,s.command,s.epoch,s.environment_id,s.environment_sequence,s.operation_name,s.operation_version,s.state
		FROM authority_events e JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id
		WHERE e.domain_id=? AND e.position<=? ORDER BY e.position`, domain, count)
	if err != nil {
		return nil, err
	}
	type matter struct{ id, repo, locator, title, birth string }
	matters := make(map[string]matter)
	for rows.Next() {
		var position uint64
		var eventID, eventCommand string
		var record []byte
		var submission storedSubmission
		if err = rows.Scan(&position, &eventID, &eventCommand, &record, &submission.domain, &submission.id,
			&submission.hash, &submission.command, &submission.epoch, &submission.env, &submission.seq,
			&submission.operation, &submission.version, &submission.state); err != nil {
			break
		}
		id := operation.ID{Name: submission.operation, Version: uint16(submission.version)}
		if !step12Relevant(id) {
			continue
		}
		command, decodeErr := operation.DecodeCanonicalCommand(submission.command)
		definition, defined := step12Definition(id)
		event, parseErr := parseStep12Event(record, domain, position, eventID, eventCommand)
		if decodeErr != nil || !defined || definition.ValidateRequest(command.Request) != nil || parseErr != nil ||
			validateStep12Event(event, submission, command) != nil {
			err = ErrInvalidStore
			break
		}
		switch event.kind {
		case "matter.created":
			if !exactKeys(event.payload, "id", "locator", "title") {
				err = ErrInvalidStore
				break
			}
			var value matter
			if artifactDecoder.Unmarshal(event.payload["id"], &value.id) != nil ||
				artifactDecoder.Unmarshal(event.payload["locator"], &value.locator) != nil ||
				artifactDecoder.Unmarshal(event.payload["title"], &value.title) != nil ||
				value.id != event.subject || value.id == "" {
				err = ErrInvalidStore
				break
			}
			value.repo, value.birth = event.repo, event.id
			if _, exists := matters[value.id]; exists {
				err = ErrInvalidStore
				break
			}
			matters[value.id] = value
		case "matter.locator-repaired":
			if !exactKeys(event.payload, "action", "requested_locator", "previous_locator", "assigned_locator") {
				err = ErrInvalidStore
				break
			}
			var previous, assigned string
			if artifactDecoder.Unmarshal(event.payload["previous_locator"], &previous) != nil ||
				artifactDecoder.Unmarshal(event.payload["assigned_locator"], &assigned) != nil {
				err = ErrInvalidStore
				break
			}
			value, exists := matters[event.subject]
			if !exists || value.repo != event.repo || value.locator != previous || assigned == "" {
				err = ErrInvalidStore
				break
			}
			value.locator = assigned
			matters[event.subject] = value
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
		return nil, err
	}
	ordered := make([]matter, 0, len(matters))
	for _, value := range matters {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].locator != ordered[j].locator {
			return ordered[i].locator < ordered[j].locator
		}
		return ordered[i].id < ordered[j].id
	})
	items := make([]SnapshotItem, 0, len(ordered))
	for _, value := range ordered {
		encoded, encodeErr := artifactEncoder.Marshal(map[string]any{
			"id": value.id, "repo_id": value.repo, "locator": value.locator,
			"title": value.title, "birth_event_id": value.birth,
		})
		if encodeErr != nil {
			return nil, encodeErr
		}
		items = append(items, SnapshotItem{ID: value.id, Value: encoded})
	}
	return items, nil
}

func parseStep12Event(raw []byte, domain string, position uint64, id, command string) (step12Event, error) {
	var event step12Event
	event.domain, event.position, event.id, event.command = domain, position, id, command
	var envelope struct {
		Schema      string `cbor:"schema"`
		ID          string `cbor:"event_id"`
		Domain      string `cbor:"domain_id"`
		Command     string `cbor:"command_id"`
		Hash        string `cbor:"request_hash"`
		Kind        string `cbor:"kind"`
		Subject     string `cbor:"subject_id"`
		Repo        string `cbor:"repo_id"`
		Acted       string `cbor:"acted_at"`
		Occurred    string `cbor:"occurred_at"`
		Environment struct {
			ID       string `cbor:"id"`
			Sequence uint64 `cbor:"sequence"`
		} `cbor:"environment"`
	}
	if closedPayload(raw, &envelope, "schema", "event_id", "domain_id", "command_id", "request_hash", "kind", "subject_id", "repo_id", "acted_at", "occurred_at", "environment", "payload") != nil {
		return event, ErrInvalidStore
	}
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !exactKeys(fields, "schema", "event_id", "domain_id", "command_id", "request_hash", "kind", "subject_id", "repo_id", "acted_at", "occurred_at", "environment", "payload") {
		return event, ErrInvalidStore
	}
	var env map[string]cbor.RawMessage
	if canonicalDecode(fields["environment"], &env) != nil || !exactKeys(env, "id", "sequence") ||
		canonicalDecode(fields["payload"], &event.payload) != nil {
		return event, ErrInvalidStore
	}
	event.hash, event.kind, event.subject, event.repo = envelope.Hash, envelope.Kind, envelope.Subject, envelope.Repo
	event.acted, event.occurred, event.environment, event.sequence = envelope.Acted, envelope.Occurred, envelope.Environment.ID, envelope.Environment.Sequence
	if envelope.Schema != "wipd.event/1" || envelope.ID != id || envelope.Domain != domain || envelope.Command != command ||
		!ulid.MatchString(id) || !ulid.MatchString(command) || !validDigest(event.hash) || !ulid.MatchString(event.repo) ||
		!ulid.MatchString(event.subject) || !ulid.MatchString(event.environment) || event.sequence == 0 || event.acted == "" {
		return event, ErrInvalidStore
	}
	if _, err := utcTime(event.occurred); err != nil {
		return event, ErrInvalidStore
	}
	return event, nil
}

func validateStep12Event(event step12Event, submission storedSubmission, command operation.Command) error {
	if event.domain != submission.domain || event.command != submission.id || event.hash != submission.hash || event.repo != command.Request.Context.Repo ||
		event.environment != submission.env || event.sequence != submission.seq || event.acted != command.ActedAt {
		return ErrInvalidStore
	}
	id := command.Request.Operation
	allowed := false
	switch id {
	case operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateV2.Metadata().Operation:
		allowed = event.kind == "matter.created" || id == operation.MatterCreateV2.Metadata().Operation && event.kind == "matter.locator-repair-required"
	case operation.StageCreateV1.Metadata().Operation:
		allowed = event.kind == "stage.created"
	case operation.StepCreateV1.Metadata().Operation, operation.StepCreateV2.Metadata().Operation:
		allowed = event.kind == "step.created"
	case operation.StepInsertV1.Metadata().Operation:
		allowed = event.kind == "step.inserted" || event.kind == "step.reordered"
	case operation.StepReorderV1.Metadata().Operation:
		allowed = event.kind == "step.reordered"
	case operation.StepReplaceV1.Metadata().Operation:
		allowed = event.kind == "step.replaced"
	case operation.StepRemoveV1.Metadata().Operation:
		allowed = event.kind == "step.removed"
	case operation.MatterLocatorRepairV1.Metadata().Operation:
		allowed = event.kind == "matter.locator-repaired"
	}
	if !allowed {
		return ErrInvalidStore
	}
	return nil
}

func validateStep12EventSequence(id operation.ID, events []step12Event) error {
	var want []string
	switch id {
	case operation.MatterCreateV1.Metadata().Operation:
		want = []string{"matter.created"}
	case operation.MatterCreateV2.Metadata().Operation:
		if len(events) == 1 {
			want = []string{"matter.created"}
		} else {
			want = []string{"matter.created", "matter.locator-repair-required"}
		}
	case operation.StageCreateV1.Metadata().Operation:
		want = []string{"stage.created"}
	case operation.StepCreateV1.Metadata().Operation, operation.StepCreateV2.Metadata().Operation:
		want = []string{"step.created"}
	case operation.StepInsertV1.Metadata().Operation:
		if len(events) == 1 {
			want = []string{"step.inserted"}
		} else {
			want = []string{"step.reordered", "step.inserted"}
		}
	case operation.StepReorderV1.Metadata().Operation:
		want = []string{"step.reordered"}
	case operation.StepReplaceV1.Metadata().Operation:
		want = []string{"step.replaced"}
	case operation.StepRemoveV1.Metadata().Operation:
		want = []string{"step.removed"}
	case operation.MatterLocatorRepairV1.Metadata().Operation:
		want = []string{"matter.locator-repaired"}
	default:
		return ErrInvalidStore
	}
	if len(events) != len(want) {
		return ErrInvalidStore
	}
	for index, kind := range want {
		if events[index].kind != kind {
			return ErrInvalidStore
		}
	}
	return nil
}

func foldStep12Event(nodes map[string]step12Node, creates map[string]struct {
	requested, assigned string
	repair              bool
}, event step12Event, command operation.Command,
) (operation.Output, error) {
	get := func(id string) (step12Node, bool) { node, ok := nodes[ownerKey(event.domain, id)]; return node, ok }
	put := func(node step12Node) { nodes[ownerKey(node.domain, node.id)] = node }
	stringField := func(name string) (string, error) {
		var value string
		if raw, ok := event.payload[name]; !ok || artifactDecoder.Unmarshal(raw, &value) != nil {
			return "", ErrInvalidStore
		}
		return value, nil
	}
	intField := func(name string) (int64, error) {
		var value int64
		if raw, ok := event.payload[name]; !ok || artifactDecoder.Unmarshal(raw, &value) != nil {
			return 0, ErrInvalidStore
		}
		return value, nil
	}
	var result operation.Output
	switch event.kind {
	case "matter.created":
		input, ok := command.Request.Input.(operation.MatterCreateInput)
		if !ok || (command.Request.Operation != operation.MatterCreateV1.Metadata().Operation && command.Request.Operation != operation.MatterCreateV2.Metadata().Operation) ||
			!exactKeys(event.payload, "id", "locator", "title") {
			return nil, ErrInvalidStore
		}
		id, err := stringField("id")
		if err != nil || id != event.subject {
			return nil, ErrInvalidStore
		}
		locator, err := stringField("locator")
		if err != nil {
			return nil, err
		}
		title, err := stringField("title")
		if err != nil || title != input.Title {
			return nil, ErrInvalidStore
		}
		requested := input.Locator
		if requested == "" {
			requested = MatterLocator(input.Title)
		}
		assigned := requested
		if command.Request.Operation == operation.MatterCreateV2.Metadata().Operation {
			candidate, repair, candidateErr := expectedStep12MatterLocator(nodes, event.domain, command.Request.Context.Repo, requested, id)
			if candidateErr != nil || candidate != locator {
				return nil, ErrInvalidStore
			}
			assigned = candidate
			creates[ownerKey(event.domain, command.ID)] = struct {
				requested, assigned string
				repair              bool
			}{requested, assigned, repair}
		} else if locator != assigned {
			return nil, ErrInvalidStore
		}
		if _, exists := get(id); exists {
			return nil, ErrInvalidStore
		}
		put(step12Node{
			domain: event.domain, id: id, kind: "matter", repo: event.repo, matter: id,
			locator: locator, title: title, sortKey: 0, birth: event.id, last: event.id,
		})
		if command.Request.Operation == operation.MatterCreateV2.Metadata().Operation {
			return nil, nil
		}
		return nil, nil
	case "matter.locator-repair-required":
		if command.Request.Operation != operation.MatterCreateV2.Metadata().Operation || !exactKeys(event.payload, "requested_locator", "assigned_locator") {
			return nil, ErrInvalidStore
		}
		node, ok := get(event.subject)
		requested, err := stringField("requested_locator")
		if err != nil {
			return nil, err
		}
		assigned, err := stringField("assigned_locator")
		create := creates[ownerKey(event.domain, command.ID)]
		if err != nil || !ok || node.kind != "matter" || node.repair || requested != create.requested || assigned != node.locator || !create.repair {
			return nil, ErrInvalidStore
		}
		node.repair, node.requested, node.last = true, requested, event.id
		put(node)
		creates[ownerKey(event.domain, command.ID)] = struct {
			requested, assigned string
			repair              bool
		}{create.requested, create.assigned, true}
		return nil, nil
	case "stage.created":
		input, ok := command.Request.Input.(operation.StageCreateInput)
		if !ok || !exactKeys(event.payload, "matter_id", "locator", "title", "sort_key") {
			return nil, ErrInvalidStore
		}
		parent, exists := get(input.MatterID)
		matterID, e1 := stringField("matter_id")
		locator, e2 := stringField("locator")
		title, e3 := stringField("title")
		sortKey, e4 := intField("sort_key")
		wantLocator := MatterLocator(input.Title)
		if !exists || parent.kind != "matter" || parent.tombstone != "" || matterID != input.MatterID || title != input.Title || locator != wantLocator ||
			sortKey != step12NextSort(nodes, event.domain, input.MatterID) || e1 != nil || e2 != nil || e3 != nil || e4 != nil || step12LocatorExists(nodes, event.domain, input.MatterID, "stage", locator) {
			return nil, ErrInvalidStore
		}
		if _, exists = get(event.subject); exists {
			return nil, ErrInvalidStore
		}
		node := step12Node{
			domain: event.domain, id: event.subject, kind: "stage", repo: event.repo, matter: input.MatterID,
			parent: input.MatterID, locator: locator, title: title, sortKey: sortKey, birth: event.id, last: event.id,
		}
		put(node)
		return operation.StageCreateOutput{ID: node.id, MatterID: node.matter, Locator: node.locator, Title: node.title, SortKey: node.sortKey, State: "planned"}, nil
	case "step.created", "step.inserted":
		input, validInput := command.Request.Input.(operation.StepCreateInput)
		if event.kind == "step.inserted" {
			insert, ok := command.Request.Input.(operation.StepInsertInput)
			if !ok {
				return nil, ErrInvalidStore
			}
			input = operation.StepCreateInput{ParentID: insert.ParentID, Title: insert.Title}
			validInput = true
		}
		if !validInput || !exactKeys(event.payload, "title", "locator", "parent", "sort_key") {
			return nil, ErrInvalidStore
		}
		parent, exists := get(input.ParentID)
		title, e1 := stringField("title")
		locator, e2 := stringField("locator")
		parentID, e3 := stringField("parent")
		sortKey, e4 := intField("sort_key")
		if !exists || parent.tombstone != "" || (parent.kind != "matter" && parent.kind != "stage") ||
			parentID != input.ParentID || title != input.Title || locator != step12NextStepLocator(nodes, event.domain, parent.matter) || e1 != nil || e2 != nil || e3 != nil || e4 != nil ||
			step12LocatorExists(nodes, event.domain, parent.matter, "step", locator) {
			return nil, ErrInvalidStore
		}
		if event.kind == "step.created" && command.Request.Operation == operation.StepCreateV1.Metadata().Operation && parent.kind != "matter" {
			return nil, ErrInvalidStore
		}
		if event.kind == "step.created" && sortKey != step12NextSort(nodes, event.domain, input.ParentID) {
			return nil, ErrInvalidStore
		}
		if _, exists = get(event.subject); exists {
			return nil, ErrInvalidStore
		}
		node := step12Node{
			domain: event.domain, id: event.subject, kind: "step", repo: event.repo, matter: parent.matter,
			parent: input.ParentID, locator: locator, title: title, sortKey: sortKey, birth: event.id, last: event.id,
		}
		put(node)
		result = operation.StepCreateOutput{
			ID: node.id, ParentID: node.parent, MatterID: node.matter, Locator: node.locator,
			Title: node.title, SortKey: node.sortKey, State: "planned",
		}
		if event.kind == "step.inserted" {
			insert := command.Request.Input.(operation.StepInsertInput)
			if !step12InsertionOrderValid(nodes, event.domain, insert, event.subject) {
				return nil, ErrInvalidStore
			}
		}
		return result, nil
	case "step.reordered":
		var order []string
		if !exactKeys(event.payload, "order") || artifactDecoder.Unmarshal(event.payload["order"], &order) != nil || len(order) == 0 {
			return nil, ErrInvalidStore
		}
		parentID := event.subject
		if input, ok := command.Request.Input.(operation.StepReorderInput); ok {
			if input.ParentID != parentID || !equalStrings(input.Order, order) {
				return nil, ErrInvalidStore
			}
		} else if input, ok := command.Request.Input.(operation.StepInsertInput); ok {
			if input.ParentID != parentID || !equalStrings(step12SiblingOrder(nodes, event.domain, parentID), order) {
				return nil, ErrInvalidStore
			}
		} else {
			return nil, ErrInvalidStore
		}
		current := step12SiblingOrder(nodes, event.domain, parentID)
		if len(current) != len(order) || !sameStringSet(current, order) {
			return nil, ErrInvalidStore
		}
		base := step12NonStepSort(nodes, event.domain, parentID)
		for index, id := range order {
			node, ok := get(id)
			if !ok || node.kind != "step" || node.parent != parentID || node.tombstone != "" {
				return nil, ErrInvalidStore
			}
			node.sortKey = base + int64(index+1)*1000
			node.last = event.id
			put(node)
		}
		if input, ok := command.Request.Input.(operation.StepReorderInput); ok {
			return operation.StepReorderOutput{ParentID: parentID, Order: append([]string(nil), input.Order...)}, nil
		}
		return nil, nil
	case "step.replaced":
		input, ok := command.Request.Input.(operation.StepReplaceInput)
		if !ok || event.subject != input.StepID || !exactKeys(event.payload, "replacement", "title", "locator") {
			return nil, ErrInvalidStore
		}
		old, exists := get(input.StepID)
		replacement, e1 := stringField("replacement")
		title, e2 := stringField("title")
		locator, e3 := stringField("locator")
		if !exists || old.kind != "step" || old.tombstone != "" || title != input.Title || locator != step12NextStepLocator(nodes, event.domain, old.matter) ||
			e1 != nil || e2 != nil || e3 != nil || step12LocatorExists(nodes, event.domain, old.matter, "step", locator) {
			return nil, ErrInvalidStore
		}
		if _, exists = get(replacement); exists {
			return nil, ErrInvalidStore
		}
		old.tombstone, old.last = event.id, event.id
		put(old)
		node := step12Node{
			domain: old.domain, id: replacement, kind: "step", repo: old.repo, matter: old.matter, parent: old.parent,
			locator: locator, title: title, sortKey: old.sortKey, birth: event.id, last: event.id,
		}
		put(node)
		return operation.StepReplaceOutput{RemovedStepID: old.id, Replacement: operation.StepCreateOutput{
			ID: node.id, ParentID: node.parent,
			MatterID: node.matter, Locator: node.locator, Title: node.title, SortKey: node.sortKey, State: "planned",
		}}, nil
	case "step.removed":
		input, ok := command.Request.Input.(operation.StepRemoveInput)
		if !ok || event.subject != input.StepID || !exactKeys(event.payload, "reason") {
			return nil, ErrInvalidStore
		}
		reason, err := stringField("reason")
		node, exists := get(input.StepID)
		if err != nil || reason != input.Reason || !exists || node.kind != "step" || node.tombstone != "" {
			return nil, ErrInvalidStore
		}
		node.tombstone, node.last = event.id, event.id
		put(node)
		return operation.StepRemoveOutput{StepID: node.id}, nil
	case "matter.locator-repaired":
		input, ok := command.Request.Input.(operation.MatterLocatorRepairInput)
		if !ok || event.subject != input.MatterID || !exactKeys(event.payload, "action", "requested_locator", "previous_locator", "assigned_locator") {
			return nil, ErrInvalidStore
		}
		node, exists := get(input.MatterID)
		action, e1 := stringField("action")
		requested, e2 := stringField("requested_locator")
		previous, e3 := stringField("previous_locator")
		assigned, e4 := stringField("assigned_locator")
		if !exists || node.kind != "matter" || !node.repair || action != input.Action || assigned != input.AssignedLocator ||
			requested != node.requested || previous != node.locator || e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			return nil, ErrInvalidStore
		}
		if action == "accept" && assigned != previous || action == "rename" && (assigned == previous || step12OtherMatterLocator(nodes, event.domain, node.repo, assigned, node.id)) {
			return nil, ErrInvalidStore
		}
		node.locator, node.repair, node.requested, node.last = assigned, false, "", event.id
		put(node)
		return operation.MatterLocatorRepairOutput{ID: node.id, Action: action, RequestedLocator: requested, PreviousLocator: previous, AssignedLocator: assigned}, nil
	default:
		return nil, ErrInvalidStore
	}
}

func expectedStep12MatterLocator(nodes map[string]step12Node, domain, repo, requested, id string) (string, bool, error) {
	if requested == "" {
		return "", false, nil
	}
	if !step12OtherMatterLocator(nodes, domain, repo, requested, "") {
		return requested, false, nil
	}
	for width := 6; width <= len(id); width++ {
		candidate := requested + "-" + strings.ToLower(id[:width])
		if !step12OtherMatterLocator(nodes, domain, repo, candidate, "") {
			return candidate, true, nil
		}
	}
	for counter := uint64(0); ; counter++ {
		candidate := requested + "-" + locatorSuffix(id, counter)
		if !step12OtherMatterLocator(nodes, domain, repo, candidate, "") {
			return candidate, true, nil
		}
		if counter == ^uint64(0) {
			return "", false, ErrResourceLimit
		}
	}
}

func step12OtherMatterLocator(nodes map[string]step12Node, domain, repo, locator, except string) bool {
	for _, node := range nodes {
		if node.domain == domain && node.kind == "matter" && node.repo == repo && node.locator == locator && node.id != except {
			return true
		}
	}
	return false
}

func step12LocatorExists(nodes map[string]step12Node, domain, matter, kind, locator string) bool {
	for _, node := range nodes {
		if node.domain == domain && node.matter == matter && node.kind == kind && node.locator == locator {
			return true
		}
	}
	return false
}

func step12NextStepLocator(nodes map[string]step12Node, domain, matter string) string {
	count := 0
	for _, node := range nodes {
		if node.domain == domain && node.matter == matter && node.kind == "step" {
			count++
		}
	}
	for offset := 1; ; offset++ {
		locator := fmt.Sprintf("step-%02d", count+offset)
		if !step12LocatorExists(nodes, domain, matter, "step", locator) {
			return locator
		}
	}
}

func step12NextSort(nodes map[string]step12Node, domain, parent string) int64 {
	maximum := int64(0)
	for _, node := range nodes {
		if node.domain == domain && node.parent == parent && node.tombstone == "" && node.sortKey > maximum {
			maximum = node.sortKey
		}
	}
	return maximum + 1000
}

func step12NonStepSort(nodes map[string]step12Node, domain, parent string) int64 {
	maximum := int64(0)
	for _, node := range nodes {
		if node.domain == domain && node.parent == parent && node.kind != "step" && node.tombstone == "" && node.sortKey > maximum {
			maximum = node.sortKey
		}
	}
	return maximum
}

func step12SiblingOrder(nodes map[string]step12Node, domain, parent string) []string {
	var siblings []step12Node
	for _, node := range nodes {
		if node.domain == domain && node.parent == parent && node.kind == "step" && node.tombstone == "" {
			siblings = append(siblings, node)
		}
	}
	for i := 1; i < len(siblings); i++ {
		for j := i; j > 0 && (siblings[j].sortKey < siblings[j-1].sortKey || siblings[j].sortKey == siblings[j-1].sortKey && siblings[j].id < siblings[j-1].id); j-- {
			siblings[j], siblings[j-1] = siblings[j-1], siblings[j]
		}
	}
	result := make([]string, len(siblings))
	for i, sibling := range siblings {
		result[i] = sibling.id
	}
	return result
}

func step12InsertionOrderValid(nodes map[string]step12Node, domain string, input operation.StepInsertInput, inserted string) bool {
	order := step12SiblingOrder(nodes, domain, input.ParentID)
	index := -1
	for i, id := range order {
		if id == inserted {
			index = i
			break
		}
	}
	if index < 0 {
		return false
	}
	order = append(order[:index], order[index+1:]...)
	position := len(order)
	if input.BeforeID != "" {
		for i, id := range order {
			if id == input.BeforeID {
				position = i
				break
			}
		}
	} else if input.AfterID != "" {
		for i, id := range order {
			if id == input.AfterID {
				position = i + 1
				break
			}
		}
	}
	if input.BeforeID != "" && position == len(order) || input.AfterID != "" && position == len(order) && (len(order) == 0 || order[len(order)-1] != input.AfterID) {
		return false
	}
	order = append(order, "")
	copy(order[position+1:], order[position:])
	order[position] = inserted
	return equalStrings(step12SiblingOrder(nodes, domain, input.ParentID), order)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, value := range a {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	for _, value := range b {
		if !seen[value] {
			return false
		}
	}
	return true
}
