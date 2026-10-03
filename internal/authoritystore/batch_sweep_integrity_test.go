package authoritystore

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func TestBatchSweepClosingEnvironmentAndCurrentAcquiredClaim(t *testing.T) {
	t.Run("different authenticated Environment", func(t *testing.T) {
		x := newSweepFixture(t, false, false, false)
		f := x.f
		d, owner := identity(domainA, 7)
		ca, leafKey := key("step4-ca"), key("sweep-other-environment")
		caDER := caFixture(t, ca, f.now)
		leaf := leafFixture(t, leafKey, ca, caDER, domainA, envB, d.OwnerKeyID, 7, f.now, 104)
		grant := grantFixture(t, owner, d, "environment-enroll", claimTestID(96), envB, leafKey, 2)
		if _, err := f.s.IssueEnvironmentCertificate(context.Background(), domainA, envB, grant, csrFixture(t, leafKey, "Environment B"), [][]byte{leaf, caDER}, f.now); err != nil {
			t.Fatal(err)
		}
		peer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
		command := x.command(90, x.acquired)
		command.EnvironmentID, command.EnvironmentSequence = envB, 1
		status, err := f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), peer, f.now, "", signWith(f.key))
		if err != nil {
			t.Fatal(err)
		}
		sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
		if err = checkSchema(f.s.db); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("new acquired claim after release", func(t *testing.T) {
		x := newSweepFixture(t, false, false, false)
		anchor := x.f.anchor(t)
		allocation := claimTestAllocation(2, anchor, 120, 121)
		allocation.BatchID = x.allocation.BatchID
		x.f.acquire(t, 20, x.next, anchor, allocation)
		x.next++
		command := x.command(90, x.acquired)
		status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
		if err != nil {
			t.Fatal(err)
		}
		sweepTestResult(t, status, "", operation.ProblemBatchSweepNotEligible)
		if err = checkSchema(x.f.s.db); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBatchSweepAuthoritativeCloseCorruption(t *testing.T) {
	for _, test := range []struct{ name, trigger, query string }{
		{"missing signed wrapper", "terminal_receipts_immutable", `UPDATE terminal_receipts SET wrapper=x'00' WHERE command_id=?`},
		{"receipt corruption", "terminal_receipts_immutable", `UPDATE terminal_receipts SET receipt=x'00' WHERE command_id=?`},
		{"no accepted positions", "terminal_receipts_immutable", `UPDATE terminal_receipts SET first_position=NULL,last_position=NULL WHERE command_id=?`},
		{"range substitution", "terminal_receipts_immutable", `UPDATE terminal_receipts SET first_position=last_position WHERE command_id=?`},
		{"artifact coordinates", "terminal_receipts_immutable", `UPDATE terminal_receipts SET artifact_sequence=(SELECT artifact_sequence FROM claim_grants LIMIT 1) WHERE command_id=?`},
		{"non-normal close", "claim_closes_immutable", `UPDATE claim_closes SET kind='stand-down',barrier=NULL,reason_digest='reason' WHERE command_id=?`},
		{"close barrier substitution", "claim_closes_immutable", `UPDATE claim_closes SET barrier=x'00' WHERE command_id=?`},
		{"regressed acquired close", "claims_immutable", `UPDATE claims SET close_command_id=NULL WHERE close_command_id=?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			if _, err := x.f.s.db.Exec(`DROP TRIGGER ` + test.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := x.f.s.db.Exec(test.query, x.acquired.ReleaseCommandID); err != nil {
				t.Fatal(err)
			}
			command := x.command(90, x.acquired)
			status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
			if err != nil {
				t.Fatal(err)
			}
			sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
		})
	}
	for _, kind := range []string{"acquired", "implicit birth"} {
		for _, field := range []string{"subject_id", "payload", "kind", "request_hash", "unknown envelope field"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				x := newSweepFixture(t, false, false, false)
				ref, eventID := x.acquired, claimTestID(111)
				if kind == "implicit birth" {
					ref, eventID = x.birth, claimTestID(112)
				}
				rewriteGateAuthorityEvent(t, x.f.root, eventID, func(fields map[string]cbor.RawMessage) error {
					value := any(claimTestID(999))
					if field == "payload" {
						value = map[string]any{"claim_id": ref.ClaimID, "claim_epoch": ref.ClaimEpoch, "dispatch_id": nil, "barrier_digest": digestBytes([]byte("other barrier"))}
					} else if field == "kind" {
						value = "claim.stood-down"
					} else if field == "request_hash" {
						value = digestBytes([]byte("other request"))
					}
					return setGateEventField(fields, field, value)
				})
				if kind == "acquired" {
					// Rehash the later birth-release event too. A matching prefix
					// must not mask a wrong release event map/envelope.
					rewriteGateAuthorityEvent(t, x.f.root, claimTestID(112), func(map[string]cbor.RawMessage) error { return nil })
				}
				anchor := x.f.anchor(t)
				ref.InstalledPrefixAnchor = operation.ClaimClosePrefix{EventCount: anchor.EventCount, EventID: &anchor.EventID, Digest: anchor.Digest}
				command := x.command(90, ref)
				status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
				if err != nil {
					t.Fatal(err)
				}
				sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
			})
		}
	}
}

func TestBatchSweepReleaseSignatureAndUnsuccessfulReceipt(t *testing.T) {
	for _, mutation := range []string{"signature", "signed unsuccessful release"} {
		t.Run(mutation, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			ref := x.birth
			var raw, wrapper []byte
			if err := x.f.s.db.QueryRow(`SELECT receipt,wrapper FROM terminal_receipts WHERE command_id=?`, ref.ReleaseCommandID).Scan(&raw, &wrapper); err != nil {
				t.Fatal(err)
			}
			var fields map[string]cbor.RawMessage
			if err := canonicalDecode(wrapper, &fields); err != nil {
				t.Fatal(err)
			}
			if mutation == "signature" {
				var signature []byte
				if err := artifactDecoder.Unmarshal(fields["signature"], &signature); err != nil {
					t.Fatal(err)
				}
				signature[0] ^= 1
				fields["signature"] = encodeTest(t, signature)
			} else {
				var receipt map[string]cbor.RawMessage
				if err := canonicalDecode(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				receipt["result"] = encodeTest(t, map[string]any{"code": "result.refused", "output": nil, "problem_code": "refusal.claim-fenced"})
				receipt["accepted_events"] = encodeTest(t, nil)
				raw = encodeTest(t, receipt)
				fields["payload"], fields["payload_digest"] = encodeTest(t, raw), encodeTest(t, digestBytes(raw))
				delete(fields, "signature")
				unsigned := encodeTest(t, fields)
				fields["signature"] = encodeTest(t, ed25519.Sign(x.f.key, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...)))
				ref.TerminalReceiptDigest = digestBytes(raw)
			}
			wrapper = encodeTest(t, fields)
			if _, err := x.f.s.db.Exec(`DROP TRIGGER terminal_receipts_immutable; DROP TRIGGER authority_artifacts_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := x.f.s.db.Exec(`UPDATE authority_artifacts SET wrapper=?,digest=? WHERE (domain_id,epoch,generation,sequence) IN
				(SELECT domain_id,artifact_epoch,artifact_generation,artifact_sequence FROM terminal_receipts WHERE command_id=?)`, wrapper, digestBytes(wrapper), ref.ReleaseCommandID); err != nil {
				t.Fatal(err)
			}
			if _, err := x.f.s.db.Exec(`UPDATE terminal_receipts SET receipt=?,wrapper=? WHERE command_id=?`, raw, wrapper, ref.ReleaseCommandID); err != nil {
				t.Fatal(err)
			}
			command := x.command(90, ref)
			status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
			if err != nil {
				t.Fatal(err)
			}
			sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
		})
	}
}

func TestBatchSweepObservedBoundaryIntegrity(t *testing.T) {
	for _, terminal := range []string{"swept", "already-swept", "refused"} {
		for _, mutation := range []string{"missing", "request hash", "receipt digest", "prefix ID", "prefix digest", "prefix count", "outcome"} {
			t.Run(terminal+"/"+mutation, func(t *testing.T) {
				x := newSweepFixture(t, false, terminal == "already-swept", false)
				command := x.command(90, x.acquired)
				if terminal == "refused" {
					input := command.Request.Input.(operation.BatchSweepAnonymousInput)
					input.ClaimClose.ReleaseRequestHash = digestBytes([]byte("substitution"))
					command.Request.Input = input
				}
				status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key))
				if err != nil {
					t.Fatal(err)
				}
				if terminal == "refused" {
					sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
				} else {
					sweepTestResult(t, status, terminal, "")
				}
				if _, err = x.f.s.db.Exec(`UPDATE batch_sweep_boundaries SET request_hash=request_hash WHERE command_id=?`, command.ID); err == nil {
					t.Fatal("boundary row is mutable")
				}
				if _, err = x.f.s.db.Exec(`DELETE FROM batch_sweep_boundaries WHERE command_id=?`, command.ID); err == nil {
					t.Fatal("boundary row is deletable")
				}
				if _, err = x.f.s.db.Exec(`DROP TRIGGER batch_sweep_boundaries_immutable; DROP TRIGGER batch_sweep_boundaries_no_delete`); err != nil {
					t.Fatal(err)
				}
				query := `UPDATE batch_sweep_boundaries SET `
				var value any = digestBytes([]byte("corrupt"))
				switch mutation {
				case "missing":
					query = `DELETE FROM batch_sweep_boundaries WHERE command_id=?`
				case "request hash":
					query += `request_hash=? WHERE command_id=?`
				case "receipt digest":
					query += `terminal_receipt_digest=? WHERE command_id=?`
				case "prefix ID":
					query += `event_id=? WHERE command_id=?`
					value = claimTestID(999)
				case "prefix digest":
					query += `prefix_digest=? WHERE command_id=?`
				case "prefix count":
					query += `event_count=event_count+1 WHERE command_id=?`
				case "outcome":
					if terminal == "refused" {
						query += `problem_code=? WHERE command_id=?`
						value = string(operation.ProblemBatchSweepNotEligible)
					} else {
						query += `outcome=? WHERE command_id=?`
						value = "already-swept"
						if terminal == "already-swept" {
							value = "swept"
						}
					}
				}
				args := []any{value, command.ID}
				if mutation == "missing" || mutation == "prefix count" {
					args = []any{command.ID}
				}
				if _, err = x.f.s.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				// Restore exact schema so reopen tests row/effect integrity.
				for _, object := range batchSweepSchema[2:] {
					if _, err = x.f.s.db.Exec(object.sql); err != nil {
						t.Fatal(err)
					}
				}
				if err = x.f.s.Close(); err != nil {
					t.Fatal(err)
				}
				store, err := OpenExisting(x.f.root)
				if store != nil {
					_ = store.Close()
				}
				if !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("corrupt observed boundary reopened: %v", err)
				}
			})
		}
	}
}

