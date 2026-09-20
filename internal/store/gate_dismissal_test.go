package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func commitGateDismissal(h *harness, actor Actor, env Env, subject string, payload any) ([]Event, error) {
	return h.Commit(h.ctx, Request{Actor: actor, Env: env}, func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeGateDismissed, Subject: subject, Payload: payload}}, nil
	})
}

func dismissalEnv(h *harness, repo string) Env {
	return Env{Repo: repo, Clone: h.Clone, Worktree: h.Worktree}
}

func dismissalPayload(gate string, scale Scale, reason string) GateDismissed {
	return GateDismissed{Gate: gate, Scale: scale, Reason: reason}
}

func TestGateDismissalRoundTripsAsDistinctTerminalSatisfaction(t *testing.T) {
	h := newHarness(t)
	// The declaration is an event since v13, so it is still there after the
	// rebuild at the end of this test — which the dismissal rule needs, because
	// a dismissal names a gate its Repo declares.
	h.declareGate("reviewed-local", ScaleMatter)
	matter := h.matter("dismissed", "A Matter with an emergency dismissal")
	h.start(matter)
	h.finish(matter)

	reason := "the designated verifier is unavailable during the incident"
	events, err := commitGateDismissal(h, ActorHuman, dismissalEnv(h, h.Repo), matter,
		dismissalPayload("reviewed-local", ScaleMatter, reason))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != TypeGateDismissed || events[0].Actor != ActorHuman ||
		events[0].Repo != h.Repo || events[0].Clone != "" || events[0].Worktree != "" {
		t.Fatalf("dismissal envelope = %+v, want durable gate event attributed to human", events)
	}

	var payload GateDismissed
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Gate != "reviewed-local" || payload.Scale != ScaleMatter || payload.Reason != reason {
		t.Fatalf("dismissal payload = %+v, want gate, scale and immutable reason", payload)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["gate"] == nil || fields["scale"] == nil || fields["reason"] == nil {
		t.Fatalf("dismissal payload fields = %v, want exactly gate, scale, reason", fields)
	}

	node, err := h.Node(h.ctx, matter)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := h.NodeCompletion(h.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if !completion.LocallyComplete || !completion.Sealed || len(completion.Pending) != 0 {
		t.Fatalf("dismissed completion = %+v, want locally complete and sealed", completion)
	}
	requirements, err := h.EffectiveGateRequirements(h.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 1 || requirements[0].State != GateRequirementDismissed ||
		requirements[0].ClosedBy != "" || requirements[0].ClosedAt != nil ||
		requirements[0].DismissedBy != ActorHuman || requirements[0].DismissedAt == nil ||
		requirements[0].DismissalReason != reason || !requirements[0].DismissedAt.Equal(events[0].OccurredAt.UTC()) {
		t.Fatalf("effective dismissed requirement = %+v, want distinct dismissal metadata", requirements)
	}
	closed, err := h.ClosedGates(h.ctx, matter)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0].State != GateRequirementDismissed || closed[0].DismissedBy != ActorHuman ||
		closed[0].DismissedAt == nil || closed[0].DismissalReason != reason || !closed[0].ClosedAt.IsZero() {
		t.Fatalf("closed-gate read model = %+v, want dismissal action and reason", closed)
	}
	if satisfied, err := h.GateSatisfied(h.ctx, h.Repo, matter, "reviewed-local"); err != nil || !satisfied {
		t.Fatalf("dismissed gate satisfaction = %v, err=%v, want true", satisfied, err)
	}
	archived, err := h.ArchivedMatters(h.ctx, h.Repo)
	if err != nil || len(archived) != 1 || archived[0].ID != matter {
		t.Fatalf("archive after dismissal = %+v, err=%v, want the Matter", archived, err)
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err = reopened.EffectiveGateRequirements(reopened.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 1 || requirements[0].State != GateRequirementDismissed ||
		requirements[0].DismissedBy != ActorHuman || requirements[0].DismissalReason != reason ||
		requirements[0].DismissedAt == nil || !requirements[0].DismissedAt.Equal(events[0].OccurredAt.UTC()) {
		t.Fatalf("dismissal after rebuild/reopen = %+v, want the same terminal read model", requirements)
	}
}

func TestGateDismissalUsesTheOwningVerifierActor(t *testing.T) {
	h := newHarness(t)
	if err := h.DeclareGate(h.ctx, h.Repo, "verified", ScaleStep); err != nil {
		t.Fatal(err)
	}
	matter := h.matter("role-dismissed", "A Matter with a verifier dismissal")
	step := h.step(matter, "step-01", "The Done step")
	h.start(matter)
	h.start(step)
	h.finish(step)
	dispatch := openDispatchForTest(h)
	spawnRoleForTest(h, dispatch, RoleVerifier)

	if _, err := commitGateDismissal(h, RoleVerifier.Actor(), dismissalEnv(h, h.Repo), step,
		dismissalPayload("verified", ScaleStep, "the verification service is unavailable")); err != nil {
		t.Fatalf("owning verifier dismissal: %v", err)
	}
	node, err := h.Node(h.ctx, step)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := h.EffectiveGateRequirements(h.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 1 || requirements[0].State != GateRequirementDismissed || requirements[0].DismissedBy != RoleVerifier.Actor() {
		t.Fatalf("verifier dismissal requirement = %+v, want role:verifier dismissal", requirements)
	}
}

func TestGateDismissalRejectsMalformedAndInvalidTerminalActions(t *testing.T) {
	h := newHarness(t)
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatal(err)
	}
	planned := h.matter("planned-dismissal", "Still planned")
	inProgress := h.matter("in-progress-dismissal", "Still in progress")
	h.start(inProgress)
	done := h.matter("done-dismissal", "Done and open")
	h.start(done)
	h.finish(done)

	attempt := func(subject string, actor Actor, env Env, payload any, want string) {
		t.Helper()
		beforeEvents, err := h.Events(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		before := len(beforeEvents)
		_, err = commitGateDismissal(h, actor, env, subject, payload)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("dismissal of %s error = %v, want %q", subject, err, want)
		}
		afterEvents, err := h.Events(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(afterEvents); got != before {
			t.Fatalf("dismissal refusal for %s changed event count from %d to %d", subject, before, got)
		}
	}

	valid := dismissalPayload("reviewed-local", ScaleMatter, "a real reason")
	for _, tc := range []struct {
		name    string
		payload any
		want    string
	}{
		{"array", json.RawMessage(`[]`), "payload must be a JSON object"},
		{"unknown field", json.RawMessage(`{"gate":"reviewed-local","scale":"matter","reason":"a real reason","extra":true}`), "unknown field"},
		{"trailing data", json.RawMessage(`{"gate":"reviewed-local","scale":"matter","reason":"a real reason"} {}`), "marshal payload"},
		{"missing reason", json.RawMessage(`{"gate":"reviewed-local","scale":"matter"}`), "non-empty reason"},
		{"blank reason", dismissalPayload("reviewed-local", ScaleMatter, " \t"), "non-empty reason"},
	} {
		t.Run(tc.name, func(_ *testing.T) {
			attempt(done, ActorHuman, dismissalEnv(h, h.Repo), tc.payload, tc.want)
		})
	}
	attempt(planned, ActorHuman, dismissalEnv(h, h.Repo), valid, "Done node")
	attempt(inProgress, ActorHuman, dismissalEnv(h, h.Repo), valid, "Done node")
	attempt(done, ActorHuman, dismissalEnv(h, h.Repo), dismissalPayload("reviewed-local", ScaleStep, "a real reason"), "against a matter")
	attempt(done, ActorHuman, dismissalEnv(h, h.Repo), dismissalPayload("not-declared", ScaleMatter, "a real reason"), "not declared")
	builderDispatch := openDispatchForTest(h)
	spawnRoleForTest(h, builderDispatch, RoleBuilder)
	attempt(done, RoleActor("builder"), dismissalEnv(h, h.Repo), valid, "actor role:builder")

	if _, err := commitGateDismissal(h, ActorHuman, dismissalEnv(h, h.Repo), done, valid); err != nil {
		t.Fatal(err)
	}
	attempt(done, ActorHuman, dismissalEnv(h, h.Repo), valid, "already satisfied")

	// A prospective exemption is satisfaction too, and cannot be replaced by a
	// dismissal. This also covers the no-reversal rule without adding an unseal
	// operation.
	exempt := h.matter("exempt-dismissal", "Already exempt")
	h.start(exempt)
	h.finish(exempt)
	h.commit(Draft{Type: TypeGateClosed, Subject: exempt, Payload: GateClosed{Gate: "reviewed-local", Scale: ScaleMatter}})
	if err := h.DeclareGate(h.ctx, h.Repo, "exempt-gate", ScaleMatter); err != nil {
		t.Fatal(err)
	}
	attempt(exempt, ActorHuman, dismissalEnv(h, h.Repo), dismissalPayload("exempt-gate", ScaleMatter, "a real reason"), "already satisfied")

	otherRepo := h.attachRepo(RepoAttached{Label: "other"})
	attempt(done, ActorHuman, dismissalEnv(h, otherRepo), valid, "targets Repo")
}

func TestGateDismissalMigrationAddsOnlyTheNewEventType(t *testing.T) {
	path := t.TempDir() + "/wip.db"
	legacy := append([]migration{}, register[:10]...)
	h := newHarnessAt(t, path, legacy, 10)
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatal(err)
	}
	matter := h.matter("migrated-dismissal", "Dismissal after migration")
	h.start(matter)
	h.finish(matter)
	log := h.rowsOf("events", "")

	migrated, err := h.reopen(append([]migration{}, register[:11]...), 11)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion() != 11 {
		t.Fatalf("schema version after dismissal migration = %d, want 11", migrated.SchemaVersion())
	}
	if got := len(migrated.rowsOf("event_types", "type = ?", TypeGateDismissed)); got != 1 {
		t.Fatalf("dismissal taxonomy rows = %d, want one", got)
	}
	if got := migrated.rowsOf("events", ""); len(got) != len(log) {
		t.Fatalf("migration changed event count from %d to %d", len(log), len(got))
	}
	if _, err := commitGateDismissal(migrated, ActorHuman, dismissalEnv(migrated, migrated.Repo), matter,
		dismissalPayload("reviewed-local", ScaleMatter, "migration preserved the open gate")); err != nil {
		t.Fatal(err)
	}
}
