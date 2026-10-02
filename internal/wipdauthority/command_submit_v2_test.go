package wipdauthority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestCommandSubmitV2AuthorityNegotiationAndAdmissionMatrix(t *testing.T) {
	for _, selectedV2 := range []bool{false, true} {
		for _, envelopeV2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("selected-v2=%t/envelope-v2=%t", selectedV2, envelopeV2), func(t *testing.T) {
				fixture := newM5CommandFixture(t)
				session := negotiateSubmitV2AuthorityTest(t, fixture, selectedV2)
				command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX70", 1, "version-matrix")
				canonical, _ := command.CanonicalBytes()
				hash := m5CommandHash(t, command)
				var payload any = wipdwire.CommandSubmit{Schema: "wipd.command-submit/1", CanonicalCommand: canonical, RequestHash: hash}
				if envelopeV2 {
					payload = wipdwire.CommandSubmitV2{Schema: wipdwire.CommandSubmitV2Feature, CanonicalCommand: canonical, RequestHash: hash}
				}
				frames := m5Exchange(t, fixture, session, "command.submit", payload)
				if envelopeV2 && !selectedV2 {
					assertSubmitV2Problem(t, frames, "protocol.unsupported-extension")
					if _, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1, fixture.peer, m5TestEnv, time.Now().UTC()); !errors.Is(err, authoritystore.ErrNotFound) {
						t.Fatalf("unsupported v2 reached authority admission: %v", err)
					}
					if fixture.calls.Load() != 0 {
						t.Fatal("unsupported v2 executed semantic handler")
					}
					return
				}
				if len(frames) != 2 || frames[0].Kind != "submission.accepted" || frames[1].Kind != "command.terminal" {
					t.Fatalf("supported transport response: %+v", frames)
				}
				terminal := bytes.Clone(frames[1].Payload)
				// Discard the response, reopen the authority, renegotiate, and
				// replay the same envelope. Admission is not executed twice.
				_ = fixture.store.Close()
				store, err := authoritystore.OpenExisting(fixture.root)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				fixture.store = store
				fixture.handler = fixture.serverForStore(t, store).http.Handler
				session = negotiateSubmitV2AuthorityTest(t, fixture, selectedV2)
				replay := m5Exchange(t, fixture, session, "command.submit", payload)
				if len(replay) != 1 || replay[0].Kind != "command.terminal" || !bytes.Equal(replay[0].Payload, terminal) || fixture.calls.Load() != 1 {
					t.Fatalf("lost authority response/restart changed identity/status: %+v calls=%d", replay, fixture.calls.Load())
				}
			})
		}
	}
}

func TestCommandSubmitV2AuthorityProofAndRepairStayPreAdmission(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := negotiateSubmitV2AuthorityTest(t, fixture, true)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX71", 1, "proof-rejected")
	canonical, _ := command.CanonicalBytes()
	hash := m5CommandHash(t, command)
	frames := m5Exchange(t, fixture, session, "command.submit", wipdwire.CommandSubmitV2{
		Schema: wipdwire.CommandSubmitV2Feature, CanonicalCommand: canonical, RequestHash: hash,
		DetachedProof: []byte{0xff, 0x00, 0x80, 0x61},
	})
	assertSubmitV2Problem(t, frames, "protocol.unsupported-extension")
	if _, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1, fixture.peer, m5TestEnv, time.Now().UTC()); !errors.Is(err, authoritystore.ErrNotFound) {
		t.Fatalf("unsupported detached authorization reached admission: %v", err)
	}
	for id := range session.operations {
		if id.Name == "gate.exemption.repair" {
			t.Fatal("transport feature registered private repair")
		}
	}
	if fixture.calls.Load() != 0 {
		t.Fatal("proof-bearing unsupported operation executed")
	}
}

func TestCommandSubmitV2LostAdmissionResponseReplaysAfterAuthorityRestart(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := negotiateSubmitV2AuthorityTest(t, fixture, true)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX72", 1, "lost-v2-admission")
	canonical, _ := command.CanonicalBytes()
	submit := wipdwire.CommandSubmitV2{Schema: wipdwire.CommandSubmitV2Feature, CanonicalCommand: canonical, RequestHash: m5CommandHash(t, command)}
	_, request := m5CommandRequest(t, session, fixture.peer, "command.submit", submit)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	lost := &m5LostTerminalWriter{cancel: cancel}
	fixture.handler.ServeHTTP(lost, request.WithContext(ctx))
	frames, err := wipdwire.ReadFrames(lost.body.Bytes(), 2)
	if err != nil || len(frames) != 1 || frames[0].Kind != "submission.accepted" || ctx.Err() != context.Canceled {
		t.Fatalf("loss after v2 durable admission: %+v %v", frames, err)
	}
	waitM5Terminal(t, fixture.store, fixture.peer, command)
	terminal := fixtureStoredReceipt(t, fixture.store, fixture.peer, command)
	if err = fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := authoritystore.OpenExisting(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fixture.store = reopened
	fixture.handler = fixture.handlerForStore(t, reopened)
	session = negotiateSubmitV2AuthorityTest(t, fixture, true)
	frames = m5Exchange(t, fixture, session, "command.submit", submit)
	if len(frames) != 1 || frames[0].Kind != "command.terminal" || !bytes.Equal(frames[0].Payload, terminal) || fixture.calls.Load() != 1 {
		t.Fatalf("v2 admission-loss restart replay changed terminal: %+v calls=%d", frames, fixture.calls.Load())
	}
}

func negotiateSubmitV2AuthorityTest(t *testing.T, fixture *m5CommandFixture, version2 bool) *labConnectionSession {
	t.Helper()
	features := []any{"wipd.frame/1"}
	if version2 {
		features = []any{wipdwire.CommandSubmitV2Feature, "wipd.frame/1"}
	}
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"}, "store_schemas": []any{"wipd.store/1"},
		"operations": []any{map[string]any{"name": "matter.create", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"}}},
		"features":   features,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &labConnectionSession{}
	recorder, request := m5Request(t, session, fixture.peer, "client.hello", hello, "POST", labNegotiatePath)
	fixture.handler.ServeHTTP(recorder, request)
	frames := m5ResponseFrames(t, recorder, 2)
	if len(frames) != 2 || frames[0].Kind != "server.hello" || !session.negotiated || session.commandSubmitV2 != version2 {
		t.Fatalf("authority v2 negotiation: %+v session=%+v", frames, session)
	}
	return session
}

func assertSubmitV2Problem(t *testing.T, frames []wipdwire.Frame, want string) {
	t.Helper()
	if len(frames) != 1 || frames[0].Kind != "problem" {
		t.Fatalf("expected no-effect problem %s: %+v", want, frames)
	}
	fields, err := wipdwire.DecodeCanonicalMap(frames[0].Payload, "code")
	if err != nil || fields["code"] != want {
		t.Fatalf("problem=%+v err=%v, want %s", fields, err, want)
	}
}
