package wipdremote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type submitV2Exchange func(context.Context, string, any) ([]wipdwire.Frame, error)

func (exchange submitV2Exchange) Exchange(ctx context.Context, kind string, payload any) ([]wipdwire.Frame, error) {
	return exchange(ctx, kind, payload)
}

func TestCommandSubmitRetryUsesRestartedDurableEnvelope(t *testing.T) {
	for _, schema := range []string{"wipd.command-submit/1", wipdwire.CommandSubmitV2Feature} {
		t.Run(schema, func(t *testing.T) {
			identity := wipdjournal.Identity{
				RepoID: "01KZ7XHAQT1S46NYPN1PW1DX3B", DomainID: "01KZ7XHAQT1S46NYPN1PW1DX3A",
				AuthorityEpoch: 1, EnvironmentID: "01KZ7XHAQT1S46NYPN1PW1DX3C",
			}
			root := filepath.Join(privateTempDir(t), "journal")
			journal, err := wipdjournal.Open(root, identity)
			if err != nil {
				t.Fatal(err)
			}
			command := operation.Command{
				ID: "01KZ7XHAQT1S46NYPN1PW1DX70", AuthorityDomainID: identity.DomainID,
				ExpectedAuthorityEpoch: 1, EnvironmentID: identity.EnvironmentID, EnvironmentSequence: 1,
				ActedAt: "2026-10-02T00:00:00Z", CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX70",
				Request: operation.Request{
					Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
					Context: operation.Context{Repo: identity.RepoID}, Input: operation.MatterCreateInput{Title: "Retry", Locator: "retry"},
				},
			}
			var proof []byte
			if schema == wipdwire.CommandSubmitV2Feature {
				proof = []byte{0x00, 0xff, 0x83, 0x11}
			}
			if _, err = journal.PrepareCanonicalSubmission(command, schema, proof); err != nil {
				t.Fatal(err)
			}
			_ = journal.Close()
			journal, err = wipdjournal.Open(root, identity)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			entry, err := journal.PrepareCanonicalSubmission(command, schema, nil)
			if err != nil {
				t.Fatal(err)
			}
			accepted, _ := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
				Schema:   "wipd.submission-accepted/1",
				DomainID: identity.DomainID, Epoch: 1, CommandID: command.ID, RequestHash: entry.RequestHash,
			})
			missing, _ := wipdwire.EncodeCanonical(wipdwire.ReceiptNotFound{
				Schema:   "wipd.receipt-not-found/1",
				DomainID: identity.DomainID, CommandID: command.ID, RequestHash: entry.RequestHash,
			})
			terminal, _ := wipdwire.EncodeCanonical(map[string]any{
				"schema": "wipd.terminal-receipt/1", "domain_id": identity.DomainID, "authority_epoch": uint64(1),
				"identity_schema": "wipd.command/1", "command_id": command.ID, "request_hash": entry.RequestHash,
				"operation":   map[string]any{"name": "matter.create", "version": uint64(1)},
				"environment": map[string]any{"id": identity.EnvironmentID, "sequence": uint64(1)},
				"result":      map[string]any{"code": "result.refused", "output": nil, "problem_code": "refusal.fixture"}, "accepted_events": nil,
			})
			for _, mode := range []string{"terminal-already-stored", "not-found-retry", "ambiguous-retry-error", "query-error"} {
				t.Run(mode, func(t *testing.T) {
					calls := 0
					exchange := submitV2Exchange(func(_ context.Context, kind string, payload any) ([]wipdwire.Frame, error) {
						calls++
						if calls == 1 {
							query, ok := payload.(wipdwire.ReceiptQuery)
							if kind != "receipt.query" || !ok || query.CommandID != command.ID || query.RequestHash != entry.RequestHash {
								t.Error("query lost retained retry identity")
								return nil, errors.New("query lost retained retry identity")
							}
							if mode == "query-error" {
								return nil, errors.New("ambiguous query error")
							}
							if mode == "terminal-already-stored" {
								return []wipdwire.Frame{{Kind: "command.terminal", Payload: terminal}}, nil
							}
							return []wipdwire.Frame{{Kind: "receipt.not-found", Payload: missing}}, nil
						}
						if calls != 2 || kind != "command.submit" {
							t.Errorf("unexpected retry/fallback %d %s", calls, kind)
							return nil, fmt.Errorf("unexpected retry/fallback %d %s", calls, kind)
						}
						encoded, _ := wipdwire.EncodeCanonical(payload)
						decoded, v2, err := wipdwire.DecodeCommandSubmit(encoded)
						if err != nil || v2 != (schema == wipdwire.CommandSubmitV2Feature) || decoded.Schema != schema ||
							!bytes.Equal(decoded.DetachedProof, proof) || !bytes.Equal(decoded.CanonicalCommand, entry.CanonicalBytes) || decoded.RequestHash != entry.RequestHash {
							t.Error("retry downgraded or replaced durable proof/identity")
							return nil, errors.New("retry downgraded or replaced durable proof/identity")
						}
						if mode == "ambiguous-retry-error" {
							return nil, errors.New("lost retry response")
						}
						return []wipdwire.Frame{{Kind: "command.terminal", Payload: terminal}}, nil
					})
					got, err := terminalFromSubmit(context.Background(), exchange, identity.DomainID, entry,
						[]wipdwire.Frame{{Kind: "submission.accepted", Payload: accepted}})
					failed := mode == "ambiguous-retry-error" || mode == "query-error"
					wantCalls := 2
					if mode == "terminal-already-stored" || mode == "query-error" {
						wantCalls = 1
					}
					if (err != nil) != failed || !failed && !bytes.Equal(got, terminal) || calls != wantCalls {
						t.Fatalf("durable retry status: receipt=%x err=%v calls=%d, want calls=%d", got, err, calls, wantCalls)
					}
				})
			}
		})
	}
}

