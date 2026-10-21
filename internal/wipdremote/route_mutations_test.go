package wipdremote

import (
	"context"
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestRouteMutationAcceptedOnlyWaitsForExactTerminal(t *testing.T) {
	entry, terminal := routeMutationTestEntryAndTerminal(t)
	accepted, err := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
		Schema: "wipd.submission-accepted/1", DomainID: entry.Command.AuthorityDomainID,
		Epoch: entry.Command.ExpectedAuthorityEpoch, CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &routeMutationTestExchanger{replies: [][]wipdwire.Frame{
		{{Kind: "receipt.pending", Payload: accepted}},
		{{Kind: "command.terminal", Payload: terminal}},
	}}
	got, err := terminalFromRouteSubmit(context.Background(), client, entry.Command.AuthorityDomainID, entry,
		[]wipdwire.Frame{{Kind: "submission.accepted", Payload: accepted}})
	if err != nil || string(got) != string(terminal) {
		t.Fatalf("accepted-only route submission terminal = %x, %v", got, err)
	}
	if len(client.calls) != 2 || client.calls[0] != "receipt.query" || client.calls[1] != "receipt.query" {
		t.Fatalf("accepted-only route query calls = %v", client.calls)
	}
}

func TestRouteMutationAcceptedOnlyCancellationRetainsOutcomeUnknown(t *testing.T) {
	entry, _ := routeMutationTestEntryAndTerminal(t)
	accepted, err := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
		Schema: "wipd.submission-accepted/1", DomainID: entry.Command.AuthorityDomainID,
		Epoch: entry.Command.ExpectedAuthorityEpoch, CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &routeMutationTestExchanger{replies: [][]wipdwire.Frame{{{Kind: "receipt.pending", Payload: accepted}}}}
	if _, err = terminalFromRouteSubmit(ctx, client, entry.Command.AuthorityDomainID, entry,
		[]wipdwire.Frame{{Kind: "submission.accepted", Payload: accepted}}); !errors.Is(err, wipd.ErrOutcomeUnknown) {
		t.Fatalf("pending route mutation cancellation = %v; want outcome unknown", err)
	}
}

type routeMutationTestExchanger struct {
	replies [][]wipdwire.Frame
	calls   []string
}

func (client *routeMutationTestExchanger) Exchange(_ context.Context, operation string, _ any) ([]wipdwire.Frame, error) {
	client.calls = append(client.calls, operation)
	if len(client.replies) == 0 {
		return nil, errors.New("unexpected route exchange")
	}
	reply := client.replies[0]
	client.replies = client.replies[1:]
	return reply, nil
}

func routeMutationTestEntryAndTerminal(t *testing.T) (wipdjournal.Entry, []byte) {
	t.Helper()
	commandID := "01KZ7XHAQT1S46NYPN1PW1DX3E"
	hash := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	command := operation.Command{
		ID: commandID, AuthorityDomainID: "01KZ7XHAQT1S46NYPN1PW1DX3B", ExpectedAuthorityEpoch: 7,
		EnvironmentID: "01KZ7XHAQT1S46NYPN1PW1DX3D", EnvironmentSequence: 4,
		Request: operation.Request{
			Operation: operation.RepoRouteBindV1.Metadata().Operation,
			Actor:     "human", Context: operation.Context{Repo: "01KZ7XHAQT1S46NYPN1PW1DX3C", Clone: "01KZ7XHAQT1S46NYPN1PW1DX3F", Worktree: "01KZ7XHAQT1S46NYPN1PW1DX3G"},
			Input: operation.RepoRouteBindInput{NormalizedKey: "github.com/acme/widget"},
		},
	}
	entry := wipdjournal.Entry{Command: command, RequestHash: hash, EnvironmentSeq: command.EnvironmentSequence}
	output, err := wipdwire.EncodeCanonical(map[string]any{
		"repo_id": command.Request.Context.Repo, "domain_id": command.AuthorityDomainID, "epoch": command.ExpectedAuthorityEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": command.AuthorityDomainID,
		"authority_epoch": command.ExpectedAuthorityEpoch, "identity_schema": "wipd.command/1",
		"command_id": command.ID, "request_hash": hash,
		"operation":       map[string]any{"name": command.Request.Operation.Name, "version": uint64(command.Request.Operation.Version)},
		"environment":     map[string]any{"id": command.EnvironmentID, "sequence": command.EnvironmentSequence},
		"result":          map[string]any{"code": string(operation.ResultSucceeded), "output": output, "problem_code": nil},
		"accepted_events": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry, terminal
}
