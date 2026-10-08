package operation

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/fxamacker/cbor/v2"
)

var canonicalCommandDecoder = func() cbor.DecMode {
	mode, err := (cbor.DecOptions{
		DupMapKey:        cbor.DupMapKeyEnforcedAPF,
		IndefLength:      cbor.IndefLengthForbidden,
		TagsMd:           cbor.TagsForbidden,
		MaxNestedLevels:  16,
		MaxArrayElements: 1024,
		MaxMapPairs:      256,
		DefaultMapType:   reflect.TypeOf(map[string]any{}),
	}).DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}()

// DecodeCanonicalCommand decodes the closed wipd.command/1 identity schema.
// It accepts only values that re-encode byte-for-byte through CanonicalBytes,
// so alternate CBOR spellings never reach a semantic handler.
func DecodeCanonicalCommand(data []byte) (Command, error) {
	var value any
	if err := canonicalCommandDecoder.Unmarshal(data, &value); err != nil {
		return Command{}, fmt.Errorf("operation: decode canonical command: %w", err)
	}
	fields, err := commandMap(value, "command", "schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return Command{}, err
	}

	var command Command
	if command.ID, err = commandString(fields, "command_id"); err != nil {
		return Command{}, err
	}
	if schema, err := commandString(fields, "schema"); err != nil || schema != commandIdentitySchema {
		return Command{}, fmt.Errorf("operation: command schema is not %q", commandIdentitySchema)
	}
	if command.ActedAt, err = commandString(fields, "acted_at"); err != nil {
		return Command{}, err
	}
	if command.Request.Actor, err = commandActor(fields, "actor"); err != nil {
		return Command{}, err
	}
	if command.CausationCommandID, err = commandNullableString(fields, "causation_command_id"); err != nil {
		return Command{}, err
	}
	if command.CorrelationCommandID, err = commandString(fields, "correlation_command_id"); err != nil {
		return Command{}, err
	}

	authority, err := commandMap(fields["authority"], "authority", "domain_id", "expected_epoch")
	if err != nil {
		return Command{}, err
	}
	if command.AuthorityDomainID, err = commandString(authority, "domain_id"); err != nil {
		return Command{}, err
	}
	if command.ExpectedAuthorityEpoch, err = commandUint(authority, "expected_epoch"); err != nil {
		return Command{}, err
	}

	environment, err := commandMap(fields["environment"], "environment", "id", "sequence")
	if err != nil {
		return Command{}, err
	}
	if command.EnvironmentID, err = commandString(environment, "id"); err != nil {
		return Command{}, err
	}
	if command.EnvironmentSequence, err = commandUint(environment, "sequence"); err != nil {
		return Command{}, err
	}

	operationValue, err := commandMap(fields["operation"], "operation", "name", "version")
	if err != nil {
		return Command{}, err
	}
	if command.Request.Operation.Name, err = commandString(operationValue, "name"); err != nil {
		return Command{}, err
	}
	version, err := commandUint(operationValue, "version")
	if err != nil || version == 0 || version > math.MaxUint16 {
		return Command{}, fmt.Errorf("operation: operation version must be a positive uint16")
	}
	command.Request.Operation.Version = uint16(version)

	contextValue, err := commandMap(fields["context"], "context", "repo_id", "clone_id", "worktree_id")
	if err != nil {
		return Command{}, err
	}
	if command.Request.Context.Repo, err = commandNullableString(contextValue, "repo_id"); err != nil {
		return Command{}, err
	}
	if command.Request.Context.Clone, err = commandNullableString(contextValue, "clone_id"); err != nil {
		return Command{}, err
	}
	if command.Request.Context.Worktree, err = commandNullableString(contextValue, "worktree_id"); err != nil {
		return Command{}, err
	}

	if command.Request.Claim, err = commandDecodeClaim(fields["claim"]); err != nil {
		return Command{}, err
	}
	if command.Request.Input, err = commandDecodeInput(command.Request.Operation, fields["input"]); err != nil {
		return Command{}, err
	}
	if command.Request.Blobs, err = commandDecodeBlobs(fields["blobs"]); err != nil {
		return Command{}, err
	}

	canonical, err := command.CanonicalBytes()
	if err != nil {
		return Command{}, err
	}
	if !bytes.Equal(canonical, data) {
		return Command{}, fmt.Errorf("operation: command CBOR is not deterministic or canonical")
	}
	return command, nil
}

