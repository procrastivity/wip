package wipd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type submitV2TestAuthority struct {
	*commandStartFakeAuthority
	v2      bool
	entries []wipdjournal.Entry
}

func (authority *submitV2TestAuthority) SupportsCommandSubmitV2() bool { return authority.v2 }

func (authority *submitV2TestAuthority) Return(ctx context.Context, entry wipdjournal.Entry, anchor wipdwire.PrefixAnchor) (CommandFold, error) {
	authority.entries = append(authority.entries, entry)
	return authority.commandStartFakeAuthority.Return(ctx, entry, anchor)
}

func TestCommandSubmitV2BothHopIPCMatrix(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("local-v2=%t/authority-v2=%t", local, remote), func(t *testing.T) {
				_, journal, environment, fake, _, server := newCommandStartFixture(t)
				fake.returnResults = []operation.ResultCode{operation.ResultSucceeded, operation.ResultSucceeded}
				fake.returnContinue = []bool{false, false}
				authority := &submitV2TestAuthority{commandStartFakeAuthority: fake, v2: remote}
				if err := server.registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
					t.Error("connected command reached local semantic handler")
					return operation.Result{Code: operation.ResultFailed}
				}); err != nil {
					t.Fatal(err)
				}
				if err := server.ConfigureConnectedCommands(commandStartDomainID, journal, authority, environment); err != nil {
					t.Fatal(err)
				}
				httpClient, _ := startHTTP2UnixServer(t, server)
				client := &Client{httpClient: httpClient, m6: local}
				if err := client.negotiate(context.Background()); err != nil {
					t.Fatal(err)
				}
				if got := containsString(client.hello.features, wipdwire.CommandSubmitV2Feature); got != (local && remote) {
					t.Fatalf("local selected v2=%t, want both hops", got)
				}
				command := commandStartCanonicalCommand(commandStartCommandPrefix+"70", 1, "", commandStartCommandPrefix+"70",
					operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateInput{Title: "Compatible v1", Locator: "compatible-v1"}, nil)
				environment.setCurrentID(command.ID)
				if result, err := client.ExecuteCommand(context.Background(), command); err != nil || result.Code != operation.ResultSucceeded {
					t.Fatalf("non-repair v1: %+v %v", result, err)
				}
				if len(authority.entries) != 1 || authority.entries[0].SubmissionSchema != "wipd.command-submit/1" {
					t.Fatalf("v1 compatibility forwarded wrong envelope: %+v", authority.entries)
				}
				command.ID, command.CorrelationCommandID, command.EnvironmentSequence = commandStartCommandPrefix+"71", commandStartCommandPrefix+"71", 2
				environment.setCurrentID(command.ID)
				result, err := client.ExecuteCommandV2(context.Background(), command, nil)
				if local && remote {
					if err != nil || result.Code != operation.ResultSucceeded || len(authority.entries) != 2 || authority.entries[1].SubmissionSchema != wipdwire.CommandSubmitV2Feature {
						t.Fatalf("both-hop v2: %+v %v; entries=%+v", result, err, authority.entries)
					}
				} else {
					var exchange *ExchangeError
					if !errors.As(err, &exchange) || exchange.Code != "protocol.unsupported-extension" || exchange.Uncertain || len(authority.entries) != 1 {
						t.Fatalf("missing-hop denial: %+v %v", result, err)
					}
					if _, err = journal.Get(command.ID); !errors.Is(err, wipdjournal.ErrNotFound) {
						t.Fatalf("unsupported v2 reached durable preparation: %v", err)
					}
				}
				before, _ := journal.Entries()
				canonical, _ := command.CanonicalBytes()
				hash, _ := command.RequestHash()
				payload, _ := wipdwire.EncodeCanonical(wipdwire.CommandSubmitV2{
					Schema:           wipdwire.CommandSubmitV2Feature,
					CanonicalCommand: canonical, RequestHash: hash, DetachedProof: []byte{0xff, 0x00, 0x80},
				})
				wire, _ := encodeFrame(frameRecord{requestID: commandStartCommandPrefix + "72", kind: "command.submit", payload: payload}, maxFrameBodyLimit)
				response, err := postFrameRequest(httpClient, exchangePath, wire)
				if err != nil {
					t.Fatal(err)
				}
				frames := readResponseFrames(t, response)
				if len(frames) != 1 || frames[0].kind != "problem" {
					t.Fatalf("unnegotiated or unsupported proof reached dispatch: %+v", frames)
				}
				code, err := problemCodeFromPayload(frames[0].payload)
				if err != nil || code != "protocol.unsupported-extension" {
					t.Fatalf("proof denial: %q %v", code, err)
				}
				after, _ := journal.Entries()
				if len(after) != len(before) {
					t.Fatal("proof-bearing unsupported operation reached admission")
				}
			})
		}
	}
}

