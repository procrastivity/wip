package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for step-04: prose and content storage.
//
// The claims under test are the Brief's content passage and the `(D46-dependent)`
// role D61 gave it — one content table, create-once versus append handled by the
// substrate rather than by a verb remembering, spill above a documented threshold
// that readers never have to know about, and the whole table a projection of the
// log:
//
//   - a Brief, a Workplan and a body are one row each and read back byte-identical;
//     findings accumulate one segment per append and read back concatenated in
//     identity order, which is write order;
//   - the event type is a consequence of the kind and not the caller's choice, from
//     both sides: ContentDraft picks it, and a payload that names a kind its event
//     type does not carry is refused;
//   - a second create-once segment is refused by `content_create_once` and by
//     nothing else;
//   - bytes at exactly SpillThreshold stay in the database and bytes above it go to
//     a sidecar file named for the content's ULID under the store's blob directory,
//     with the row keeping the reference — and Store.Content resolves both the same
//     way;
//   - a missing, truncated or corrupted sidecar file is *reported*, never silently
//     short, because spilled bytes are the one projection fact the log does not
//     carry (docs/schema/decisions.md);
//   - a rebuild reproduces the content table column for column, spilled row
//     included, without needing the sidecar file to exist;
//   - nothing reads prose back in from a file (D36, D40) — a claim about the absence
//     of a code path, checked structurally at the end of this file.

// ---------------------------------------------------------------------------
// Test-local helpers
// ---------------------------------------------------------------------------

// generated returns n deterministic bytes with a per-payload tag.
//
// Generated rather than embedded because the spill cases are a mebibyte each: a
// loop costs the suite nothing and a fixture file would cost it a mebibyte in the
// repository. The pattern varies per byte so a truncation or a flipped byte is
// something the digest and the length checks can actually catch.
func generated(n int, tag byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = tag ^ byte(i*7+11)
	}
	return out
}

// digestOf is the hex SHA-256 the store records for some bytes.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// segments reads a node's live content of one kind, failing the test rather than
// returning an error.
func (h *harness) segments(node string, kind ContentKind) []ContentSegment {
	h.t.Helper()
	segs, err := h.ContentSegments(h.ctx, node, kind)
	if err != nil {
		h.t.Fatalf("read the %s segments of %s: %v", kind, node, err)
	}
	return segs
}

// contentOf decodes a content event's payload, so a test can assert that the row's
// identity is the one the *payload* minted and not the event's.
func contentOf(t *testing.T, ev Event) ContentWritten {
	t.Helper()
	var p ContentWritten
	if err := decode(ev, &p); err != nil {
		t.Fatalf("decode the payload of %s: %v", ev.ID, err)
	}
	return p
}

// blobPathOf is where a spilled segment's bytes live, computed from paths.go's own
// rule rather than from the Store's cached field — so the assertion is about the
// documented layout and not about the store agreeing with itself.
func (h *harness) blobPathOf(id string) string {
	h.t.Helper()
	return filepath.Join(blobDirFor(h.Path()), id)
}

// rawContentInsert puts a content row straight into the table, around every guard
// this package has, expecting the substrate to refuse it. The storage-form CHECK
// is the only thing standing between a row and not knowing where its bytes are,
// and it cannot be reached through a write path built to make it unreachable.
func (h *harness) rawContentInsert(what, want, node string, bytesArg, blobRef any) {
	h.t.Helper()
	ev := h.commit(renderDraft(node))[0]
	rawRefusedBy(h, what, want,
		`INSERT INTO content (id, node, kind, bytes, blob_ref, byte_len, sha256, birth_event, last_event)
		 VALUES (?, ?, 'findings', ?, ?, 0, ?, ?, ?)`,
		h.NewID(), node, bytesArg, blobRef, digestOf(nil), ev.ID, ev.ID)
}

// ---------------------------------------------------------------------------
// 1. Create-once versus append
// ---------------------------------------------------------------------------