func TestBatchSweepHistoryRejectsSignedFalseNoEventTerminal(t *testing.T) {
	for _, outcome := range []string{"already-swept", "false refusal", "post-command prefix"} {
		t.Run(outcome, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			command := x.command(90, x.acquired)
			hash := hashCommand(t, command)
			canonical, err := command.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			tx, err := x.f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			anchor, err := currentAnchor(ctx, tx, domainA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`INSERT INTO submissions VALUES(?,?,?,?,?,?,?,?,?,'submitted')`, domainA, command.ID, hash, canonical, 7, envA, x.next, "batch.sweep-anonymous", 1); err != nil {
				t.Fatal(err)
			}
			code := "result.succeeded"
			var output, problem, storedOutcome any = encodeTest(t, map[string]any{"outcome": "already-swept"}), nil, "already-swept"
			if outcome == "false refusal" {
				code, output, problem, storedOutcome = "result.refused", nil, string(operation.ProblemBatchSweepTargetMissing), nil
			}
			identity := commandIdentity{domain: domainA, id: command.ID, hash: hash, epoch: 7, environment: envA, sequence: x.next, name: "batch.sweep-anonymous", version: 1}
			_, err = x.f.s.finishCommandTx(ctx, tx, identity, x.next-1, code, output, problem, nil, nil, nil, x.f.now, signWith(x.f.key), func(receipt, _ []byte, _, _ uint64) error {
				_, err := tx.Exec(`INSERT INTO batch_sweep_boundaries VALUES(?,?,?,?,?,?,?,?,?,?)`, domainA, command.ID, hash, digestBytes(receipt), code, storedOutcome, problem, anchor.EventCount, anchor.EventID, anchor.Digest)
				return err
			})
			if err != nil {
				t.Fatalf("construct correctly signed false terminal: %v", err)
			}
			if outcome == "post-command prefix" {
				// A falsely claims already-swept at N. B genuinely sweeps at
				// N+1, then A's entire anchor is replaced with B's real prefix.
				x.next++
				later := x.command(91, x.acquired)
				status, err := x.f.s.sweepAnonymousBatch(ctx, later, hashCommand(t, later), x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key))
				if err != nil {
					t.Fatal(err)
				}
				sweepTestResult(t, status, "swept", "")
				after := x.f.anchor(t)
				if after.EventCount != anchor.EventCount+1 {
					t.Fatal("later sweep did not advance the prefix by one event")
				}
				if _, err = x.f.s.db.Exec(`DROP TRIGGER batch_sweep_boundaries_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err = x.f.s.db.Exec(`UPDATE batch_sweep_boundaries SET event_count=?,event_id=?,prefix_digest=? WHERE domain_id=? AND command_id=?`,
					after.EventCount, after.EventID, after.Digest, domainA, command.ID); err != nil {
					t.Fatal(err)
				}
				// Restore the exact trigger: reopen must reject chronology,
				// not an altered schema or an invalid prefix hash.
				if _, err = x.f.s.db.Exec(batchSweepSchema[2].sql); err != nil {
					t.Fatal(err)
				}
			}
			if err = x.f.s.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenExisting(x.f.root)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("signed but nonfactual no-event terminal reopened: %v", err)
			}
		})
	}
}

// Seed canonical event envelopes for Step 10 states which are intentionally
// not admitted by this store. These tests exercise the internal guard, not a
// new command/transport route or historical promotion of those records.
func sweepSeedBracketHistory(t *testing.T, x sweepFixture, events []step13Event) {
	t.Helper()
	var raw []byte
	var hash string
	if err := x.f.s.db.QueryRow(`SELECT command,request_hash FROM submissions WHERE command_id=?`, claimTestID(10)).Scan(&raw, &hash); err != nil {
		t.Fatal(err)
	}
	command, err := operation.DecodeCanonicalCommand(raw)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := x.f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for index, event := range events {
		identity := eventIdentity{domainA, command.ID, hash, envA, command.EnvironmentSequence, command.ActedAt, repoA}
		payload := make(map[string]any, len(event.payload))
		for key, value := range event.payload {
			payload[key] = value
		}
		if _, err = appendCommandEvent(context.Background(), tx, identity, x.f.now, claimTestID(200+index), event.kind, event.subject, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchSweepUnsupportedBracketAndAnonymousTargetGuards(t *testing.T) {
	for _, scenario := range []string{"live Run", "live Dispatch", "live role after dispatch closed", "unrelated Run", "all closed", "named Batch", "wrong anonymous Batch", "dismissed Batch"} {
		t.Run(scenario, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			run, dispatch, role, otherBatch := claimTestID(300), claimTestID(301), claimTestID(302), claimTestID(303)
			event := func(kind, subject string, payload map[string]any) step13Event {
				return step13TestEvent(1, kind, subject, payload)
			}
			started := event("run.started", run, map[string]any{"batch": x.allocation.BatchID, "locator": "run", "matters": []string{x.f.matter}})
			finished := event("run.finished", run, map[string]any{})
			opened := event("dispatch.opened", dispatch, map[string]any{"run": run, "matter": x.f.matter})
			closed := event("dispatch.closed", dispatch, map[string]any{"reason": "finished"})
			spawned := event("role.spawned", role, map[string]any{"dispatch": dispatch, "name": "worker"})
			var events []step13Event
			var problem operation.ProblemCode
			command := x.command(90, x.acquired)
			switch scenario {
			case "live Run":
				events, problem = []step13Event{started}, operation.ProblemBatchSweepUnsupported
			case "live Dispatch":
				events, problem = []step13Event{started, finished, opened}, operation.ProblemBatchSweepUnsupported
			case "live role after dispatch closed":
				events, problem = []step13Event{started, finished, opened, closed, spawned}, operation.ProblemBatchSweepUnsupported
			case "unrelated Run":
				events = []step13Event{event("run.started", run, map[string]any{"batch": otherBatch, "locator": "other", "matters": []string{claimTestID(999)}})}
			case "all closed":
				events = []step13Event{started, opened, spawned, closed, finished, event("role.closed", role, map[string]any{"reason": "finished"})}
			case "named Batch", "wrong anonymous Batch":
				input := command.Request.Input.(operation.BatchSweepAnonymousInput)
				input.BatchID = otherBatch
				command.Request.Input = input
				kind, payload := "batch.created", map[string]any{"name": "named"}
				if scenario == "wrong anonymous Batch" {
					kind, payload = "batch.anonymous-created", map[string]any{"batch_id": otherBatch, "matter_id": claimTestID(999)}
				}
				events, problem = []step13Event{event(kind, otherBatch, payload)}, operation.ProblemBatchSweepNotEligible
			case "dismissed Batch":
				events, problem = []step13Event{event("batch.dismissed", x.allocation.BatchID, map[string]any{})}, operation.ProblemBatchSweepNotEligible
			}
			sweepSeedBracketHistory(t, x, events)
			status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, claimTestID(400), signWith(x.f.key))
			if err != nil {
				t.Fatal(err)
			}
			sweepTestResult(t, status, "swept", problem)
		})
	}
}

func TestBatchSweepNormalReleaseWithoutDone(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	x := sweepFixture{f: f, allocation: allocation, next: 3}
	command := step12Command(f, 12, x.next, operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: f.matter}, allocation.ClaimID)
	started := completeLifecycleCommand(t, f, command, []string{claimTestID(105)})
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)
	x.next++
	releaseRepairFixtureClaim(t, repairTerminalFixture{f: f, claim: allocation}, x.next, 15, 110, 111, f.now)
	var hash string
	var released CommandStatus
	if err := f.s.db.QueryRow(`SELECT s.request_hash,r.receipt,r.wrapper FROM submissions s JOIN terminal_receipts r USING(domain_id,command_id) WHERE s.command_id=?`, claimTestID(15)).Scan(&hash, &released.Receipt, &released.SignedReceipt); err != nil {
		t.Fatal(err)
	}
	x.acquired = sweepReference(t, f, claimTestID(15), hash, allocation.ClaimID, released)
	x.next++
	x.closeBirth(t)
	command = x.command(90, x.acquired)
	status, err := f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), f.peer, f.now, "", signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	sweepTestResult(t, status, "", operation.ProblemBatchSweepNotEligible)
	if err = checkSchema(f.s.db); err != nil {
		t.Fatal(err)
	}
}