type submitV2RoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip submitV2RoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestCommandSubmitV2RepairRequiresBothHops(t *testing.T) {
	// The canonical input is public; authorization remains detached and private.
	id := commandStartCommandPrefix + "68"
	raw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": id,
		"authority":   map[string]any{"domain_id": commandStartDomainID, "expected_epoch": uint64(1)},
		"environment": map[string]any{"id": id, "sequence": uint64(1)},
		"acted_at":    "2026-10-02T00:00:00Z", "actor": "human", "causation_command_id": nil, "correlation_command_id": id,
		"operation": map[string]any{"name": "gate.exemption.repair", "version": uint64(1)},
		"context":   map[string]any{"repo_id": id, "clone_id": id, "worktree_id": id},
		"claim":     map[string]any{"id": id, "epoch": uint64(1)}, "blobs": []any{},
		"input": map[string]any{
			"node_id": id, "gate": "reviewed", "event_count": uint64(1), "high_water_event_id": id,
			"prefix_digest": "sha256:" + fmt.Sprintf("%064x", 0), "incident_ref": "urn:example:incident", "reason": "Owner-approved repair",
			"evidence_refs": []any{fmt.Sprintf("sha256:%064x", 0)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), raw...))
	for _, local := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			for _, schema := range []string{"wipd.command-submit/1", wipdwire.CommandSubmitV2Feature} {
				fields := map[string]any{"schema": schema, "canonical_command": raw, "request_hash": fmt.Sprintf("sha256:%x", hash), "deadline": nil}
				if schema == wipdwire.CommandSubmitV2Feature {
					fields["detached_proof"] = nil
				}
				payload, err := wipdwire.EncodeCanonical(fields)
				if err != nil {
					t.Fatal(err)
				}
				_, _, _, err = decodeNegotiatedCommandSubmit(payload, local, remote)
				allowed := local && remote && schema == wipdwire.CommandSubmitV2Feature
				if allowed && err != nil || !allowed && !errors.Is(err, errUnsupportedExtension) {
					t.Fatalf("repair ingress local=%t authority=%t schema=%s: %v", local, remote, schema, err)
				}
			}
		}
	}
}

func TestRepairIPCBothHopDenialBeforeJournalWrite(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("local=%t/authority=%t", local, remote), func(t *testing.T) {
				_, journal, _, fake, _, server := newCommandStartFixture(t)
				if err := server.registry.Register(operation.GateExemptionRepairV1, func(context.Context, operation.Request) operation.Result {
					t.Fatal("repair reached generic local handler")
					return operation.Result{}
				}); err != nil {
					t.Fatal(err)
				}
				if err := server.ConfigureConnectedCommands(commandStartDomainID, journal,
					&submitV2TestAuthority{commandStartFakeAuthority: fake, v2: remote}, newCommandStartTestEnvironment(t, journal, &commandStartTrace{}, commandStartCommandPrefix+"74")); err != nil {
					t.Fatal(err)
				}
				httpClient, _ := startHTTP2UnixServer(t, server)
				client := &Client{httpClient: httpClient, m6: local}
				if err := client.negotiate(context.Background()); err != nil {
					t.Fatal(err)
				}
				selected := operationCapabilityContains(client.hello.operations, operation.GateExemptionRepairV1.Metadata().Operation, identitySchemaV1)
				if selected != (local && remote) {
					t.Fatalf("repair capability selected=%t", selected)
				}
				id := commandStartCommandPrefix + "74"
				command := commandStartCanonicalCommand(id, 1, "", id, operation.GateExemptionRepairV1.Metadata().Operation,
					operation.GateExemptionRepairInput{
						NodeID: id, Gate: "reviewed", EventCount: 1, HighWaterEventID: id,
						PrefixDigest: "sha256:" + fmt.Sprintf("%064x", 0), IncidentRef: "urn:example:incident",
						Reason: "Owner-approved repair", EvidenceRefs: []string{fmt.Sprintf("sha256:%064x", 0)},
					}, &operation.ClaimContext{ID: id, Epoch: "1"})
				command.Request.Context.Clone, command.Request.Context.Worktree = id, id
				if _, err := client.ExecuteCommand(context.Background(), command); err == nil {
					t.Fatal("repair accepted over v1")
				}
				if !local || !remote {
					if _, err := client.ExecuteCommandV2(context.Background(), command, []byte{0x01}); err == nil {
						t.Fatal("repair accepted without both v2 hops")
					}
				}
				entries, err := journal.Entries()
				if err != nil || len(entries) != 0 {
					t.Fatalf("denied repair wrote journal: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestCommandSubmitV2LocalSendLossNeverFallsBack(t *testing.T) {
	command := commandStartCanonicalCommand(commandStartCommandPrefix+"69", 1, "", commandStartCommandPrefix+"69",
		operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateInput{Title: "Loss", Locator: "loss"}, nil)
	canonical, _ := command.CanonicalBytes()
	hash, _ := command.RequestHash()
	proof := []byte{0xff, 0x00, 0x80, 0x13}
	calls := 0
	client := &Client{hello: serverHello{
		features:   []string{wipdwire.CommandSubmitV2Feature, frameSchema},
		operations: []operationCapability{{name: "matter.create", versions: []uint16{1}, identitySchemas: []string{identitySchemaV1}}},
	}, parameters: defaultSessionParameters(4)}
	client.httpClient = &http.Client{Transport: submitV2RoundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		frame, err := readFrame(request.Body, maxFrameBodyLimit)
		if err != nil {
			t.Fatal(err)
		}
		submit, v2, err := wipdwire.DecodeCommandSubmit(frame.payload)
		if err != nil || !v2 || !bytes.Equal(submit.DetachedProof, proof) || !bytes.Equal(submit.CanonicalCommand, canonical) || submit.RequestHash != hash {
			t.Fatalf("local send changed detached bytes/identity: %+v %t %v", submit, v2, err)
		}
		return nil, errors.New("response lost after local send")
	})}
	_, err := client.ExecuteCommandV2(context.Background(), command, proof)
	var exchange *ExchangeError
	if !errors.As(err, &exchange) || !exchange.Uncertain || calls != 1 {
		t.Fatalf("local-send loss fallback: %v calls=%d", err, calls)
	}
	client.hello.features = []string{frameSchema}
	_, err = client.ExecuteCommandV2(context.Background(), command, nil)
	if !errors.As(err, &exchange) || exchange.Uncertain || exchange.Code != "protocol.unsupported-extension" || calls != 1 {
		t.Fatalf("omitted proof permitted downgrade after loss: %v calls=%d", err, calls)
	}
}