// TestACreateOnceKindIsOneRowAndFindingsAccumulate is the Brief's content passage
// read literally: "Create-once kinds (Brief, Workplan, body) are one row; append
// kinds (findings) accumulate across `content.appended`."
//
// It also pins the half of that sentence which is about the *event*: which type a
// content write is follows from the kind, so a caller cannot append a Brief or
// create a findings entry. ContentDraft is one side of that and insertContent's
// kind/type guard (below) is the other; together they make the combination
// unexpressible rather than merely unusual.
func TestACreateOnceKindIsOneRowAndFindingsAccumulate(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("content", "A Matter with prose")
	step := h.step(matter, "step-01", "A Step with a body")

	brief := []byte("# The Brief\n\nWhy this Matter exists.\n")
	workplan := []byte("# The Workplan\n\nstep-01 and nothing else.\n")
	body := []byte("What this Step is, in prose.\n")

	briefEvent := h.write(matter, KindBrief, brief)
	workplanEvent := h.write(matter, KindWorkplan, workplan)
	bodyEvent := h.write(step, KindBody, body)

	for _, c := range []struct {
		node  string
		kind  ContentKind
		data  []byte
		event Event
	}{
		{matter, KindBrief, brief, briefEvent},
		{matter, KindWorkplan, workplan, workplanEvent},
		{step, KindBody, body, bodyEvent},
	} {
		if c.event.Type != TypeContentCreated {
			t.Errorf("a %s was written as %s, want %s", c.kind, c.event.Type, TypeContentCreated)
		}
		if c.event.Subject != c.node {
			t.Errorf("a %s names subject %s, want the owning node %s", c.kind, c.event.Subject, c.node)
		}
		segs := h.segments(c.node, c.kind)
		if len(segs) != 1 {
			t.Fatalf("%s content is %d segments, want exactly 1: a create-once kind is one row", c.kind, len(segs))
		}
		// The row's identity is the payload's, not the event's: an append kind
		// orders its segments by it, so it has to be the content's own.
		if got := contentOf(t, c.event).Content; segs[0].ID != got {
			t.Errorf("the %s row is %s and its event's payload minted %s", c.kind, segs[0].ID, got)
		}
		if segs[0].Spilled {
			t.Errorf("%s content of %d bytes spilled; SpillThreshold is %d", c.kind, len(c.data), SpillThreshold)
		}
		if segs[0].ByteLen != int64(len(c.data)) || segs[0].SHA256 != digestOf(c.data) {
			t.Errorf("the %s segment records %d bytes / %s, want %d / %s",
				c.kind, segs[0].ByteLen, segs[0].SHA256, len(c.data), digestOf(c.data))
		}
		h.wantContent("a create-once write", c.node, c.kind, c.data)
	}

	// One create-once row, column for column. Nothing else in this file asserts
	// every column of a content row, and the shape of the row is half the Step:
	// ULID, owning node ULID, kind, bytes.
	brief0 := h.segments(matter, KindBrief)[0]
	h.wantRow("the Brief's row", "content", "id = ?", []any{brief0.ID}, map[string]any{
		"id":       brief0.ID,
		"node":     matter,
		"kind":     string(KindBrief),
		"bytes":    brief,
		"blob_ref": nil,
		"byte_len": int64(len(brief)),
		"sha256":   digestOf(brief),
		// Born by one event and never moved: content is written, never rewritten,
		// so birth_event and last_event agree for the row's whole life.
		"birth_event":     briefEvent.ID,
		"last_event":      briefEvent.ID,
		"tombstone_event": nil,
	})

	// Zero-length content is a content object like any other, and both spellings of
	// it are the same one. The schema permits it (`byte_len >= 0`) and a reader
	// cannot tell an empty Brief from a missing one through Content, so the row is
	// the only place the difference is recorded — which means the payload has to be
	// able to say "these are the bytes, and there are none" as distinct from "the
	// bytes are elsewhere," the form a spilled payload uses.
	for _, c := range []struct {
		what string
		data []byte
	}{{"an empty slice", []byte{}}, {"a nil slice", nil}} {
		blank := h.step(matter, "step-blank-"+c.what, "A Step with "+c.what+" for a body")
		h.write(blank, KindBody, c.data)
		h.wantContent("a zero-length body written as "+c.what, blank, KindBody, nil)
		h.wantRowCount("a zero-length body written as "+c.what, "content",
			"node = ? AND kind = 'body' AND bytes IS NOT NULL AND byte_len = 0", []any{blank}, 1)
	}

	// --- findings accumulate -------------------------------------------------
	notes := [][]byte{
		[]byte("The first finding.\n"),
		[]byte("A second, appended later.\n"),
		[]byte("A third, later still.\n"),
	}
	var appended []Event
	for _, note := range notes {
		appended = append(appended, h.write(step, KindFindings, note))
	}
	for i, ev := range appended {
		if ev.Type != TypeContentAppended {
			t.Errorf("finding %d was written as %s, want %s", i, ev.Type, TypeContentAppended)
		}
	}
	segs := h.segments(step, KindFindings)
	if len(segs) != len(notes) {
		t.Fatalf("three appends produced %d segments, want %d: an append is an insert", len(segs), len(notes))
	}
	for i, seg := range segs {
		if want := contentOf(t, appended[i]).Content; seg.ID != want {
			t.Errorf("segment %d is %s, want %s: identity order is write order", i, seg.ID, want)
		}
		if i > 0 && segs[i-1].ID >= seg.ID {
			t.Errorf("segment %d (%s) does not sort after segment %d (%s)", i, seg.ID, i-1, segs[i-1].ID)
		}
	}
	h.wantContent("three appended findings", step, KindFindings, bytes.Join(notes, nil))

	// --- the type is a consequence of the kind -------------------------------
	//
	// Asked of all four kinds in one command and then rolled back, so the mapping is
	// asserted without a node having to accommodate four kinds at once — and so the
	// assertion is about what ContentDraft returns rather than about what happened to
	// land.
	rolledBack := errors.New("rolled back on purpose")
	_, err := h.Commit(h.ctx, h.req(), func(_ context.Context, tx *Tx) ([]Draft, error) {
		for _, c := range []struct {
			kind ContentKind
			want string
		}{
			{KindBrief, TypeContentCreated},
			{KindWorkplan, TypeContentCreated},
			{KindBody, TypeContentCreated},
			{KindFindings, TypeContentAppended},
		} {
			draft, err := tx.ContentDraft(matter, c.kind, []byte("some prose"))
			if err != nil {
				return nil, err
			}
			if draft.Type != c.want {
				t.Errorf("ContentDraft made a %s write a %s, want %s", c.kind, draft.Type, c.want)
			}
			if c.kind.AppendOnlyKind() != (draft.Type == TypeContentAppended) {
				t.Errorf("%s is append-only=%v and drafted %s", c.kind, c.kind.AppendOnlyKind(), draft.Type)
			}
		}
		return nil, rolledBack
	})
	if !errors.Is(err, rolledBack) {
		t.Fatalf("the rolled-back probe returned %v, want it to have rolled back", err)
	}
}

