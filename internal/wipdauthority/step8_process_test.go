package wipdauthority

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestStep8RegistryIsClosed(t *testing.T) {
	f := newM5CommandFixture(t)
	for _, omitted := range append([]string{""}, "dependency.add", "dependency.remove", "reference.bind", "reference.unbind", "reference.rebind", "batch.sweep-anonymous", "gate.close") {
		t.Run("omit-"+omitted, func(t *testing.T) {
			complete, err := NewM6Step8Registry()
			if err != nil {
				t.Fatal(err)
			}
			registry := operation.NewRegistry()
			for _, definition := range complete.Definitions() {
				if definition.Metadata().Operation.Name == omitted {
					continue
				}
				if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result { return operation.Result{} }); err != nil {
					t.Fatal(err)
				}
			}
			config := f.config
			config.Registry = registry
			_, err = NewM6LabServer(f.profile, f.serverCert, config)
			if (err == nil) != (omitted == "") {
				t.Fatalf("closed Step 8 registry omitted %q: %v", omitted, err)
			}
			if _, err = NewM5LabServer(f.profile, f.serverCert, config); err == nil {
				t.Fatal("M5 accepted Step 8 registry")
			}
		})
	}
}

func TestStep8ThroughAuthenticatedWipdProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated AF_UNIX IPC is Linux-only")
	}
	for _, scenario := range []string{"lost-terminal-dependency.add", "lost-terminal-dependency.remove", "lost-terminal-reference.bind", "lost-terminal-reference.rebind", "lost-terminal-reference.unbind", "lost-local-response", "aggregate-lifecycle", "local-step7", "local-m5", "authority-step7"} {
		t.Run(scenario, func(t *testing.T) { runStep8Process(t, scenario) })
	}
}