func TestRepairTerminalRejectsSubstitutedOutputAndRange(t *testing.T) {
	entry := wipdjournal.Entry{Command: operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX70", AuthorityDomainID: "01KZ7XHAQT1S46NYPN1PW1DX3A",
		ExpectedAuthorityEpoch: 1, EnvironmentID: "01KZ7XHAQT1S46NYPN1PW1DX3C", EnvironmentSequence: 1,
		Request: operation.Request{
			Operation: operation.GateExemptionRepairV1.Metadata().Operation,
			Input:     operation.GateExemptionRepairInput{NodeID: "01KZ7XHAQT1S46NYPN1PW1DX71", Gate: "reviewed"},
		},
	}, RequestHash: "sha256:" + fmt.Sprintf("%064x", 1), EnvironmentSeq: 1}
	for _, tc := range []struct {
		gate    string
		already bool
		rangeOK bool
		wantErr bool
	}{
		{"reviewed", true, false, false},
		{"other", true, false, true},
		{"reviewed", false, false, true},
		{"reviewed", true, true, true},
	} {
		output, _ := wipdwire.EncodeCanonical(map[string]any{
			"gate": tc.gate, "node_id": "01KZ7XHAQT1S46NYPN1PW1DX71", "already_exempt": tc.already,
		})
		var accepted any
		if tc.rangeOK {
			accepted = map[string]any{"first_event_id": "01KZ7XHAQT1S46NYPN1PW1DX72", "last_event_id": "01KZ7XHAQT1S46NYPN1PW1DX72", "event_count": uint64(1)}
		}
		receipt, _ := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID, "authority_epoch": uint64(1),
			"identity_schema": "wipd.command/1", "command_id": entry.Command.ID, "request_hash": entry.RequestHash,
			"operation":       map[string]any{"name": "gate.exemption.repair", "version": uint64(1)},
			"environment":     map[string]any{"id": entry.Command.EnvironmentID, "sequence": uint64(1)},
			"result":          map[string]any{"code": string(operation.ResultSucceeded), "output": output, "problem_code": nil},
			"accepted_events": accepted,
		})
		if err := validateTerminalIdentity(receipt, entry.Command.AuthorityDomainID, entry); (err != nil) != tc.wantErr {
			t.Fatalf("gate=%q already=%t range=%t: err=%v", tc.gate, tc.already, tc.rangeOK, err)
		}
	}
}
