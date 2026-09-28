package wipdremote

import (
	"context"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func terminalFromSubmit(ctx context.Context, client *wipdseed.CommandExchangeClient, domain string, entry wipdjournal.Entry, frames []wipdwire.Frame) ([]byte, error) {
	if len(frames) == 0 {
		return nil, errors.New("wipdremote: empty command submission response")
	}
	if frames[0].Kind == "command.terminal" {
		if err := validateTerminalIdentity(frames[0].Payload, domain, entry); err != nil {
			return nil, err
		}
		return frames[0].Payload, nil
	}
	if frames[0].Kind != "submission.accepted" || validateAccepted(frames[0].Payload, domain, entry) != nil {
		return nil, errors.New("wipdremote: authority did not acknowledge the submitted command identity")
	}
	if len(frames) > 2 {
		return nil, errors.New("wipdremote: invalid command submission response sequence")
	}
	if len(frames) == 2 {
		if frames[1].Kind != "command.terminal" || validateTerminalIdentity(frames[1].Payload, domain, entry) != nil {
			return nil, errors.New("wipdremote: invalid terminal command response")
		}
		return frames[1].Payload, nil
	}
	query := wipdwire.ReceiptQuery{
		Schema: "wipd.receipt-query/1", DomainID: domain,
		CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		frames, err := client.Exchange(ctx, "receipt.query", query)
		if err != nil {
			return nil, err
		}
		if len(frames) != 1 {
			return nil, errors.New("wipdremote: receipt query returned an invalid frame count")
		}
		switch frames[0].Kind {
		case "command.terminal":
			if err = validateTerminalIdentity(frames[0].Payload, domain, entry); err != nil {
				return nil, err
			}
			return frames[0].Payload, nil
		case "receipt.pending":
			if err = validateAccepted(frames[0].Payload, domain, entry); err != nil {
				return nil, err
			}
		case "receipt.not-found":
			var missing wipdwire.ReceiptNotFound
			if wipdwire.DecodeCanonical(frames[0].Payload, &missing, "schema", "domain_id", "command_id", "request_hash") != nil ||
				missing.Schema != "wipd.receipt-not-found/1" || missing.DomainID != domain ||
				missing.CommandID != entry.Command.ID || missing.RequestHash != entry.RequestHash {
				return nil, errors.New("wipdremote: receipt absence is not bound to the pending command")
			}
			retry := wipdwire.CommandSubmit{
				Schema: "wipd.command-submit/1", CanonicalCommand: entry.CanonicalBytes, RequestHash: entry.RequestHash,
			}
			frames, err = client.Exchange(ctx, "command.submit", retry)
			if err != nil {
				return nil, err
			}
			if len(frames) == 0 || frames[0].Kind != "submission.accepted" && frames[0].Kind != "command.terminal" {
				return nil, errors.New("wipdremote: exact command retry was not accepted")
			}
			if frames[0].Kind == "command.terminal" {
				if err = validateTerminalIdentity(frames[0].Payload, domain, entry); err != nil {
					return nil, err
				}
				return frames[0].Payload, nil
			}
			if err = validateAccepted(frames[0].Payload, domain, entry); err != nil {
				return nil, err
			}
			if len(frames) > 2 || len(frames) == 2 && frames[1].Kind != "command.terminal" {
				return nil, errors.New("wipdremote: invalid exact-retry response sequence")
			}
			if len(frames) == 2 {
				if err = validateTerminalIdentity(frames[1].Payload, domain, entry); err != nil {
					return nil, err
				}
				return frames[1].Payload, nil
			}
		default:
			return nil, errors.New("wipdremote: authority returned an unexpected receipt state")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("wipdremote: command remains pending; exact retry is safe")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func validateAccepted(payload []byte, domain string, entry wipdjournal.Entry) error {
	var accepted wipdwire.SubmissionAccepted
	if wipdwire.DecodeCanonical(payload, &accepted, "schema", "domain_id", "authority_epoch", "command_id", "request_hash") != nil ||
		accepted.Schema != "wipd.submission-accepted/1" || accepted.DomainID != domain ||
		accepted.Epoch != entry.Command.ExpectedAuthorityEpoch || accepted.CommandID != entry.Command.ID || accepted.RequestHash != entry.RequestHash {
		return errors.New("wipdremote: submission acknowledgement identity mismatch")
	}
	return nil
}

func validateTerminalIdentity(payload []byte, domain string, entry wipdjournal.Entry) error {
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != domain ||
		fields["authority_epoch"] != entry.Command.ExpectedAuthorityEpoch || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != entry.Command.ID || fields["request_hash"] != entry.RequestHash {
		return errors.New("wipdremote: terminal receipt identity mismatch")
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") ||
		operationFields["name"] != entry.Command.Request.Operation.Name || operationFields["version"] != uint64(entry.Command.Request.Operation.Version) {
		return errors.New("wipdremote: terminal receipt operation mismatch")
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") ||
		environment["id"] != entry.Command.EnvironmentID || environment["sequence"] != entry.EnvironmentSeq {
		return errors.New("wipdremote: terminal receipt Environment mismatch")
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") {
		return errors.New("wipdremote: malformed terminal result")
	}
	if _, ok = result["code"].(string); !ok {
		return errors.New("wipdremote: malformed terminal result code")
	}
	return nil
}

func terminalResultCode(receipt []byte) (operation.ResultCode, error) {
	fields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return "", err
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") {
		return "", errors.New("wipdremote: malformed terminal result")
	}
	code, ok := result["code"].(string)
	if !ok || code != string(operation.ResultSucceeded) && code != string(operation.ResultRejected) &&
		code != string(operation.ResultRefused) && code != string(operation.ResultFailed) {
		return "", errors.New("wipdremote: unknown terminal result code")
	}
	return operation.ResultCode(code), nil
}
