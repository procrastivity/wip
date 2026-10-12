package authoritystore

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func referenceHistoryDefinition(id operation.ID) (operation.Definition, bool) {
	for _, definition := range []operation.Definition{
		operation.ReferenceBindV1, operation.ReferenceUnbindV1, operation.ReferenceRebindV1,
		operation.ReferenceBindV2, operation.ReferenceUnbindV2, operation.ReferenceRebindV2,
	} {
		if definition.Metadata().Operation == id {
			return definition, true
		}
	}
	return operation.Definition{}, false
}

func (s *Store) submitReferenceCommand(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool, definition operation.Definition,
) (CommandStatus, error) {
	var empty CommandStatus
	metadata := definition.Metadata()
	if metadata.Delivery != operation.DeliveryAuthority || metadata.Claim != operation.ClaimNone && metadata.Claim != operation.ClaimTargetSet ||
		definition.ValidateRequest(command.Request) != nil || command.Request.Claim != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone != "" || command.Request.Context.Worktree != "" {
		return empty, ErrInvalidProof
	}
	var beforeCommit func() error
	if checkContext {
		beforeCommit = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return nil
		}
	}
	return s.submitIdentity(ctx, commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch,
		environment: command.EnvironmentID, sequence: command.EnvironmentSequence,
		id: command.ID, name: metadata.Operation.Name, version: uint64(metadata.Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, nil, beforeCommit, nil)
}

func completeReferenceTx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	eventID string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{}
	refuse := func(code, message string) (step4Fold, error) {
		fold.result = step4Problem(code, message)
		return fold, nil
	}
	definition, ok := referenceHistoryDefinition(command.Request.Operation)
	if !ok {
		return fold, ErrInvalidProof
	}
	if definition.ValidateRequest(command.Request) != nil || command.Request.Claim != nil ||
		!ulid.MatchString(eventID) || occurred.IsZero() {
		return fold, ErrInvalidProof
	}
	matterID := ""
	from, to := "", ""
	switch input := command.Request.Input.(type) {
	case operation.ReferenceBindInput:
		matterID, to = input.MatterID, input.Reference
	case operation.ReferenceBindV2Input:
		matterID, to = input.MatterID, input.Reference
	case operation.ReferenceUnbindInput:
		matterID, from = input.MatterID, input.Reference
	case operation.ReferenceUnbindV2Input:
		matterID, from = input.MatterID, input.Reference
	case operation.ReferenceRebindInput:
		matterID, from, to = input.MatterID, input.From, input.To
	case operation.ReferenceRebindV2Input:
		matterID, from, to = input.MatterID, input.From, input.To
	default:
		return fold, ErrInvalidProof
	}
	projection, live, err := referenceStateTx(ctx, tx, identity.domain, identity.repo, matterID)
	if err != nil {
		return fold, err
	}
	if !live {
		return refuse("refusal.reference-matter", "the target Matter must be live in this authority domain and belong to the command Repo")
	}
	if command.Request.Operation.Version == 2 {
		valid, claimErr := validateTargetClaimsTx(ctx, tx, command, []string{matterID})
		if claimErr != nil {
			return fold, claimErr
		}
		if !valid {
			return refuse("refusal.claim-fenced", "the exact current claim for the target Matter is required")
		}
	}
	isActive := func(ref string) bool {
		for _, relation := range projection.references {
			if relation.domain == identity.domain && relation.matter == matterID && relation.ref == ref && relation.removed == "" {
				return true
			}
		}
		return false
	}
	eventKind := ""
	payload := map[string]any{}
	output := operation.ReferenceOutput{MatterID: matterID}
	switch command.Request.Operation {
	case operation.ReferenceBindV1.Metadata().Operation, operation.ReferenceBindV2.Metadata().Operation:
		if isActive(to) {
			return refuse("refusal.reference-exists", "that tracker reference is already bound to the Matter")
		}
		eventKind = "reference.added"
		payload["ref"] = to
		output.Reference = to
	case operation.ReferenceUnbindV1.Metadata().Operation, operation.ReferenceUnbindV2.Metadata().Operation:
		if !isActive(from) {
			return refuse("refusal.reference-missing", "that tracker reference is not bound to the Matter")
		}
		eventKind = "reference.removed"
		payload["ref"] = from
		output.Reference = from
	case operation.ReferenceRebindV1.Metadata().Operation, operation.ReferenceRebindV2.Metadata().Operation:
		if !isActive(from) {
			return refuse("refusal.reference-missing", "the source tracker reference is not bound to the Matter")
		}
		if isActive(to) {
			return refuse("refusal.reference-destination-exists", "the destination tracker reference is already bound to the Matter")
		}
		eventKind = "reference.rebound"
		payload["from"], payload["to"] = from, to
		output.Reference, output.PreviousReference = to, from
	default:
		return fold, ErrInvalidProof
	}
	level, err := gatePushLevelTx(ctx, tx, identity.domain, identity.repo)
	if err != nil {
		return fold, err
	}
	payload["tracker_push_level"] = level
	position, err := appendCommandEvent(ctx, tx, identity, occurred, eventID, eventKind, matterID, payload)
	if err != nil {
		return fold, err
	}
	if err = step13ProjectionTx(ctx, tx); err != nil {
		return fold, err
	}
	fold.result = operation.Result{Code: operation.ResultSucceeded, Output: output}
	if definition.ValidateResult(fold.result) != nil {
		return fold, ErrInvalidProof
	}
	fold.output, err = referenceOutputBytes(output)
	if err != nil {
		return fold, err
	}
	fold.first, fold.last = position, position
	fold.rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}
	return fold, nil
}