func commandMap(value any, name string, keys ...string) (map[string]any, error) {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("operation: %s must be a CBOR map", name)
	}
	if len(fields) != len(keys) {
		return nil, fmt.Errorf("operation: %s has missing or unknown fields", name)
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("operation: %s is missing field %q", name, key)
		}
	}
	return fields, nil
}

func commandString(fields map[string]any, key string) (string, error) {
	value, ok := fields[key].(string)
	if !ok {
		return "", fmt.Errorf("operation: %s must be CBOR text", key)
	}
	return value, nil
}

func commandActor(fields map[string]any, key string) (Actor, error) {
	value, err := commandString(fields, key)
	return Actor(value), err
}

func commandNullableString(fields map[string]any, key string) (string, error) {
	value, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("operation: %s is missing", key)
	}
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("operation: %s must be CBOR text or null", key)
	}
	return text, nil
}

func commandUint(fields map[string]any, key string) (uint64, error) {
	value, ok := fields[key].(uint64)
	if !ok {
		return 0, fmt.Errorf("operation: %s must be an unsigned CBOR integer", key)
	}
	return value, nil
}

func commandDecodeClaim(value any) (*ClaimContext, error) {
	if value == nil {
		return nil, nil
	}
	fields, err := commandMap(value, "claim", "id", "epoch")
	if err != nil {
		return nil, err
	}
	id, err := commandString(fields, "id")
	if err != nil {
		return nil, err
	}
	epoch, err := commandUint(fields, "epoch")
	if err != nil {
		return nil, err
	}
	return &ClaimContext{ID: id, Epoch: strconv.FormatUint(epoch, 10)}, nil
}