func runStep8Process(t *testing.T, scenario string) {
	ctx := context.Background()
	f := newM5CommandFixture(t)
	registry, err := NewM6Step8Registry()
	if scenario == "authority-step7" {
		registry, err = NewM6Step7Registry()
	}
	if err != nil {
		t.Fatal(err)
	}
	config := f.config
	config.Registry = registry
	f.server, err = NewM6LabServer(f.profile, f.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	var drop m5OrdinaryCommandResponseDrop
	var faults m5ConnectionFaults
	var trace m6AuthorityHTTPTrace
	var hold atomic.Bool
	committed, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	handler := injectM5ConnectionFaults(discardOneOrdinaryCommandResponse(f.server.http.Handler, &drop), &faults)
	f.server.http.Handler = traceM6AuthorityHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(sweepProcessTerminalHoldWriter{w, &hold, committed, resume}, r)
	}), &trace)
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- f.server.Serve(authorityCtx, f.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case err := <-served:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("authority did not stop")
		}
	})
	root, err := os.MkdirTemp("/tmp", "s8-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	catalogue := []string{"m6-step8"}
	if scenario == "local-step7" {
		catalogue = []string{"m6-step7"}
	}
	if scenario == "local-m5" {
		catalogue = nil
	}
	env, err := prepareM5ProcessEnvironment(t, f, root, "env", m5TestEnv, f.environment, f.clientCert.Certificate[0], f.clientCert.Certificate[1], catalogue...)
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	if scenario == "authority-step7" {
		wait, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		output, err := exec.CommandContext(wait, binary, "--profile-root", env.profileRoot).CombinedOutput()
		if err == nil || wait.Err() != nil || !strings.Contains(string(output), "connected authority startup refused") {
			t.Fatalf("Step 8 did not fail closed against Step 7 authority: %v %s", err, output)
		}
		anchor, err := f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil || anchor.EventCount != 0 || faults.commandSubmits.Load() != 0 {
			t.Fatalf("unsupported authority changed history: %+v %v", anchor, err)
		}
		return
	}
	client, stopDaemon, output := startWipdForBirthReleaseRecovery(t, binary, env.profileRoot, true)
	defer func() { _ = client.Close(); stopDaemon() }()
	restart := func() {
		_ = client.Close()
		stopDaemon()
		client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, env.profileRoot, true)
		_ = client.Close()
		client, err = wipd.ConnectM6Step8(ctx, env.profileRoot)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Existing clients must not offer Step 8 even to a new-profile daemon.
	probeID := repairTransportID(150)
	probe := operation.Command{ID: probeID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1, EnvironmentID: m5TestEnv, EnvironmentSequence: 1, ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: probeID, Request: operation.Request{Operation: operation.ReferenceBindV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo}, Input: operation.ReferenceBindInput{MatterID: repairTransportID(151), Reference: "OLD"}}}
	if _, err = client.ExecuteCommand(ctx, probe); err == nil || !strings.Contains(err.Error(), "operation.unsupported-version") {
		t.Fatalf("Step 7 client offered Step 8: %v", err)
	}
	_ = client.Close()
	old, err := wipd.Connect(ctx, env.profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecuteCommand(ctx, probe); err == nil || !strings.Contains(err.Error(), "operation.unsupported-version") {
		t.Fatalf("M5 client offered Step 8: %v", err)
	}
	_ = old.Close()
	client, err = wipd.ConnectM6Step8(ctx, env.profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "local-step7" || scenario == "local-m5" {
		if _, err = client.ExecuteCommand(ctx, probe); err == nil || !strings.Contains(err.Error(), "operation.unsupported-version") {
			t.Fatalf("old profile advertised Step 8: %v", err)
		}
		anchor, err := f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil || anchor.EventCount != 0 || faults.commandSubmits.Load() != 0 {
			t.Fatalf("unsupported local profile changed history: %+v %v", anchor, err)
		}
		return
	}
	sequence := uint64(1)
	command := func(id int, definition operation.Definition, input operation.Input) operation.Command {
		commandID := repairTransportID(id)
		item := operation.Command{ID: commandID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1, EnvironmentID: m5TestEnv, EnvironmentSequence: sequence, ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: commandID, Request: operation.Request{Operation: definition.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo}, Input: input}}
		sequence++
		return item
	}
	var lifecycleMatterID string
	if scenario == "aggregate-lifecycle" {
		// Release this provisional birth before creating DeliveryAuthority
		// attempts, which intentionally remain attempt-prepared after outcome install.
		lifecycleCreate := command(179, operation.MatterCreateV1, operation.MatterCreateInput{Title: "Lifecycle aggregate refresh"})
		created, err := client.ExecuteCommand(ctx, lifecycleCreate)
		lifecycleMatter, ok := created.Output.(operation.MatterCreateOutput)
		if err != nil || created.Code != operation.ResultSucceeded || !ok || lifecycleMatter.ID == "" {
			t.Fatalf("create lifecycle Matter = %+v, %v; daemon=%s", created, err, output.String())
		}
		lifecycleMatterID = lifecycleMatter.ID
		released, err := client.ReleaseBirthClaim(ctx, lifecycleMatterID, repairTransportID(180), operation.Actor("human"))
		if err != nil || released.Code != operation.ResultSucceeded || released.CommandID != repairTransportID(180) || len(released.Receipt) == 0 {
			t.Fatalf("release lifecycle Matter birth claim = %+v, %v; daemon=%s", released, err, output.String())
		}
		sequence++ // Birth release consumed the next Environment entry.
	}
	var matters []string
	for i := 0; i < 2; i++ {
		item := command(152+i, operation.MatterCreateV1, operation.MatterCreateInput{Title: fmt.Sprintf("Step 8 Matter %d", i)})
		got, err := client.ExecuteCommand(ctx, item)
		matter, ok := got.Output.(operation.MatterCreateOutput)
		if err != nil || got.Code != operation.ResultSucceeded || !ok {
			t.Fatalf("birth: %+v %v daemon=%s", got, err, output.String())
		}
		matters = append(matters, matter.ID)
	}
	a, b := matters[0], matters[1]
	ops := []struct {
		definition operation.Definition
		input      operation.Input
		want       operation.Output
		problem    string
		kind       string
		subject    string
	}{
		{operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: a, BlockerID: b}, nil, "", "dependency.added", a},
		{operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: a, BlockerID: b}, operation.DependencyOutput{BlockedID: a, BlockerID: b}, "", "dependency.removed", a},
		{operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: a, Reference: "OLD"}, operation.ReferenceOutput{MatterID: a, Reference: "OLD"}, "", "reference.added", a},
		{operation.ReferenceRebindV1, operation.ReferenceRebindInput{MatterID: a, From: "OLD", To: "NEW"}, operation.ReferenceOutput{MatterID: a, Reference: "NEW", PreviousReference: "OLD"}, "", "reference.rebound", a},
		{operation.ReferenceUnbindV1, operation.ReferenceUnbindInput{MatterID: a, Reference: "NEW"}, operation.ReferenceOutput{MatterID: a, Reference: "NEW"}, "", "reference.removed", a},
	}
	// A terminal refusal keeps the established command-start quarantine
	// barrier. Each fixture therefore ends with one operation's refusal.
	refused := ops[len(ops)-1]
	refused.want, refused.kind, refused.problem = nil, "", "refusal.reference-missing"
	switch strings.TrimPrefix(scenario, "lost-terminal-") {
	case "dependency.add":
		refused.definition, refused.input, refused.problem = operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: a, BlockerID: a}, "refusal.dependency-cycle"
	case "dependency.remove":
		refused.definition, refused.input, refused.problem = operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: a, BlockerID: b}, "refusal.dependency-missing"
	case "reference.bind":
		refused.definition, refused.input, refused.problem = operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: repairTransportID(151), Reference: "OLD"}, "refusal.reference-matter"
	case "reference.rebind":
		refused.definition, refused.input = operation.ReferenceRebindV1, operation.ReferenceRebindInput{MatterID: a, From: "OLD", To: "OTHER"}
	}
	if scenario != "aggregate-lifecycle" {
		ops = append(ops, refused)
	}
	identity := wipdjournal.Identity{RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv, OwnerRootSPKI: f.profile.OwnerRootSPKI()}
	openJournal := func() *wipdjournal.Journal {
		_ = client.Close()
		stopDaemon()
		j, err := wipdjournal.Open(filepath.Join(env.profileRoot, "environment-journal"), identity)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	db, err := sql.Open("sqlite", filepath.Join(f.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	invalidClaimID := repairTransportID(159)
	invalidClaimCommand := operation.Command{
		ID: invalidClaimID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: sequence,
		ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: invalidClaimID,
		Request: operation.Request{
			Operation: operation.ReferenceBindV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Claim: &operation.ClaimContext{ID: a, Epoch: "1"},
			Input: operation.ReferenceBindInput{MatterID: a, Reference: "REJECTED"},
		},
	}
	submitsBeforeInvalidClaim := faults.commandSubmits.Load()
	if _, err = client.ExecuteCommand(ctx, invalidClaimCommand); err == nil || !strings.Contains(err.Error(), "protocol.malformed-message") {
		t.Fatalf("ClaimNone reference command was not rejected before admission: %v", err)
	}
	var invalidSubmissions, invalidClaimJournalEntries int
	if err = db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, m5TestDomain, invalidClaimID).Scan(&invalidSubmissions); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, m5TestDomain, invalidClaimID).Scan(&invalidClaimJournalEntries); err != nil {
		t.Fatal(err)
	}
	if invalidSubmissions != 0 || invalidClaimJournalEntries != 0 || faults.commandSubmits.Load() != submitsBeforeInvalidClaim {
		t.Fatalf("rejected ClaimNone context reached authority: submissions=%d claim-journal=%d submit-count=%d/%d",
			invalidSubmissions, invalidClaimJournalEntries, faults.commandSubmits.Load(), submitsBeforeInvalidClaim)
	}
	journal := openJournal()
	if _, err = journal.Get(invalidClaimID); !errors.Is(err, wipdjournal.ErrNotFound) {
		t.Fatalf("rejected ClaimNone context entered the Environment journal: %v", err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	restart()
	var firstEdge string
	var previousProjection *wipdjournal.Step8Projection
	for index, op := range ops {
		metadata := op.definition.Metadata()
		if metadata.Delivery != operation.DeliveryAuthority || metadata.Claim != operation.ClaimNone {
			t.Fatalf("%s metadata = delivery %s claim %s, want DeliveryAuthority + ClaimNone",
				metadata.Operation, metadata.Delivery, metadata.Claim)
		}
		item := command(160+index, op.definition, op.input)
		hash := m5CommandHash(t, item)
		before, err := f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(scenario, "lost-terminal-") {
			drop.armed.Store(true)
			faults.blockReceiptQuery.Store(true)
		}
		var got operation.Result
		if scenario == "lost-local-response" && index == 0 {
			hold.Store(true)
			wait, cancel := context.WithCancel(ctx)
			finished := make(chan error, 1)
			go func() { _, err := client.ExecuteCommand(wait, item); finished <- err }()
			select {
			case <-committed:
			case <-time.After(10 * time.Second):
				t.Fatal("authority did not commit")
			}
			cancel()
			select {
			case err := <-finished:
				var exchange *wipd.ExchangeError
				if !errors.As(err, &exchange) || !exchange.Uncertain {
					t.Fatalf("local loss not uncertain: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled wait stuck")
			}
			close(resume)
			// A local disconnect ends only the wait; the daemon must finish
			// installing before its restart, without another authority effect.
			local, err := sql.Open("sqlite", "file:"+filepath.Join(env.profileRoot, "environment-journal", "command-journal.sqlite")+"?mode=ro&_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				var receipt []byte
				err = local.QueryRow(`SELECT canonical_receipt FROM authority_command_outcomes WHERE command_id=?`, item.ID).Scan(&receipt)
				if err == nil {
					break
				}
				if !errors.Is(err, sql.ErrNoRows) || time.Now().After(deadline) {
					t.Fatalf("local loss prevented install: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = local.Close()
			restart()
			got, err = client.ExecuteCommand(ctx, item)
			if err != nil {
				t.Fatalf("execute after local restart: %v", err)
			}
		} else {
			got, err = client.ExecuteCommand(ctx, item)
		}
		original, statusErr := f.store.QueryCommand(ctx, m5TestDomain, item.ID, hash, 1, f.peer, m5TestEnv, time.Now())
		if statusErr != nil || len(original.SignedReceipt) == 0 {
			t.Fatalf("not durably terminal: %+v %v", original, statusErr)
		}
		if strings.HasPrefix(scenario, "lost-terminal-") {
			if err == nil || !drop.dropped.Load() {
				t.Fatalf("terminal not lost: %v", err)
			}
			j := openJournal()
			entry, entryErr := j.Get(item.ID)
			installed, installErr := j.InstallSnapshot(ctx)
			if entryErr != nil || installErr != nil || entry.State != wipdjournal.StateAttemptPrepared || entry.RequestHash != hash || entry.JournalPosition != 0 || entry.Delivery != operation.DeliveryAuthority || installed.Anchor.EventCount != before.EventCount || installed.Anchor.Digest != before.Digest || installed.Anchor.EventID == nil || *installed.Anchor.EventID != before.EventID {
				t.Fatalf("lost reply attempt changed: state=%s hash=%s installed=%+v authority=%+v errors=%v/%v", entry.State, entry.RequestHash, installed.Anchor, before, entryErr, installErr)
			}
			if _, ok := installed.Receipts[item.ID]; ok {
				t.Fatal("lost reply installed a terminal")
			}
			_ = j.Close()
			faults.blockReceiptQuery.Store(false)
			restart()
			got, err = client.ExecuteCommand(ctx, item)
		}
		if err != nil {
			t.Fatalf("%s: %v daemon=%s trace=%v", op.definition.Metadata().Operation, err, output.String(), trace.snapshot())
		}
		if index == 0 {
			value, ok := got.Output.(operation.DependencyOutput)
			if !ok || value.EdgeID == "" || value.BlockedID != a || value.BlockerID != b {
				t.Fatalf("typed edge identity/direction: %+v", got)
			}
			firstEdge = value.EdgeID
			op.want = value
		}
		if op.problem == "" {
			if got.Code != operation.ResultSucceeded || got.Output != op.want {
				t.Fatalf("typed success: %+v want=%+v", got, op.want)
			}
		} else if got.Code != operation.ResultRefused || got.Output != nil || got.Problem == nil || string(got.Problem.Code) != op.problem {
			t.Fatalf("typed refusal: %+v", got)
		}
		after, err := f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		wantCount := before.EventCount
		if op.problem == "" {
			wantCount++
		}
		if err != nil || after.EventCount != wantCount || op.problem != "" && after != before {
			t.Fatalf("wrong effects/refusal prefix: %+v %v", after, err)
		}
		// Independent reopened install and immutable authority receipt must
		// agree before exact replay. No Step 8 attempt enters either journal.
		j := openJournal()
		installed, err := j.InstallSnapshot(ctx)
		receipt, ok := installed.Receipts[item.ID]
		if err != nil || !ok || !bytes.Equal(receipt.CanonicalReceipt, original.Receipt) || installed.Anchor.EventCount != after.EventCount || installed.Anchor.Digest != after.Digest || installed.Anchor.EventID == nil || *installed.Anchor.EventID != after.EventID || receipt.JournalPosition != 0 {
			t.Fatalf("reopened receipt/prefix: found=%t position=%d installed=%+v authority=%+v err=%v", ok, receipt.JournalPosition, installed.Anchor, after, err)
		}
		entry, entryErr := j.Get(item.ID)
		if entryErr != nil || entry.Delivery != operation.DeliveryAuthority || entry.JournalPosition != 0 || entry.Command.Request.Claim != nil || entry.EnvironmentSeq != item.EnvironmentSequence {
			t.Fatalf("Environment entry is not an unclaimed authority attempt at its exact sequence: entry=%+v error=%v", entry, entryErr)
		}
		if op.problem != "" {
			if !reflect.DeepEqual(installed.Step8Projection, previousProjection) {
				t.Fatalf("refusal changed installed dependency/reference projection: before=%+v after=%+v", previousProjection, installed.Step8Projection)
			}
		} else {
			projection := installed.Step8Projection
			if projection == nil || len(projection.Candidates) != 0 {
				t.Fatalf("off policy should install a projection without tracker candidates: %+v", projection)
			}
			switch index {
			case 0:
				if len(projection.Dependencies) != 1 || !projection.Dependencies[0].Live || projection.Dependencies[0].BlockedID != a || projection.Dependencies[0].BlockerID != b ||
					len(projection.References) != 0 || len(projection.Aggregates) != 0 {
					t.Fatalf("dependency add transfer/install projection = %+v", projection)
				}
			case 1:
				if len(projection.Dependencies) != 1 || projection.Dependencies[0].Live || projection.Dependencies[0].TombstoneEventID == "" ||
					projection.Dependencies[0].BlockedID != a || projection.Dependencies[0].BlockerID != b {
					t.Fatalf("dependency remove transfer/install projection = %+v", projection)
				}
			case 2:
				if len(projection.References) != 1 || projection.References[0].Reference != "OLD" || projection.References[0].RemovedEventID != "" ||
					len(projection.Aggregates) != 1 || projection.Aggregates[0].Reference != "OLD" || projection.Aggregates[0].Members != 1 || projection.Aggregates[0].Disposition != "" {
					t.Fatalf("reference bind transfer/install projection = %+v", projection)
				}
			case 3:
				removedOld, activeNew := false, false
				for _, reference := range projection.References {
					removedOld = removedOld || reference.Reference == "OLD" && reference.RemovedEventID != ""
					activeNew = activeNew || reference.Reference == "NEW" && reference.RemovedEventID == ""
				}
				if len(projection.References) != 2 || !removedOld || !activeNew || len(projection.Aggregates) != 1 || projection.Aggregates[0].Reference != "NEW" {
					t.Fatalf("reference rebind transfer/install projection = %+v", projection)
				}
			case 4:
				if len(projection.References) != 2 || projection.References[0].RemovedEventID == "" || projection.References[1].RemovedEventID == "" || len(projection.Aggregates) != 0 {
					t.Fatalf("reference unbind transfer/install projection = %+v", projection)
				}
			}
			previousProjection = projection
		}
		records, err := j.EventRecords(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var effects []wipdwire.EventRecord
		for _, record := range records {
			fields, err := wipdwire.DecodeCanonicalMap(record.Record, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			if err != nil {
				t.Fatal(err)
			}
			if fields["command_id"] == item.ID {
				if fields["kind"] != op.kind || fields["subject_id"] != op.subject || fields["repo_id"] != m5TestRepo || fields["request_hash"] != hash || fields["acted_at"] != item.ActedAt {
					t.Fatalf("effect substitution: %#v", fields)
				}
				payload := fields["payload"].(map[string]any)
				switch input := op.input.(type) {
				case operation.DependencyAddInput:
					if payload["edge"] != firstEdge || payload["blocker"] != input.BlockerID {
						t.Fatal("add changed edge identity/endpoints")
					}
				case operation.DependencyRemoveInput:
					if payload["edge"] != firstEdge || payload["blocker"] != input.BlockerID {
						t.Fatal("remove changed edge identity/endpoints")
					}
				case operation.ReferenceBindInput:
					if payload["ref"] != input.Reference || payload["tracker_push_level"] != "off" {
						t.Fatal("bind changed reference/policy snapshot")
					}
				case operation.ReferenceUnbindInput:
					if payload["ref"] != input.Reference || payload["tracker_push_level"] != "off" {
						t.Fatal("unbind changed reference/policy snapshot")
					}
				case operation.ReferenceRebindInput:
					if payload["from"] != input.From || payload["to"] != input.To || payload["tracker_push_level"] != "off" {
						t.Fatal("rebind changed endpoints/policy snapshot")
					}
				}
				effects = append(effects, record)
			}
		}
		if len(effects) != int(wantCount-before.EventCount) {
			t.Fatal("wrong retained command lineage")
		}
		fields, err := wipdwire.DecodeCanonicalMap(original.Receipt, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if err != nil {
			t.Fatal(err)
		}
		if len(effects) == 0 {
			if fields["accepted_events"] != nil {
				t.Fatal("refusal has accepted range")
			}
		} else {
			accepted, ok := fields["accepted_events"].(map[string]any)
			if !ok || accepted["event_count"] != uint64(1) || accepted["first_event_id"] != effects[0].EventID || accepted["last_event_id"] != effects[0].EventID {
				t.Fatalf("range substitution: %#v", fields)
			}
		}
		_ = j.Close()
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, m5TestDomain, item.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("Step 8 entered authority claim journal: %d %v", count, err)
		}
		restart()
		submits := faults.commandSubmits.Load()
		replay, err := client.ExecuteCommand(ctx, item)
		if err != nil || replay.Code != got.Code || replay.Output != got.Output || got.Problem != nil && (replay.Problem == nil || replay.Problem.Code != got.Problem.Code) || faults.commandSubmits.Load() != submits {
			t.Fatalf("replay changed outcome/resubmitted: %+v %v", replay, err)
		}
		changed := item
		changed.Request.Actor = "role:other"
		if _, err = client.ExecuteCommand(ctx, changed); err == nil || !strings.Contains(err.Error(), "command.id-conflict") {
			t.Fatalf("changed hash did not conflict: %v", err)
		}
		status, err := f.store.QueryCommand(ctx, m5TestDomain, item.ID, hash, 1, f.peer, m5TestEnv, time.Now())
		if err != nil || !bytes.Equal(status.Receipt, original.Receipt) || !bytes.Equal(status.SignedReceipt, original.SignedReceipt) {
			t.Fatal("recovery replaced signed receipt")
		}
		unchanged, err := f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil || unchanged != after || faults.commandSubmits.Load() != submits {
			t.Fatal("replay/conflict changed authority")
		}
	}
	if scenario == "aggregate-lifecycle" {
		acquired, err := client.AcquireClaim(ctx, repairTransportID(181), lifecycleMatterID,
			repairTransportID(182), repairTransportID(183), repairTransportID(184), operation.Actor("human"))
		if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil {
			t.Fatalf("acquire lifecycle Matter claim = %+v, %v; daemon=%s", acquired, err, output.String())
		}
		sequence++ // Claim acquisition consumed the next Environment entry.
		claim := &operation.ClaimContext{ID: acquired.Grant.ClaimID, Epoch: fmt.Sprint(acquired.Grant.ClaimEpoch)}
		commandContext := operation.Context{Repo: m5TestRepo, Clone: repairTransportID(182), Worktree: repairTransportID(183)}
		bind := command(185, operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: lifecycleMatterID, Reference: "LIFECYCLE"})
		bound, err := client.ExecuteCommand(ctx, bind)
		if err != nil || bound.Code != operation.ResultSucceeded || bound.Output != (operation.ReferenceOutput{MatterID: lifecycleMatterID, Reference: "LIFECYCLE"}) {
			t.Fatalf("lifecycle reference bind = %+v, %v; daemon=%s", bound, err, output.String())
		}
		assertLifecycleSnapshot := func(journal *wipdjournal.Journal, disposition, commandID, eventKind, from, to string) {
			t.Helper()
			installed, snapshotErr := journal.InstallSnapshot(ctx)
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			projection := installed.Step8Projection
			if projection == nil || len(projection.Aggregates) != 1 || projection.Aggregates[0].Reference != "LIFECYCLE" ||
				projection.Aggregates[0].Members != 1 || projection.Aggregates[0].Disposition != disposition || len(projection.Candidates) != 0 {
				t.Fatalf("reopened %s aggregate/candidates = %+v, want disposition=%q and no candidates under off policy", eventKind, projection, disposition)
			}
			activeReference := false
			for _, reference := range projection.References {
				if reference.Reference == "LIFECYCLE" {
					activeReference = reference.MatterID == lifecycleMatterID && reference.RemovedEventID == ""
				}
			}
			if !activeReference {
				t.Fatalf("reopened projection lost the bound lifecycle reference: %+v", projection.References)
			}
			if commandID == "" {
				return
			}
			records, recordsErr := journal.EventRecords(ctx)
			if recordsErr != nil {
				t.Fatal(recordsErr)
			}
			found := false
			for _, record := range records {
				fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
					"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if fields["command_id"] != commandID {
					continue
				}
				payload, ok := fields["payload"].(map[string]any)
				if !ok || fields["domain_id"] != m5TestDomain || fields["repo_id"] != m5TestRepo || fields["subject_id"] != lifecycleMatterID || fields["kind"] != eventKind ||
					payload["from"] != from || payload["to"] != to {
					t.Fatalf("installed lifecycle event does not match authority command: %#v", fields)
				}
				found = true
			}
			if !found {
				t.Fatalf("reopened Environment has no %s event for command %s", eventKind, commandID)
			}
		}
		journal := openJournal()
		assertLifecycleSnapshot(journal, "", "", "reference bind", "", "")
		if err = journal.Close(); err != nil {
			t.Fatal(err)
		}
		restart()
		start := command(186, operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: lifecycleMatterID})
		start.Request.Claim, start.Request.Context = claim, commandContext
		started, err := client.ExecuteCommand(ctx, start)
		if err != nil || started.Code != operation.ResultSucceeded || started.Output != (operation.MatterLifecycleOutput{MatterID: lifecycleMatterID, State: "in-progress"}) {
			t.Fatalf("authenticated Matter start = %+v, %v; daemon=%s", started, err, output.String())
		}
		journal = openJournal()
		assertLifecycleSnapshot(journal, "active", start.ID, "matter.started", "planned", "in-progress")
		if err = journal.Close(); err != nil {
			t.Fatal(err)
		}
		restart()
		cancelCommand := command(187, operation.MatterCancelV1, operation.NodeLifecycleInput{NodeID: lifecycleMatterID, Reason: "scope changed"})
		cancelCommand.Request.Claim, cancelCommand.Request.Context = claim, commandContext
		canceled, err := client.ExecuteCommand(ctx, cancelCommand)
		if err != nil || canceled.Code != operation.ResultSucceeded || canceled.Output != (operation.MatterLifecycleOutput{MatterID: lifecycleMatterID, State: "canceled"}) {
			t.Fatalf("authenticated Matter cancel = %+v, %v; daemon=%s", canceled, err, output.String())
		}
		journal = openJournal()
		assertLifecycleSnapshot(journal, "canceled", cancelCommand.ID, "matter.canceled", "in-progress", "canceled")
		if err = journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !traceContains(trace.snapshot(), "request=command.submit response=submission.accepted,command.terminal") {
		t.Fatalf("missing authenticated authority submit: %v", trace.snapshot())
	}
	if scenario != "aggregate-lifecycle" {
		for _, entry := range trace.snapshot() {
			if strings.Contains(entry, "request=claim-journal.") {
				t.Fatalf("Step 8 used claim journal: %s", entry)
			}
		}
	}
}