func referenceStateTx(ctx context.Context, tx *sql.Tx, domain, repo, matter string) (step13Projection, bool, error) {
	var empty step13Projection
	nodes, err := step13NodesTx(ctx, tx)
	if err != nil {
		return empty, false, err
	}
	events, err := step13EventsTx(ctx, tx)
	if err != nil {
		return empty, false, err
	}
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		return empty, false, err
	}
	stored, err := readStep13ProjectionTx(ctx, tx)
	if err != nil {
		return empty, false, err
	}
	if !sameStep13Projection(stored, projection) {
		return empty, false, ErrInvalidStore
	}
	memberships, err := dependencyRepoMemberships(ctx, tx)
	if err != nil {
		return empty, false, err
	}
	if memberships[repo] != domain {
		return empty, false, ErrFenced
	}
	var position uint64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(position),0) FROM authority_events WHERE domain_id=?`, domain).Scan(&position); err != nil {
		return empty, false, err
	}
	node, exists := nodes[ownerKey(domain, matter)]
	live := exists && node.node.domain == domain && node.node.kind == "matter" && node.node.repo == repo &&
		memberships[node.node.repo] == domain && node.birthPos <= position && step13NodeLiveAt(node, position)
	return projection, live, nil
}

func referenceOutputBytes(output operation.ReferenceOutput) ([]byte, error) {
	fields := map[string]any{"matter_id": output.MatterID, "reference": output.Reference}
	if output.PreviousReference != "" {
		fields["previous_reference"] = output.PreviousReference
	}
	return artifactEncoder.Marshal(fields)
}

func validateReferenceHistoryEvent(event step12Event, submission storedSubmission, command operation.Command) error {
	definition, exists := referenceHistoryDefinition(command.Request.Operation)
	if !exists || definition.ValidateRequest(command.Request) != nil || command.Request.Claim != nil ||
		event.hash != submission.hash || event.domain != command.AuthorityDomainID || event.command != command.ID ||
		event.repo != command.Request.Context.Repo || event.environment != command.EnvironmentID ||
		event.sequence != command.EnvironmentSequence || event.acted != command.ActedAt {
		return ErrInvalidStore
	}
	var matterID, ref, from, to, kind string
	switch input := command.Request.Input.(type) {
	case operation.ReferenceBindInput:
		matterID, ref, kind = input.MatterID, input.Reference, "reference.added"
	case operation.ReferenceBindV2Input:
		matterID, ref, kind = input.MatterID, input.Reference, "reference.added"
	case operation.ReferenceUnbindInput:
		matterID, ref, kind = input.MatterID, input.Reference, "reference.removed"
	case operation.ReferenceUnbindV2Input:
		matterID, ref, kind = input.MatterID, input.Reference, "reference.removed"
	case operation.ReferenceRebindInput:
		matterID, from, to, kind = input.MatterID, input.From, input.To, "reference.rebound"
	case operation.ReferenceRebindV2Input:
		matterID, from, to, kind = input.MatterID, input.From, input.To, "reference.rebound"
	default:
		return ErrInvalidStore
	}
	if event.kind != kind {
		return ErrInvalidStore
	}
	if command.Request.Operation == operation.ReferenceRebindV1.Metadata().Operation || command.Request.Operation == operation.ReferenceRebindV2.Metadata().Operation {
		var payload struct {
			From             string `cbor:"from"`
			To               string `cbor:"to"`
			TrackerPushLevel string `cbor:"tracker_push_level"`
		}
		if event.subject != matterID ||
			!step13ClosedPayload(event.payload, &payload, []string{"from", "to", "tracker_push_level"}) ||
			payload.From != from || payload.To != to || !referencePushLevel(payload.TrackerPushLevel) {
			return ErrInvalidStore
		}
		return nil
	}
	var payload struct {
		Ref              string `cbor:"ref"`
		TrackerPushLevel string `cbor:"tracker_push_level"`
	}
	if event.subject != matterID || !step13ClosedPayload(event.payload, &payload, []string{"ref", "tracker_push_level"}) ||
		payload.Ref != ref || !referencePushLevel(payload.TrackerPushLevel) {
		return ErrInvalidStore
	}
	return nil
}

func referencePushLevel(level string) bool {
	return level == "off" || level == "boundary" || level == "narrated"
}

func checkReferenceCommands(db *sql.DB, submissions []storedSubmission) error {
	for _, submission := range submissions {
		definition, known := referenceHistoryDefinition(operation.ID{Name: submission.operation, Version: uint16(submission.version)})
		if !known || submission.state != "terminal" {
			continue
		}
		command, err := operation.DecodeCanonicalCommand(submission.command)
		if err != nil || definition.ValidateRequest(command.Request) != nil || command.Request.Claim != nil ||
			command.AuthorityDomainID != submission.domain || command.ID != submission.id ||
			command.ExpectedAuthorityEpoch != submission.epoch || command.EnvironmentID != submission.env || command.EnvironmentSequence != submission.seq {
			return ErrInvalidStore
		}
		var receipt []byte
		if err = db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&receipt); err != nil {
			return ErrInvalidStore
		}
		r, err := readReceipt(receipt)
		if err != nil {
			return ErrInvalidStore
		}
		if r.Result.Code == string(operation.ResultSucceeded) {
			var output operation.ReferenceOutput
			switch input := command.Request.Input.(type) {
			case operation.ReferenceBindInput:
				output = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.Reference}
			case operation.ReferenceBindV2Input:
				output = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.Reference}
			case operation.ReferenceUnbindInput:
				output = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.Reference}
			case operation.ReferenceUnbindV2Input:
				output = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.Reference}
			case operation.ReferenceRebindInput:
				output = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.To, PreviousReference: input.From}
			case operation.ReferenceRebindV2Input:
				output = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.To, PreviousReference: input.From}
			default:
				return ErrInvalidStore
			}
			want, marshalErr := referenceOutputBytes(output)
			if marshalErr != nil || !bytes.Equal(r.Result.Output, want) || r.Range == nil || r.Range.Count != 1 {
				return ErrInvalidStore
			}
		} else if r.Result.Code != string(operation.ResultRefused) || r.Result.Problem == nil || r.Result.Output != nil || r.Range != nil ||
			!referenceRefusalAllowed(command.Request.Operation, *r.Result.Problem) {
			return ErrInvalidStore
		}
	}
	return nil
}

func referenceRefusalAllowed(id operation.ID, problem string) bool {
	if problem == "refusal.step8-v1-claim-required" {
		return id.Version == 1
	}
	if problem == "refusal.claim-fenced" {
		return id.Version == 2
	}
	if problem == "refusal.reference-matter" {
		return true
	}
	switch id {
	case operation.ReferenceBindV1.Metadata().Operation, operation.ReferenceBindV2.Metadata().Operation:
		return problem == "refusal.reference-exists"
	case operation.ReferenceUnbindV1.Metadata().Operation, operation.ReferenceUnbindV2.Metadata().Operation:
		return problem == "refusal.reference-missing"
	case operation.ReferenceRebindV1.Metadata().Operation, operation.ReferenceRebindV2.Metadata().Operation:
		return problem == "refusal.reference-missing" || problem == "refusal.reference-destination-exists"
	default:
		return false
	}
}
