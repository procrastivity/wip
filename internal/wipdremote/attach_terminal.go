package wipdremote

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type attachCommandExchanger interface {
	Exchange(context.Context, string, any) ([]wipdwire.Frame, error)
}

func decodeAttachTerminal(payload []byte, commandID, requestHash string) ([]byte, []byte, error) {
	var empty []byte
	var terminal wipdwire.SignedCommandTerminal
	if wipdwire.DecodeCanonical(payload, &terminal,
		"schema", "command_id", "request_hash", "receipt", "signed_receipt") != nil ||
		terminal.Schema != "wipd.signed-command-terminal/1" || terminal.CommandID != commandID ||
		terminal.RequestHash != requestHash || len(terminal.Receipt) == 0 || len(terminal.SignedReceipt) == 0 {
		return empty, empty, errors.New("wipdremote: invalid signed attach terminal")
	}
	return bytes.Clone(terminal.Receipt), bytes.Clone(terminal.SignedReceipt), nil
}

func terminalFromAttachSubmit(ctx context.Context, client attachCommandExchanger, domain string, entry wipdjournal.Entry, frames []wipdwire.Frame) ([]byte, []byte, error) {
	terminal := func(frame wipdwire.Frame) ([]byte, []byte, error) {
		if frame.Kind != "command.terminal" {
			return nil, nil, errors.New("wipdremote: attach response is not terminal")
		}
		receipt, signed, err := decodeAttachTerminal(frame.Payload, entry.Command.ID, entry.RequestHash)
		if err == nil {
			err = validateTerminalIdentity(receipt, domain, entry)
		}
		return receipt, signed, err
	}
	accepted := func(frame wipdwire.Frame) error {
		if frame.Kind != "submission.accepted" {
			return errors.New("wipdremote: attach submission was not acknowledged")
		}
		return validateAccepted(frame.Payload, domain, entry)
	}
	if len(frames) == 1 && frames[0].Kind == "command.terminal" {
		return terminal(frames[0])
	}
	if len(frames) == 2 && accepted(frames[0]) == nil {
		return terminal(frames[1])
	}
	if len(frames) != 1 || accepted(frames[0]) != nil {
		return nil, nil, errors.New("wipdremote: invalid attach submission response")
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
			return nil, nil, err
		}
		if len(frames) != 1 {
			return nil, nil, errors.New("wipdremote: attach receipt query has invalid frame count")
		}
		switch frames[0].Kind {
		case "command.terminal":
			return terminal(frames[0])
		case "receipt.pending":
			if err = validateAccepted(frames[0].Payload, domain, entry); err != nil {
				return nil, nil, err
			}
		case "receipt.not-found":
			var missing wipdwire.ReceiptNotFound
			if wipdwire.DecodeCanonical(frames[0].Payload, &missing, "schema", "domain_id", "command_id", "request_hash") != nil ||
				missing.Schema != "wipd.receipt-not-found/1" || missing.DomainID != domain ||
				missing.CommandID != entry.Command.ID || missing.RequestHash != entry.RequestHash {
				return nil, nil, errors.New("wipdremote: attach absence does not match pinned command")
			}
			submission, submitErr := entry.SubmissionPayload()
			if submitErr != nil {
				return nil, nil, submitErr
			}
			retryFrames, retryErr := client.Exchange(ctx, "command.submit", submission)
			if retryErr != nil {
				return nil, nil, retryErr
			}
			if len(retryFrames) == 1 && retryFrames[0].Kind == "command.terminal" {
				return terminal(retryFrames[0])
			}
			if len(retryFrames) == 2 && accepted(retryFrames[0]) == nil {
				return terminal(retryFrames[1])
			}
			if len(retryFrames) != 1 || accepted(retryFrames[0]) != nil {
				return nil, nil, errors.New("wipdremote: exact attach retry was not acknowledged")
			}
		default:
			return nil, nil, errors.New("wipdremote: unexpected attach receipt state")
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-deadline.C:
			return nil, nil, errors.New("wipdremote: attach remains pending; exact recovery identity is retained")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