func commandDecodeInput(id ID, value any) (Input, error) {
	switch id {
	case MatterCreateV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "title", "requested_locator")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		locator, err := commandString(fields, "requested_locator")
		if err != nil {
			return nil, err
		}
		return MatterCreateInput{Title: title, Locator: locator}, nil
	case StepCreateV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "parent_id", "title")
		if err != nil {
			return nil, err
		}
		parent, err := commandString(fields, "parent_id")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		return StepCreateInput{ParentID: parent, Title: title}, nil
	case MatterCreateV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "title", "requested_locator")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		locator, err := commandString(fields, "requested_locator")
		if err != nil {
			return nil, err
		}
		return MatterCreateInput{Title: title, Locator: locator}, nil
	case StageCreateV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "title")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		return StageCreateInput{MatterID: matter, Title: title}, nil
	case StepCreateV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "parent_id", "title")
		if err != nil {
			return nil, err
		}
		parent, err := commandString(fields, "parent_id")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		return StepCreateInput{ParentID: parent, Title: title}, nil
	case StepInsertV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "parent_id", "title", "after_id", "before_id")
		if err != nil {
			return nil, err
		}
		parent, err := commandString(fields, "parent_id")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		after, err := commandNullableString(fields, "after_id")
		if err != nil {
			return nil, err
		}
		before, err := commandNullableString(fields, "before_id")
		if err != nil {
			return nil, err
		}
		return StepInsertInput{ParentID: parent, Title: title, AfterID: after, BeforeID: before}, nil
	case StepReorderV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "parent_id", "order")
		if err != nil {
			return nil, err
		}
		parent, err := commandString(fields, "parent_id")
		if err != nil {
			return nil, err
		}
		order, err := commandStringArray(fields, "order")
		if err != nil {
			return nil, err
		}
		return StepReorderInput{ParentID: parent, Order: order}, nil
	case StepReplaceV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "step_id", "title")
		if err != nil {
			return nil, err
		}
		step, err := commandString(fields, "step_id")
		if err != nil {
			return nil, err
		}
		title, err := commandString(fields, "title")
		if err != nil {
			return nil, err
		}
		return StepReplaceInput{StepID: step, Title: title}, nil
	case StepRemoveV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "step_id", "reason")
		if err != nil {
			return nil, err
		}
		step, err := commandString(fields, "step_id")
		if err != nil {
			return nil, err
		}
		reason, err := commandString(fields, "reason")
		if err != nil {
			return nil, err
		}
		return StepRemoveInput{StepID: step, Reason: reason}, nil
	case MatterLocatorRepairV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "action", "assigned_locator")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		locator, err := commandString(fields, "assigned_locator")
		if err != nil {
			return nil, err
		}
		action, err := commandString(fields, "action")
		if err != nil {
			return nil, err
		}
		return MatterLocatorRepairInput{MatterID: matter, AssignedLocator: locator, Action: action}, nil
	case StepStartV1.Metadata().Operation, StepFinishV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "step_id")
		if err != nil {
			return nil, err
		}
		stepID, err := commandString(fields, "step_id")
		if err != nil {
			return nil, err
		}
		return StepLifecycleInput{StepID: stepID}, nil
	case MatterFinishV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id")
		if err != nil {
			return nil, err
		}
		matterID, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		return MatterFinishInput{MatterID: matterID}, nil
	case BatchSweepAnonymousV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "batch_id", "claim_close")
		if err != nil {
			return nil, err
		}
		var input BatchSweepAnonymousInput
		if input.MatterID, err = commandString(fields, "matter_id"); err != nil {
			return nil, err
		}
		if input.BatchID, err = commandString(fields, "batch_id"); err != nil {
			return nil, err
		}
		closeFields, err := commandMap(fields["claim_close"], "claim_close",
			"claim_id", "claim_epoch", "release_command_id", "release_request_hash", "terminal_receipt_digest", "installed_prefix_anchor")
		if err != nil {
			return nil, err
		}
		if input.ClaimClose.ClaimID, err = commandString(closeFields, "claim_id"); err != nil {
			return nil, err
		}
		if input.ClaimClose.ClaimEpoch, err = commandUint(closeFields, "claim_epoch"); err != nil {
			return nil, err
		}
		if input.ClaimClose.ReleaseCommandID, err = commandString(closeFields, "release_command_id"); err != nil {
			return nil, err
		}
		if input.ClaimClose.ReleaseRequestHash, err = commandString(closeFields, "release_request_hash"); err != nil {
			return nil, err
		}
		if input.ClaimClose.TerminalReceiptDigest, err = commandString(closeFields, "terminal_receipt_digest"); err != nil {
			return nil, err
		}
		prefixFields, err := commandMap(closeFields["installed_prefix_anchor"], "installed_prefix_anchor", "event_count", "event_id", "digest")
		if err != nil {
			return nil, err
		}
		if input.ClaimClose.InstalledPrefixAnchor.EventCount, err = commandUint(prefixFields, "event_count"); err != nil {
			return nil, err
		}
		eventID, err := commandNullableString(prefixFields, "event_id")
		if err != nil {
			return nil, err
		}
		if prefixFields["event_id"] != nil {
			input.ClaimClose.InstalledPrefixAnchor.EventID = &eventID
		}
		if input.ClaimClose.InstalledPrefixAnchor.Digest, err = commandString(prefixFields, "digest"); err != nil {
			return nil, err
		}
		return input, nil
	case BatchCreateV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "name")
		if err != nil {
			return nil, err
		}
		name, err := commandString(fields, "name")
		if err != nil {
			return nil, err
		}
		return BatchCreateInput{Name: name}, nil
	case GateDeclareV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "gate", "scale")
		if err != nil {
			return nil, err
		}
		gate, err := commandString(fields, "gate")
		if err != nil {
			return nil, err
		}
		scale, err := commandString(fields, "scale")
		if err != nil {
			return nil, err
		}
		return GateDeclareInput{Gate: gate, Scale: scale}, nil
	case GateCloseV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "gate", "node_id")
		if err != nil {
			return nil, err
		}
		gate, err := commandString(fields, "gate")
		if err != nil {
			return nil, err
		}
		nodeID, err := commandString(fields, "node_id")
		if err != nil {
			return nil, err
		}
		return GateCloseInput{Gate: gate, NodeID: nodeID}, nil
	case GateDismissV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "gate", "node_id", "reason")
		if err != nil {
			return nil, err
		}
		gate, err := commandString(fields, "gate")
		if err != nil {
			return nil, err
		}
		nodeID, err := commandString(fields, "node_id")
		if err != nil {
			return nil, err
		}
		reason, err := commandString(fields, "reason")
		if err != nil {
			return nil, err
		}
		return GateDismissInput{Gate: gate, NodeID: nodeID, Reason: reason}, nil
	case DependencyAddV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "blocked_id", "blocker_id")
		if err != nil {
			return nil, err
		}
		blocked, err := commandString(fields, "blocked_id")
		if err != nil {
			return nil, err
		}
		blocker, err := commandString(fields, "blocker_id")
		if err != nil {
			return nil, err
		}
		return DependencyAddInput{BlockedID: blocked, BlockerID: blocker}, nil
	case DependencyAddV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "blocked_id", "blocker_id", "target_claims")
		if err != nil {
			return nil, err
		}
		blocked, err := commandString(fields, "blocked_id")
		if err != nil {
			return nil, err
		}
		blocker, err := commandString(fields, "blocker_id")
		if err != nil {
			return nil, err
		}
		claims, err := commandDecodeTargetClaims(fields["target_claims"])
		return DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: claims}, err
	case DependencyRemoveV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "blocked_id", "blocker_id")
		if err != nil {
			return nil, err
		}
		blocked, err := commandString(fields, "blocked_id")
		if err != nil {
			return nil, err
		}
		blocker, err := commandString(fields, "blocker_id")
		if err != nil {
			return nil, err
		}
		return DependencyRemoveInput{BlockedID: blocked, BlockerID: blocker}, nil
	case DependencyRemoveV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "blocked_id", "blocker_id", "target_claims")
		if err != nil {
			return nil, err
		}
		blocked, err := commandString(fields, "blocked_id")
		if err != nil {
			return nil, err
		}
		blocker, err := commandString(fields, "blocker_id")
		if err != nil {
			return nil, err
		}
		claims, err := commandDecodeTargetClaims(fields["target_claims"])
		return DependencyRemoveV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: claims}, err
	case ReferenceBindV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "reference")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		reference, err := commandString(fields, "reference")
		if err != nil {
			return nil, err
		}
		return ReferenceBindInput{MatterID: matter, Reference: reference}, nil
	case ReferenceBindV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "reference", "target_claims")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		reference, err := commandString(fields, "reference")
		if err != nil {
			return nil, err
		}
		claims, err := commandDecodeTargetClaims(fields["target_claims"])
		return ReferenceBindV2Input{MatterID: matter, Reference: reference, TargetClaims: claims}, err
	case ReferenceUnbindV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "reference")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		reference, err := commandString(fields, "reference")
		if err != nil {
			return nil, err
		}
		return ReferenceUnbindInput{MatterID: matter, Reference: reference}, nil
	case ReferenceUnbindV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "reference", "target_claims")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		reference, err := commandString(fields, "reference")
		if err != nil {
			return nil, err
		}
		claims, err := commandDecodeTargetClaims(fields["target_claims"])
		return ReferenceUnbindV2Input{MatterID: matter, Reference: reference, TargetClaims: claims}, err
	case ReferenceRebindV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "from", "to")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		from, err := commandString(fields, "from")
		if err != nil {
			return nil, err
		}
		to, err := commandString(fields, "to")
		if err != nil {
			return nil, err
		}
		return ReferenceRebindInput{MatterID: matter, From: from, To: to}, nil
	case ReferenceRebindV2.Metadata().Operation:
		fields, err := commandMap(value, "input", "matter_id", "from", "to", "target_claims")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		from, err := commandString(fields, "from")
		if err != nil {
			return nil, err
		}
		to, err := commandString(fields, "to")
		if err != nil {
			return nil, err
		}
		claims, err := commandDecodeTargetClaims(fields["target_claims"])
		return ReferenceRebindV2Input{MatterID: matter, From: from, To: to, TargetClaims: claims}, err
	case GateExemptionRepairV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "node_id", "gate", "event_count", "high_water_event_id", "prefix_digest", "incident_ref", "reason", "evidence_refs")
		if err != nil {
			return nil, err
		}
		var input GateExemptionRepairInput
		if input.NodeID, err = commandString(fields, "node_id"); err != nil {
			return nil, err
		}
		if input.Gate, err = commandString(fields, "gate"); err != nil {
			return nil, err
		}
		if input.EventCount, err = commandUint(fields, "event_count"); err != nil {
			return nil, err
		}
		if input.HighWaterEventID, err = commandString(fields, "high_water_event_id"); err != nil {
			return nil, err
		}
		if input.PrefixDigest, err = commandString(fields, "prefix_digest"); err != nil {
			return nil, err
		}
		if input.IncidentRef, err = commandString(fields, "incident_ref"); err != nil {
			return nil, err
		}
		if input.Reason, err = commandString(fields, "reason"); err != nil {
			return nil, err
		}
		if input.EvidenceRefs, err = commandStringArray(fields, "evidence_refs"); err != nil {
			return nil, err
		}
		return input, nil
	case MatterStartV1.Metadata().Operation, MatterPauseV1.Metadata().Operation,
		MatterResumeV1.Metadata().Operation, MatterCancelV1.Metadata().Operation,
		StageStartV1.Metadata().Operation, StagePauseV1.Metadata().Operation,
		StageResumeV1.Metadata().Operation, StageCancelV1.Metadata().Operation, StageFinishV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "node_id", "reason")
		if err != nil {
			return nil, err
		}
		nodeID, err := commandString(fields, "node_id")
		if err != nil {
			return nil, err
		}
		reason, err := commandString(fields, "reason")
		if err != nil {
			return nil, err
		}
		return NodeLifecycleInput{NodeID: nodeID, Reason: reason}, nil
	case StepPauseV1.Metadata().Operation, StepResumeV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "step_id")
		if err != nil {
			return nil, err
		}
		stepID, err := commandString(fields, "step_id")
		if err != nil {
			return nil, err
		}
		return StepLifecycleInput{StepID: stepID}, nil
	case StepCancelV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "step_id", "reason")
		if err != nil {
			return nil, err
		}
		stepID, err := commandString(fields, "step_id")
		if err != nil {
			return nil, err
		}
		reason, err := commandString(fields, "reason")
		if err != nil {
			return nil, err
		}
		return StepCancelInput{StepID: stepID, Reason: reason}, nil
	case ContentWriteOnceV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "subject_id", "kind")
		if err != nil {
			return nil, err
		}
		subject, err := commandString(fields, "subject_id")
		if err != nil {
			return nil, err
		}
		kind, err := commandString(fields, "kind")
		if err != nil {
			return nil, err
		}
		return ContentWriteInput{SubjectID: subject, Kind: kind}, nil
	case FindingAppendV1.Metadata().Operation:
		fields, err := commandMap(value, "input", "subject_id")
		if err != nil {
			return nil, err
		}
		subject, err := commandString(fields, "subject_id")
		if err != nil {
			return nil, err
		}
		return FindingAppendInput{SubjectID: subject}, nil
	default:
		return nil, fmt.Errorf("operation: no canonical identity schema for %s", id)
	}
}