// ---------------------------------------------------------------------------
// 2. A second create-once segment
// ---------------------------------------------------------------------------

// TestASecondCreateOnceSegmentIsRefusedByTheCreateOnceIndex holds "create-once" to
// being enforced by the substrate.
//
// docs/schema/decisions.md puts the enforcement in a partial unique index rather
// than in a verb that remembers, so the refusal has to name that index: a test
// that only checked *some* error came back would pass just as happily if the verb
// had grown a lookup, or if an unrelated guard fired first.
func TestASecondCreateOnceSegmentIsRefusedByTheCreateOnceIndex(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("once", "A Matter written once")
	other := h.matter("elsewhere", "A Matter with its own prose")

	for _, kind := range []ContentKind{KindBrief, KindWorkplan, KindBody} {
		first := []byte("the first and only " + string(kind) + "\n")
		h.write(matter, kind, first)

		err := h.writeError(matter, kind, []byte("a rewrite of the "+string(kind)+"\n"))
		refusalMentions(t, "a second "+string(kind), err, "content_create_once")
		h.wantRowCount("a refused second "+string(kind), "content",
			"node = ? AND kind = ?", []any{matter, string(kind)}, 1)
		h.wantContent("after a refused rewrite", matter, kind, first)

		// The index keys on (node, kind), so the same kind on another node is a
		// different pair and not a rewrite of anything.
		elsewhere := []byte("another node's " + string(kind) + "\n")
		h.write(other, kind, elsewhere)
		h.wantContent("the same kind on another node", other, kind, elsewhere)
	}

	// findings are outside the index's partial predicate entirely, which is what
	// makes one index serve both halves of the rule.
	h.write(matter, KindFindings, []byte("one finding\n"))
	h.write(matter, KindFindings, []byte("and another\n"))
	h.wantRowCount("findings are not create-once", "content",
		"node = ? AND kind = 'findings'", []any{matter}, 2)

	// The predicate itself, read out of the shipped index rather than assumed: it is
	// the two halves of the rule in one place — which kinds are create-once, and
	// that a tombstoned segment no longer occupies the slot.
	var indexSQL string
	if err := h.db.QueryRowContext(h.ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'content_create_once'`).
		Scan(&indexSQL); err != nil {
		t.Fatalf("read the create-once index: %v", err)
	}
	for _, want := range []string{"'brief','workplan','body'", "tombstone_event IS NULL"} {
		if !strings.Contains(indexSQL, want) {
			t.Errorf("the create-once index is %q, which does not mention %q", indexSQL, want)
		}
	}

	// So a tombstoned create-once segment would make room for a new one. Whether
	// that path is *reachable* is a separate question, and the answer in P1 is no:
	// applyEvent has no rule that writes content.tombstone_event — no P1 event
	// removes content — so nothing a command can do reaches it. The demonstration
	// below therefore has to go around the API, and that is the finding: the
	// predicate is correct and currently unexercised by any verb.
	h.wantRowCount("no P1 event tombstones content", "content", "tombstone_event IS NOT NULL", nil, 0)

	newer := h.commit(renderDraft(matter))[0]
	brief := h.segments(matter, KindBrief)[0]
	if err := h.rawExec(`UPDATE content SET tombstone_event = ?, last_event = ? WHERE id = ?`,
		newer.ID, newer.ID, brief.ID); err != nil {
		t.Fatalf("tombstone a content row around the API: %v", err)
	}
	replacement := []byte("a Brief written after the first was tombstoned\n")
	h.write(matter, KindBrief, replacement)
	h.wantRowCount("a tombstoned Brief and its successor", "content",
		"node = ? AND kind = 'brief'", []any{matter}, 2)
	h.wantContent("after the first Brief was tombstoned", matter, KindBrief, replacement)
}

// ---------------------------------------------------------------------------
// 3. Findings accumulate; nothing leaks
// ---------------------------------------------------------------------------

// TestFindingsAccumulateAndNoContentLeaksAcrossKindsOrNodes covers the boring
// half, which is the half a bug hides in: a read is scoped to one node and one
// kind, ordered by identity, and empty when there is nothing — because absence of
// prose is a fact about the node and not a failure of the reader.
func TestFindingsAccumulateAndNoContentLeaksAcrossKindsOrNodes(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("findings", "A Matter that accumulates findings")
	first := h.step(matter, "step-01", "The Step that finds things")
	second := h.step(matter, "step-02", "A Step that finds nothing")

	// Nothing written yet: every kind reads empty, and none of them is an error.
	for _, kind := range []ContentKind{KindBrief, KindWorkplan, KindBody, KindFindings} {
		got, err := h.Content(h.ctx, first, kind)
		if err != nil {
			t.Errorf("reading the %s of a node that has none failed: %v", kind, err)
		}
		if len(got) != 0 {
			t.Errorf("the %s of a node that has none is %d bytes, want none", kind, len(got))
		}
		if segs := h.segments(first, kind); len(segs) != 0 {
			t.Errorf("a node with no %s has %d segments of it", kind, len(segs))
		}
	}

	notes := [][]byte{
		[]byte("Found: the harness had no content helper.\n"),
		[]byte("Found: the threshold is exclusive.\n"),
		[]byte("Found: a missing sidecar is reported.\n"),
	}
	for _, note := range notes {
		h.write(first, KindFindings, note)
	}
	whole := bytes.Join(notes, nil)
	h.wantContent("three findings", first, KindFindings, whole)

	// A body on the same node does not join the findings, and vice versa: the read
	// is scoped to one kind even though both live in one table.
	body := []byte("What this Step is, which is not a finding.\n")
	h.write(first, KindBody, body)
	h.wantContent("findings after a body was written", first, KindFindings, whole)
	h.wantContent("the body beside three findings", first, KindBody, body)

	// Another node's findings do not join this one's.
	elsewhere := []byte("A finding belonging to the other Step.\n")
	h.write(second, KindFindings, elsewhere)
	h.wantContent("the other Step's findings", second, KindFindings, elsewhere)
	h.wantContent("findings after another node appended its own", first, KindFindings, whole)

	// And the order is the table's, not the caller's: the segments are ordered by
	// identity, so a read after an interleaved write to another node still returns
	// this node's three in write order.
	if segs := h.segments(first, KindFindings); len(segs) != 3 {
		t.Fatalf("this Step's findings are %d segments, want 3", len(segs))
	}
}

// ---------------------------------------------------------------------------
// 4. Spill
// ---------------------------------------------------------------------------

// TestContentAtTheThresholdStaysInStoreAndAboveItSpills is PLAN 1.2's one named
// accommodation: above a documented threshold a content row keeps a reference and
// its bytes live in a store-adjacent file.
//
// The boundary is asserted from both sides because "above" is the documented word
// and off-by-one there would silently move every 1 MiB Brief out of the database.
func TestContentAtTheThresholdStaysInStoreAndAboveItSpills(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("spill", "A Matter with an ingested log")
	edge := h.step(matter, "step-01", "A Step whose body is exactly at the threshold")
	ingested := h.step(matter, "step-02", "A Step with a CI log attached")

	atThreshold := generated(SpillThreshold, 0x5a)
	overThreshold := generated(SpillThreshold+1, 0xa5)

	h.write(edge, KindBody, atThreshold)
	h.write(ingested, KindFindings, overThreshold)

	inStore := h.segments(edge, KindBody)[0]
	spilled := h.segments(ingested, KindFindings)[0]

	// --- exactly at the threshold: in the database ---------------------------
	if inStore.Spilled {
		t.Errorf("content of exactly SpillThreshold (%d) bytes spilled; the threshold is exclusive", SpillThreshold)
	}
	if row := h.rowOf("content", "id = ?", inStore.ID); row["blob_ref"] != "NULL" || row["bytes"] == "NULL" {
		t.Errorf("the at-threshold row has blob_ref %s and %s bytes, want a null reference and bytes in the row",
			row["blob_ref"], map[bool]string{true: "null", false: "non-null"}[row["bytes"] == "NULL"])
	}
	h.wantContent("content of exactly SpillThreshold bytes", edge, KindBody, atThreshold)

	// --- one byte over: a sidecar file --------------------------------------
	if !spilled.Spilled {
		t.Errorf("content of SpillThreshold+1 (%d) bytes did not spill", SpillThreshold+1)
	}
	if row := h.rowOf("content", "id = ?", spilled.ID); row["bytes"] != "NULL" || row["blob_ref"] != "'"+spilled.ID+"'" {
		t.Errorf("the spilled row holds bytes=%s blob_ref=%s, want NULL bytes and a reference of %q",
			map[bool]string{true: "NULL", false: "<bytes>"}[row["bytes"] == "NULL"], row["blob_ref"], spilled.ID)
	}

	// The path is the one paths.go documents — $XDG_DATA_HOME/wip/<host>/blobs for a
	// real store, a sibling of the database for this one — named for the content's
	// own ULID, which is what makes the reference need no separate bookkeeping.
	if h.blobDir != blobDirFor(h.Path()) {
		t.Errorf("the store spills to %s, and blobDirFor says %s", h.blobDir, blobDirFor(h.Path()))
	}
	sidecar := h.blobPathOf(spilled.ID)
	onDisk, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("read the sidecar file the store said it wrote: %v", err)
	}
	if !bytes.Equal(onDisk, overThreshold) {
		t.Errorf("the sidecar file holds %d bytes, want the %d that were written", len(onDisk), len(overThreshold))
	}

	// Exactly one file: the at-threshold write must not have left one behind.
	entries, err := os.ReadDir(h.blobDir)
	if err != nil {
		t.Fatalf("read the blob directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != spilled.ID {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("the blob directory holds %v, want only %s", names, spilled.ID)
	}

	// --- spill is transparent to readers ------------------------------------
	h.wantContent("spilled content, through Store.Content", ingested, KindFindings, overThreshold)
	got, err := h.SegmentBytes(h.ctx, spilled.ID)
	if err != nil {
		t.Fatalf("resolve a spilled segment: %v", err)
	}
	if !bytes.Equal(got, overThreshold) {
		t.Errorf("SegmentBytes returned %d bytes for a spilled segment, want %d", len(got), len(overThreshold))
	}

	// A spilled segment concatenates with an in-store one exactly as two in-store
	// ones do: which storage form a segment used is not a fact a reader can see.
	tail := []byte("a small finding appended after the log\n")
	h.write(ingested, KindFindings, tail)
	h.wantContent("a spilled segment followed by an in-store one",
		ingested, KindFindings, append(append([]byte{}, overThreshold...), tail...))

	// --- the storage-form CHECK ---------------------------------------------
	//
	// A row always knows where its bytes are. Both forms at once and neither are the
	// two ways that could stop being true, and the CHECK is what says so — reachable
	// only around the API, because insertContent refuses the same payloads first.
	const wantCheck = "(bytes IS NULL) <> (blob_ref IS NULL)"
	h.rawContentInsert("a row carrying both bytes and a reference", wantCheck, matter, []byte("x"), "some-ref")
	h.rawContentInsert("a row carrying neither", wantCheck, matter, nil, nil)
}

// ---------------------------------------------------------------------------
// 5. A missing or corrupt blob
// ---------------------------------------------------------------------------

// TestAMissingOrCorruptBlobIsReportedNeverSilentlyShort is the assertion that
// makes spill honest.
//
// Spilled bytes are the one projection fact not recoverable from the log alone
// (docs/schema/decisions.md), so the payload carries a length and a digest and a
// read verifies both. The failure this rules out is the quiet one: a reader handed
// a truncated Brief, or an empty one, and told nothing.
func TestAMissingOrCorruptBlobIsReportedNeverSilentlyShort(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("corrupt", "A Matter whose sidecar file goes wrong")
	step := h.step(matter, "step-01", "A Step with an ingested log")

	log := generated(SpillThreshold+1, 0x3c)
	h.write(step, KindFindings, log)
	seg := h.segments(step, KindFindings)[0]
	sidecar := h.blobPathOf(seg.ID)

	// --- truncated: the length check ----------------------------------------
	if err := os.WriteFile(sidecar, log[:1024], 0o600); err != nil {
		t.Fatalf("truncate the sidecar file: %v", err)
	}
	_, err := h.Content(h.ctx, step, KindFindings)
	refusalMentions(t, "a truncated sidecar file", err, "is 1024 bytes; the store recorded")
	refusalMentions(t, "a truncated sidecar file", err, seg.ID)

	// --- rewritten with different bytes of the same length: the digest -------
	//
	// The length check cannot see this one, which is exactly why the digest is
	// recorded as well as the length.
	flipped := append([]byte{}, log...)
	flipped[len(flipped)/2] ^= 0xff
	if err := os.WriteFile(sidecar, flipped, 0o600); err != nil {
		t.Fatalf("corrupt the sidecar file: %v", err)
	}
	_, err = h.Content(h.ctx, step, KindFindings)
	refusalMentions(t, "a corrupted sidecar file of the right length", err, "hashes to")
	refusalMentions(t, "a corrupted sidecar file of the right length", err, seg.SHA256)

	// --- missing: named, with its path --------------------------------------
	if err := os.Remove(sidecar); err != nil {
		t.Fatalf("remove the sidecar file: %v", err)
	}
	_, err = h.Content(h.ctx, step, KindFindings)
	refusalMentions(t, "a missing sidecar file", err, seg.ID)
	refusalMentions(t, "a missing sidecar file", err, sidecar)
	// And the read fails rather than returning what it could: a short answer is the
	// one outcome the length and digest exist to prevent.
	got, err := h.Content(h.ctx, step, KindFindings)
	if err == nil {
		t.Errorf("a read with no sidecar file returned %d bytes and no error", len(got))
	}
	if got != nil {
		t.Errorf("a failed read returned %d bytes as well as an error", len(got))
	}

	// --- an in-store row tampered with around the API ------------------------
	//
	// The bytes of an in-store row cannot go *missing*, but they can be edited by
	// anything that can write the database, and the same two checks catch it. The
	// projection guards make this a two-column update: a row may only ever advance
	// to a newer event, so the tampering has to name one.
	body := []byte("The body as it was written.\n")
	h.write(step, KindBody, body)
	inStore := h.segments(step, KindBody)[0]
	newer := h.commit(renderDraft(matter))[0]

	if err := h.rawExec(`UPDATE content SET bytes = ?, last_event = ? WHERE id = ?`,
		body[:8], newer.ID, inStore.ID); err != nil {
		t.Fatalf("truncate an in-store row: %v", err)
	}
	_, err = h.Content(h.ctx, step, KindBody)
	refusalMentions(t, "an in-store row whose bytes were truncated", err, "is 8 bytes; the store recorded")

	later := h.commit(renderDraft(matter))[0]
	sameLength := append([]byte{}, body...)
	sameLength[0] ^= 0xff
	if err := h.rawExec(`UPDATE content SET bytes = ?, last_event = ? WHERE id = ?`,
		sameLength, later.ID, inStore.ID); err != nil {
		t.Fatalf("corrupt an in-store row: %v", err)
	}
	_, err = h.Content(h.ctx, step, KindBody)
	refusalMentions(t, "an in-store row whose bytes were edited", err, "hashes to")
}

// ---------------------------------------------------------------------------
// 6. The (D46-dependent) role: content is a projection
// ---------------------------------------------------------------------------

// TestRebuildReproducesTheContentTableIncludingASpilledRow is step-04's half of
// the `(D46-dependent)` question. D46 closed in favour of event-sourcing (D61), so
// the content table is a projection and the bytes are carried by the payload —
// blob-ref for the spilled case.
//
// What this proves, and what it deliberately does not:
//
//   - it proves the *row* is derivable from the log, spilled row included: every
//     column, including the reference, the length and the digest, comes back from
//     the fold alone, and the fold does not need the sidecar file to exist;
//   - it does not prove the spilled *bytes* are derivable, because they are not.
//     They never entered the log. That is the single exception
//     docs/schema/decisions.md names, and it is why the row carries a length and a
//     digest at all: a rebuild can restore the store's promise about those bytes,
//     and if the file behind the promise is gone the reader is told (asserted in
//     TestAMissingOrCorruptBlobIsReportedNeverSilentlyShort, and again at the end
//     here).
func TestRebuildReproducesTheContentTableIncludingASpilledRow(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("rebuilt", "A Matter whose content is rebuilt")
	step := h.step(matter, "step-01", "A Step with prose and an ingested log")

	h.write(matter, KindBrief, []byte("# The Brief\n\nWhy this Matter exists.\n"))
	h.write(matter, KindWorkplan, []byte("# The Workplan\n\nstep-01.\n"))
	h.write(step, KindBody, []byte("What this Step is.\n"))
	h.write(step, KindFindings, []byte("The first finding.\n"))
	h.write(step, KindFindings, []byte("A second finding.\n"))
	// Zero-length content, which is a content object like any other: the schema
	// permits it (`byte_len >= 0`) and the payload has to be able to say "these are
	// the bytes, and there are none" rather than "the bytes are elsewhere".
	empty := h.step(matter, "step-02", "A Step whose body is empty")
	h.write(empty, KindBody, nil)
	spilledBytes := generated(SpillThreshold+1, 0x11)
	h.write(step, KindFindings, spilledBytes)

	before := h.snapshotProjection()
	rows := before["content"]
	if len(rows) != 7 {
		t.Fatalf("the content table holds %d rows, want 7; the rebuild assertion is only worth its fixture", len(rows))
	}
	spilledRows := 0
	for _, row := range rows {
		if row["blob_ref"] != "NULL" {
			spilledRows++
		}
	}
	if spilledRows != 1 {
		t.Fatalf("the fixture has %d spilled rows, want exactly 1", spilledRows)
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	h.wantSameProjection("after a rebuild", before, h.snapshotProjection())
	h.wantContent("a rebuilt spilled row", step, KindFindings,
		[]byte("The first finding.\nA second finding.\n"+string(spilledBytes)))
	h.wantContent("a rebuilt zero-length body", empty, KindBody, nil)

	// The row is derivable and the bytes are not, so a rebuild must not depend on
	// the sidecar file: recovering the projection is exactly the situation in which
	// the store's other files may be missing.
	seg := h.segments(step, KindFindings)[2]
	if err := os.Remove(h.blobPathOf(seg.ID)); err != nil {
		t.Fatalf("remove the sidecar file: %v", err)
	}
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild with no sidecar file: %v", err)
	}
	h.wantSameProjection("after rebuilding with no sidecar file", before, h.snapshotProjection())

	// And the bytes are gone, reported rather than silently short — which is the
	// honest boundary of what a rebuild restored.
	_, err := h.Content(h.ctx, step, KindFindings)
	refusalMentions(t, "reading a rebuilt row whose sidecar file is gone", err, seg.ID)
}

// ---------------------------------------------------------------------------
// 7. insertContent's own guards
// ---------------------------------------------------------------------------

// TestInsertContentRefusesAPayloadItCouldNotHaveProduced covers the guards on the
// projection rule rather than on the drafting helper.
//
// Every payload below is hand-built, because ContentDraft cannot express any of
// them — which is the point. insertContent runs on the rebuild path too, over
// whatever the log happens to hold, so its guards are what stands between a
// malformed payload and a content row the store cannot read back.
func TestInsertContentRefusesAPayloadItCouldNotHaveProduced(t *testing.T) {
	h := newHarness(t)
	node := h.matter("guards", "A Matter whose payloads are hand-built")
	prose := []byte("some prose")

	for _, c := range []struct {
		what      string
		eventType string
		want      string
		payload   ContentWritten
	}{
		{
			what: "a payload naming content that is not an identity", eventType: TypeContentCreated,
			want: `names content "brief-1", which is not an identity`,
			payload: ContentWritten{
				Kind: KindBrief, Content: "brief-1", Bytes: prose,
				ByteLen: int64(len(prose)), SHA256: digestOf(prose),
			},
		},
		{
			what: "a payload carrying both bytes and a blob reference", eventType: TypeContentCreated,
			want: "not both and not neither",
			payload: ContentWritten{
				Kind: KindBrief, Bytes: prose, BlobRef: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
				ByteLen: int64(len(prose)), SHA256: digestOf(prose),
			},
		},
		{
			what: "a payload carrying neither bytes nor a blob reference", eventType: TypeContentCreated,
			want: "not both and not neither",
			payload: ContentWritten{
				Kind: KindBrief, ByteLen: int64(len(prose)), SHA256: digestOf(prose),
			},
		},
		{
			what: "findings on content.created", eventType: TypeContentCreated,
			want: `kind "findings" does not belong on content.created`,
			payload: ContentWritten{
				Kind: KindFindings, Bytes: prose, ByteLen: int64(len(prose)), SHA256: digestOf(prose),
			},
		},
		{
			what: "a Brief on content.appended", eventType: TypeContentAppended,
			want: `kind "brief" does not belong on content.appended`,
			payload: ContentWritten{
				Kind: KindBrief, Bytes: prose, ByteLen: int64(len(prose)), SHA256: digestOf(prose),
			},
		},
		{
			// A row whose recorded length or digest contradicts its own bytes is a row
			// SegmentBytes will refuse forever. The projection is where that is still
			// catchable, and catching it there keeps the read-side verification a
			// statement about the sidecar file rather than about the log.
			what: "a payload whose recorded length contradicts its bytes", eventType: TypeContentCreated,
			want: "carries 10 bytes and records 11",
			payload: ContentWritten{
				Kind: KindBrief, Bytes: prose, ByteLen: int64(len(prose)) + 1, SHA256: digestOf(prose),
			},
		},
		{
			what: "a payload whose recorded digest contradicts its bytes", eventType: TypeContentCreated,
			want: "carries bytes hashing to " + digestOf(prose),
			payload: ContentWritten{
				Kind: KindBrief, Bytes: prose, ByteLen: int64(len(prose)), SHA256: digestOf(nil),
			},
		},
	} {
		err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			p := c.payload
			if p.Content == "" {
				p.Content = tx.NewID()
			}
			return []Draft{{Type: c.eventType, Subject: node, Payload: p}}, nil
		})
		refusalMentions(t, c.what, err, c.want)
	}

	// Nothing landed. A refused payload leaves no row and no event, because the
	// projection runs inside the same transaction as the append.
	h.wantRowCount("after every refused payload", "content", "", nil, 0)

	// An unknown kind never gets as far as a payload: ContentDraft refuses it, so
	// there is no event for insertContent to see and no row for the CHECK to catch.
	for _, kind := range []ContentKind{"prose", "", "Brief"} {
		err := h.writeError(node, kind, prose)
		refusalMentions(t, "the kind "+string(kind), err, "is not a content kind")
	}
}

// ---------------------------------------------------------------------------
// 8. No parse-back path (D36, D40)
// ---------------------------------------------------------------------------

// TestNothingReadsContentBackInFromAFile is a claim about the absence of a code
// path, which no ordinary assertion can make.
//
// D36 makes the store the source of truth for all content including prose and D40
// gives intake ownership of a blob after import; together they say every markdown
// rendering is a projection that is never parsed back, and nothing round-trips out
// to a hand-edited file. The API surface is what actually enforces that: the only
// way content enters the store is a caller handing bytes to Tx.ContentDraft, and
// there is no exported function anywhere in this package that takes a path and
// produces content.
//
// So this test checks the property structurally, over the package's own source:
// every file this package reads must be one it wrote itself. It is an odd-looking
// test and it earns its place, because an ingest-from-file helper is exactly the
// kind of addition that looks harmless in a diff and quietly gives prose two
// sources of truth.
func TestNothingReadsContentBackInFromAFile(t *testing.T) {
	// The two functions in this package that may read a file, and why.
	allowed := map[string]string{
		"SegmentBytes":    "resolves a spilled segment from the sidecar file the store itself wrote",
		"backup":          "copies the database aside before a migration",
		"ReapOrphanBlobs": "lists the blob sidecar directory to find files no live content row references (D68); it never reads a blob's bytes back in as content",
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(f os.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse this package: %v", err)
	}

	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkgName, ok := sel.X.(*ast.Ident)
					if !ok || pkgName.Name != "os" {
						return true
					}
					// Open and Read cover every way this package could take bytes off
					// the filesystem: os.Open, os.OpenFile, os.ReadFile, os.ReadDir.
					if !strings.HasPrefix(sel.Sel.Name, "Open") && !strings.HasPrefix(sel.Sel.Name, "Read") {
						return true
					}
					seen[fn.Name.Name] = true
					if _, ok := allowed[fn.Name.Name]; !ok {
						t.Errorf("%s reads the filesystem (os.%s at %s); content has one source of truth (D36, D40) and every file this package reads must be one it wrote",
							fn.Name.Name, sel.Sel.Name, fset.Position(call.Pos()))
					}
					return true
				})
			}
		}
	}

	// The control: if the walk found nothing, the assertion above would pass over an
	// empty set and say nothing at all.
	for name, why := range allowed {
		if !seen[name] {
			t.Errorf("%s no longer reads a file (%s), so this test is no longer looking at anything", name, why)
		}
	}
}
