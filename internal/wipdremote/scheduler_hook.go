package wipdremote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/schedulerhook"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (runtime *Runtime) completeSchedulerHook(ctx context.Context, entry wipdjournal.Entry, frames []wipdwire.Frame) ([]byte, error) {
	if len(frames) != 1 {
		return nil, errors.New("wipdremote: invalid scheduler-hook submission response")
	}
	var terminal []byte
	switch frames[0].Kind {
	case "command.terminal":
		if err := validateTerminalIdentity(frames[0].Payload, runtime.state.DomainID, entry); err != nil {
			return nil, err
		}
		terminal = frames[0].Payload
	case "submission.accepted":
		if validateAccepted(frames[0].Payload, runtime.state.DomainID, entry) != nil {
			return nil, errors.New("wipdremote: scheduler-hook submission identity mismatch")
		}
	default:
		return nil, errors.New("wipdremote: scheduler-hook submission identity mismatch")
	}
	if runtime.hooks == nil || !runtime.client.SupportsOperation(operation.SchedulerOrchestrateV1.Metadata().Operation) {
		return nil, errors.New("protocol.unsupported-extension")
	}
	request, err := schedulerhook.RequestFromCommand(entry.Command)
	if err != nil {
		return nil, err
	}
	expectedInputDigest, err := schedulerhook.InputDigest(request)
	if err != nil {
		return nil, err
	}
	result, err := runtime.hooks.LookupInstalled(ctx, entry.Command.ID)
	if errors.Is(err, schedulerhook.ErrNotFound) {
		for index := range request.Inputs {
			reader, size, openErr := runtime.journal.OpenBlob(request.Inputs[index].Digest)
			if openErr != nil || size != request.Inputs[index].Size {
				if reader != nil {
					_ = reader.Close()
				}
				return nil, wipdjournal.ErrInvalidJournal
			}
			content, readErr := io.ReadAll(io.LimitReader(reader, size+1))
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || int64(len(content)) != size {
				clear(content)
				return nil, errors.Join(readErr, closeErr, wipdjournal.ErrInvalidJournal)
			}
			request.Inputs[index].Bytes = content
		}
		hookCtx, stopKeepAlive := context.WithCancel(ctx)
		keepAliveError := make(chan error, 1)
		keepAliveDone := make(chan struct{})
		go func() {
			defer close(keepAliveDone)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-hookCtx.Done():
					return
				case <-ticker.C:
					pending, queryErr := runtime.client.Exchange(hookCtx, "receipt.query", wipdwire.ReceiptQuery{
						Schema: "wipd.receipt-query/1", DomainID: runtime.state.DomainID,
						CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
					})
					if queryErr == nil && (len(pending) != 1 || pending[0].Kind != "receipt.pending") {
						queryErr = errors.New("wipdremote: scheduler-hook keepalive did not confirm the original command as pending")
					}
					if queryErr == nil {
						queryErr = validateAccepted(pending[0].Payload, runtime.state.DomainID, entry)
					}
					if queryErr != nil {
						keepAliveError <- queryErr
						stopKeepAlive()
						return
					}
				}
			}
		}()
		result, err = runtime.hooks.StartOrInstall(hookCtx, request)
		stopKeepAlive()
		<-keepAliveDone
		select {
		case keepAliveErr := <-keepAliveError:
			if keepAliveErr != nil {
				return nil, fmt.Errorf("wipdremote: scheduler-hook keepalive: %w", keepAliveErr)
			}
		default:
		}
		for index := range request.Inputs {
			clear(request.Inputs[index].Bytes)
		}
	}
	if err != nil {
		return nil, err
	}
	if result.InputDigest != expectedInputDigest {
		return nil, schedulerhook.ErrConflict
	}
	output, err := schedulerhook.OperationOutput(result)
	if err != nil {
		return nil, err
	}
	if output.Kind != string(request.Kind) || output.Version != uint64(request.Version) {
		return nil, schedulerhook.ErrConflict
	}
	if output.Kind == string(schedulerhook.KindWork) {
		for _, digest := range output.EvidenceRefs {
			alreadyUploaded := false
			for _, input := range entry.Command.Request.Blobs {
				if input.Digest == digest {
					alreadyUploaded = true
					break
				}
			}
			if alreadyUploaded {
				continue
			}
			evidence, evidenceErr := runtime.hooks.Evidence(ctx, digest)
			if evidenceErr != nil {
				return nil, evidenceErr
			}
			evidenceErr = runtime.client.UploadBlob(ctx, digest, uint64(len(evidence)), bytes.NewReader(evidence))
			clear(evidence)
			if evidenceErr != nil {
				return nil, evidenceErr
			}
		}
	}
	encodedOutput, err := wipdwire.EncodeCanonical(output)
	if err != nil {
		return nil, err
	}
	if terminal != nil {
		if err = validateSchedulerHookTerminal(terminal, runtime.state.DomainID, entry, encodedOutput); err != nil {
			return nil, err
		}
		return terminal, nil
	}
	payload := wipdwire.SchedulerHookResult{
		Schema: "wipd.scheduler-hook-result/1", DomainID: runtime.state.DomainID, Epoch: runtime.state.Epoch,
		EnvironmentID: runtime.state.EnvironmentID, CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
		CanonicalCommand: bytes.Clone(entry.CanonicalBytes), Result: encodedOutput,
	}
	frames, err = runtime.client.Exchange(ctx, "scheduler.hook-result", payload)
	if err != nil {
		return nil, err
	}
	if len(frames) != 1 || frames[0].Kind != "command.terminal" {
		return nil, errors.New("wipdremote: scheduler-hook result receiver returned no terminal receipt")
	}
	if err = validateSchedulerHookTerminal(frames[0].Payload, runtime.state.DomainID, entry, encodedOutput); err != nil {
		return nil, err
	}
	return frames[0].Payload, nil
}

func validateSchedulerHookTerminal(receipt []byte, domain string, entry wipdjournal.Entry, output []byte) error {
	if err := validateTerminalIdentity(receipt, domain, entry); err != nil {
		return err
	}
	fields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return err
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") ||
		result["code"] != string(operation.ResultSucceeded) || result["problem_code"] != nil {
		return errors.New("wipdremote: scheduler-hook terminal result is not successful")
	}
	actual, ok := result["output"].([]byte)
	if !ok || !bytes.Equal(actual, output) {
		return errors.New("wipdremote: authority receipt substituted the scheduler-hook result")
	}
	accepted, ok := fields["accepted_events"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") ||
		accepted["event_count"] != uint64(1) || accepted["first_event_id"] != accepted["last_event_id"] {
		return fmt.Errorf("wipdremote: scheduler-hook completion did not produce exactly one event")
	}
	return nil
}