func commandStringArray(fields map[string]any, key string) ([]string, error) {
	values, ok := fields[key].([]any)
	if !ok {
		return nil, fmt.Errorf("operation: %s must be an array", key)
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("operation: %s[%d] must be text", key, index)
		}
		result[index] = text
	}
	return result, nil
}

func commandDecodeTargetClaims(value any) ([]TargetClaim, error) {
	values, ok := value.([]any)
	if !ok || len(values) > 2 {
		return nil, fmt.Errorf("operation: target_claims must be an array of at most two records")
	}
	claims := make([]TargetClaim, 0, len(values))
	for index, value := range values {
		fields, err := commandMap(value, fmt.Sprintf("target_claims[%d]", index), "matter_id", "claim_id", "claim_epoch")
		if err != nil {
			return nil, err
		}
		matter, err := commandString(fields, "matter_id")
		if err != nil {
			return nil, err
		}
		id, err := commandString(fields, "claim_id")
		if err != nil {
			return nil, err
		}
		epoch, err := commandUint(fields, "claim_epoch")
		if err != nil {
			return nil, err
		}
		claims = append(claims, TargetClaim{MatterID: matter, ClaimID: id, ClaimEpoch: epoch})
	}
	return claims, nil
}

func commandDecodeBlobs(value any) ([]BlobInput, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("operation: blobs must be a CBOR array")
	}
	blobs := make([]BlobInput, 0, len(values))
	for index, value := range values {
		fields, err := commandMap(value, fmt.Sprintf("blobs[%d]", index), "name", "digest", "byte_length")
		if err != nil {
			return nil, err
		}
		name, err := commandString(fields, "name")
		if err != nil {
			return nil, err
		}
		digest, err := commandString(fields, "digest")
		if err != nil {
			return nil, err
		}
		length, err := commandUint(fields, "byte_length")
		if err != nil || length > math.MaxInt64 {
			return nil, fmt.Errorf("operation: blobs[%d].byte_length exceeds int64", index)
		}
		blobs = append(blobs, BlobInput{Name: name, Digest: digest, Size: int64(length)})
	}
	return blobs, nil
}
