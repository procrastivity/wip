package authoritystore

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func TestS9BReviewRejectsSuccessfulNoopJoinForUnjoinedPair(t *testing.T) {
	fixture := newSweepFixture(t, false, false, false)
	store, now := fixture.f.s, fixture.f.now
	var sequence uint64
	if err := store.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	sequence++
	batchID := claimTestID(1601)
	batchCommand := namedBatchCommand(claimTestID(1600), domainA, envA, repoA, sequence, "review duplicate join")
	completeNamedBatchForTest(t, store, batchCommand, fixture.f.peer, fixture.f.key, now, batchID, claimTestID(1602))
	sequence++
	secondMatterID := claimTestID(1604)
	secondMatterCommand := matterCommand(claimTestID(1603), sequence, "review-unjoined-matter")
	completeNamedBatchMatterForTest(t, store, secondMatterCommand, fixture.f.peer, fixture.f.key, now, 1604, 1605)
	sequence++
	joinCommand := func(id int, matterID string) operation.Command {
		return namedBatchMembershipCommand(claimTestID(id), domainA, envA, repoA, sequence,
			operation.BatchJoinV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID})
	}
	first := joinCommand(1606, fixture.f.matter)
	sequence++
	completeNamedBatchMembershipForTest(t, store, first, fixture.f.peer, fixture.f.key, now, 1706)
	duplicate := joinCommand(1607, fixture.f.matter)
	sequence++
	completeNamedBatchMembershipForTest(t, store, duplicate, fixture.f.peer, fixture.f.key, now, 1707)

	// Replace only the final no-event receipt with a correctly signed receipt
	// for a valid Matter that never joined this Batch. Events and projections stay
	// unchanged, so validation must bind the receipt to its historical pair.
	mutated := duplicate
	input := mutated.Request.Input.(operation.BatchMembershipInput)
	input.MatterID = secondMatterID
	mutated.Request.Input = input
	commandBytes, err := mutated.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := mutated.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	var receiptBytes, wrapper []byte
	var epoch, generation, artifactSequence uint64
	if err = store.db.QueryRow(`SELECT receipt,wrapper,artifact_epoch,artifact_generation,artifact_sequence FROM terminal_receipts WHERE domain_id=? AND command_id=?`,
		domainA, duplicate.ID).Scan(&receiptBytes, &wrapper, &epoch, &generation, &artifactSequence); err != nil {
		t.Fatal(err)
	}
	var latestSequence uint64
	if err = store.db.QueryRow(`SELECT max(sequence) FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=?`, domainA, epoch, generation).Scan(&latestSequence); err != nil {
		t.Fatal(err)
	}
	if artifactSequence != latestSequence {
		t.Fatalf("duplicate join receipt is not the final artifact: receipt=%d latest=%d", artifactSequence, latestSequence)
	}

	var receiptFields map[string]cbor.RawMessage
	if err = canonicalDecode(receiptBytes, &receiptFields); err != nil {
		t.Fatal(err)
	}
	receiptFields["request_hash"] = encodeTest(t, requestHash)
	var resultFields map[string]cbor.RawMessage
	if err = canonicalDecode(receiptFields["result"], &resultFields); err != nil {
		t.Fatal(err)
	}
	output, err := namedBatchReceiptOutput(operation.BatchMembershipOutput{BatchID: batchID, MatterID: secondMatterID})
	if err != nil {
		t.Fatal(err)
	}
	resultFields["output"] = encodeTest(t, output)
	receiptFields["result"] = encodeTest(t, resultFields)
	newReceipt := encodeTest(t, receiptFields)

	var wrapperFields map[string]cbor.RawMessage
	if err = canonicalDecode(wrapper, &wrapperFields); err != nil {
		t.Fatal(err)
	}
	wrapperFields["payload"] = encodeTest(t, newReceipt)
	wrapperFields["payload_digest"] = encodeTest(t, digestBytes(newReceipt))
	delete(wrapperFields, "signature")
	unsigned := encodeTest(t, wrapperFields)
	signature := ed25519.Sign(fixture.f.key, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...))
	wrapperFields["signature"] = encodeTest(t, signature)
	newWrapper := encodeTest(t, wrapperFields)
	artifactDigest := digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), newWrapper...))

	for _, trigger := range []string{"submissions_immutable", "terminal_receipts_immutable", "authority_artifacts_immutable"} {
		if _, err = store.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.db.Exec(`UPDATE submissions SET request_hash=?,command=? WHERE domain_id=? AND command_id=?`,
		requestHash, commandBytes, domainA, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE authority_artifacts SET digest=?,wrapper=? WHERE domain_id=? AND epoch=? AND generation=? AND sequence=?`,
		artifactDigest, newWrapper, domainA, epoch, generation, artifactSequence); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE terminal_receipts SET receipt=?,wrapper=? WHERE domain_id=? AND command_id=?`,
		newReceipt, newWrapper, domainA, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	for _, object := range step4Schema {
		if object.name == "submissions_immutable" || object.name == "terminal_receipts_immutable" {
			if _, err = store.db.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, object := range step3Schema {
		if object.name == "authority_artifacts_immutable" {
			if _, err = store.db.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = checkNamedBatchMembershipState(store.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("membership validation accepted re-signed unjoined-pair receipt: %v", err)
	}
	if err = checkSchema(store.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("schema validation accepted re-signed unjoined-pair receipt: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := OpenExisting(fixture.f.root); !errors.Is(openErr, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("OpenExisting accepted signed no-event join receipt for an unjoined pair: %v", openErr)
	}
}
