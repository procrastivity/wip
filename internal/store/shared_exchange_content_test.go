//go:build sharedstoreexperiment

package store_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// labContentPayload decodes one action's ContentWritten — the shared payload
// both content event types carry.
func labContentPayload(t *testing.T, source store.ExperimentSource, i int) store.ContentWritten {
	t.Helper()
	var p store.ContentWritten
	if err := json.Unmarshal(source.Actions[i].Event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// labContentClaim forges one content claim in the shape a second independent
// write takes: fresh event and content identities, caller-chosen type, kind,
// subject, source-space baseline and storage. Version and Host clone base so
// the envelope stays well-formed; everything else is the claim under test.
func labContentClaim(t *testing.T, s *store.Store, base store.ExperimentSource, eventType string, kind store.ContentKind, subject, baseline string, data []byte, spill bool) store.ExperimentSource {
	t.Helper()
	sum := sha256.Sum256(data)
	p := store.ContentWritten{
		Kind: kind, Content: s.NewID(), ByteLen: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}
	if spill {
		p.BlobRef = p.Content
	} else {
		p.Bytes = data
	}
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	id := s.NewID()
	return store.ExperimentSource{
		Version: base.Version,
		Host:    base.Host,
		Actions: []store.ExperimentAction{{
			Event: store.Event{
				ID: id, Type: eventType, OccurredAt: base.Actions[0].Event.OccurredAt,
				Actor: base.Actions[0].Event.Actor, Causation: id, Correlation: id,
				Repo: base.Actions[0].Event.Repo, Subject: subject, Payload: payload,
			},
			Baseline: baseline,
		}},
	}
}

// labInstallBlob copies one spilled blob file from src's sidecar directory
// into dst's — the stand-in for the verified, atomic install sync-content
// performs before it rechecks a bytes-pending group.
func labInstallBlob(t *testing.T, src, dst *store.Store, ref string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(src.Path()), "blobs", ref))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(dst.Path()), "blobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ref), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every create-once kind lands on every node kind — the native WriteOnce
// resolves any live node and the fold's contentSubjectExists counts any node
// row, so the admitted matrix is kind-agnostic on the owner side. Each write
// is its own committed group whose baseline is the subject's prior tail.
func TestSharedStoreContentCreateOnceBound(t *testing.T) {
	p := newLabPair(t)
	nodes := []string{p.matter, labStageID(t, p.hybrid, p.repo), p.step1}
	kinds := []store.ContentKind{store.KindBrief, store.KindWorkplan, store.KindBody}
	var last store.ExperimentSource
	for _, subject := range nodes {
		for _, kind := range kinds {
			text := string(kind) + " on " + subject
			source := labExportAfter(t, p.hybrid, "Hybrid", subject, store.TypeContentCreated,
				func() error {
					_, err := writesurface.WriteOnce(labCtx, p.hybrid, store.ActorHuman, p.repo, subject, kind, []byte(text))
					return err
				})
			last = source
			d := labImportBound(t, p.fluke, source, p.repo, "integrated")
			if len(d.Effects) != 1 {
				t.Fatalf("%s on %s effects = %v", kind, subject, d.Effects)
			}
			got, err := p.fluke.Content(labCtx, subject, kind)
			if err != nil || string(got) != text {
				t.Fatalf("%s on %s = %q %v", kind, subject, got, err)
			}
			segments, err := p.fluke.ContentSegments(labCtx, subject, kind)
			if err != nil || len(segments) != 1 || segments[0].Spilled {
				t.Fatalf("%s on %s segments = %+v %v", kind, subject, segments, err)
			}
		}
	}
	// The verified self-echo maps back to native identities and writes nothing.
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, last, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != last.Actions[0].Event.ID ||
		len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("content self-echo was not inert", echo)
	}
	// An exact retry on the receiver is equally inert.
	flukeBefore := len(labEvents(t, p.fluke))
	again := labImportBound(t, p.fluke, last, p.repo, "integrated")
	if !reflect.DeepEqual(last.Actions, again.Source.Actions) || len(labEvents(t, p.fluke)) != flukeBefore {
		t.Fatal("content retry was not inert", again)
	}
	labRebuild(t, p.fluke)
	// Create-once content mints identity, so it stays bound-only like the
	// births: the unscoped import retains the same group as evidence.
	labImport(t, p.fluke, last, "conflict")
}

// Findings accumulate on nodes and backlog entries alike — the only
// append-only kind — on both the bound and the unbound path. Sequential
// appends chain through the ledger; a concurrent claim against the same
// observed tail is an independent fact and survives beside them.
func TestSharedStoreContentFindingsBound(t *testing.T) {
	p := newLabPair(t)

	f1 := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, p.step1, []byte("finding one"))
			return err
		})
	labImportBound(t, p.fluke, f1, p.repo, "integrated")
	f2 := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, p.step1, []byte("finding two"))
			return err
		})
	labImportBound(t, p.fluke, f2, p.repo, "integrated")

	// Concurrent: fresh event and content identities, baseline still the
	// pre-f1 subject tail f1 observed. It is not the tail anymore — findings
	// ordering is causal, so it integrates beside the sequential pair.
	concurrent := labContentClaim(t, p.hybrid, f1, store.TypeContentAppended, store.KindFindings,
		p.step1, f1.Actions[0].Baseline, []byte("concurrent finding"), false)
	labImportBound(t, p.fluke, concurrent, p.repo, "integrated")

	segments, err := p.fluke.ContentSegments(labCtx, p.step1, store.KindFindings)
	if err != nil || len(segments) != 3 {
		t.Fatalf("finding segments = %+v %v", segments, err)
	}
	seen := map[string]bool{}
	for _, seg := range segments {
		data, err := p.fluke.SegmentBytes(labCtx, seg.ID)
		if err != nil {
			t.Fatal(err)
		}
		seen[string(data)] = true
	}
	for _, want := range []string{"finding one", "finding two", "concurrent finding"} {
		if !seen[want] {
			t.Fatalf("missing finding %q in %v", want, seen)
		}
	}

	// Findings on a backlog entry: entered must land first, then the append
	// resolves its entry subject through the same classifier.
	entry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Triage me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	bf := labExportAfter(t, p.hybrid, "Hybrid", entry.ID, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, entry.ID, []byte("triage note"))
			return err
		})
	labImportBound(t, p.fluke, bf, p.repo, "integrated")
	got, err := p.fluke.Content(labCtx, entry.ID, store.KindFindings)
	if err != nil || string(got) != "triage note" {
		t.Fatalf("entry findings = %q %v", got, err)
	}

	// The family mutates an already-paired object, so the unbound import
	// agrees with the bound one — repo ids coincide in the lab copy.
	uf := labExportAfter(t, p.hybrid, "Hybrid", p.matter, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, p.matter, []byte("unbound finding"))
			return err
		})
	labImport(t, p.fluke, uf, "integrated")
	got, err = p.fluke.Content(labCtx, p.matter, store.KindFindings)
	if err != nil || string(got) != "unbound finding" {
		t.Fatalf("unbound findings = %q %v", got, err)
	}

	// The verified self-echo maps back to the native event identity.
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, f1, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != f1.Actions[0].Event.ID ||
		len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("finding self-echo was not inert", echo)
	}
	labRebuild(t, p.fluke)
}

// A finding whose subject has not arrived waits; one whose baseline names a
// finding that has not arrived waits the same way — and both integrate on
// recheck once their evidence lands, in causal order.
func TestSharedStoreContentFindingPending(t *testing.T) {
	p := newLabPair(t)

	// Subject missing entirely: the entry exists only on the source.
	entry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Late entry", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ef := labExportAfter(t, p.hybrid, "Hybrid", entry.ID, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, entry.ID, []byte("early note"))
			return err
		})
	d := labImportBound(t, p.fluke, ef, p.repo, "pending")
	if d.Reason != "finding awaiting subject" {
		t.Fatalf("missing-subject reason = %q", d.Reason)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	followRecheck(t, p.fluke, ef, "integrated")
	got, err := p.fluke.Content(labCtx, entry.ID, store.KindFindings)
	if err != nil || string(got) != "early note" {
		t.Fatalf("rechecked entry findings = %q %v", got, err)
	}

	// Baseline missing: f2's baseline is f1's source event, which has not
	// arrived — the subject exists, the causal evidence does not.
	f1 := labExportAfter(t, p.hybrid, "Hybrid", p.step2, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, p.step2, []byte("first"))
			return err
		})
	f2 := labExportAfter(t, p.hybrid, "Hybrid", p.step2, store.TypeContentAppended,
		func() error {
			_, err := writesurface.AppendFinding(labCtx, p.hybrid, store.ActorHuman, p.repo, p.step2, []byte("second"))
			return err
		})
	d = labImportBound(t, p.fluke, f2, p.repo, "pending")
	if d.Reason != "unknown baseline dependency" {
		t.Fatalf("missing-baseline reason = %q", d.Reason)
	}
	labImportBound(t, p.fluke, f1, p.repo, "integrated")
	// ApplyExchangeInbox rechecks pending groups after each batch; the
	// explicit recheck is the same path.
	followRecheck(t, p.fluke, f2, "integrated")
	segments, err := p.fluke.ContentSegments(labCtx, p.step2, store.KindFindings)
	if err != nil || len(segments) != 2 {
		t.Fatalf("rechecked finding segments = %+v %v", segments, err)
	}
	labRebuild(t, p.fluke)
}

// The fold's own rules are the classifier's floor: kind/type pairing, global
// content identity, byte integrity, node-only create-once subjects and the
// blob-reference shape all refuse as retained conflicts — never merges,
// never invented rows.
func TestSharedStoreContentConflicts(t *testing.T) {
	p := newLabPair(t)

	plan := labExportAfter(t, p.hybrid, "Hybrid", p.matter, store.TypeContentCreated,
		func() error {
			_, err := writesurface.WriteOnce(labCtx, p.hybrid, store.ActorHuman, p.repo, p.matter, store.KindWorkplan, []byte("first plan"))
			return err
		})
	labImportBound(t, p.fluke, plan, p.repo, "integrated")
	planPayload := labContentPayload(t, plan, 0)

	// A second create-once claim of the same kind on the same subject, its
	// baseline correctly naming the accepted tail: competing content conflicts.
	rival := labContentClaim(t, p.hybrid, plan, store.TypeContentCreated, store.KindWorkplan,
		p.matter, plan.Actions[0].Event.ID, []byte("second plan"), false)
	d := labImportBound(t, p.fluke, rival, p.repo, "conflict")
	if !strings.Contains(d.Reason, "competing") {
		t.Fatalf("rival workplan reason = %q", d.Reason)
	}
	if got, err := p.fluke.Content(labCtx, p.matter, store.KindWorkplan); err != nil || string(got) != "first plan" {
		t.Fatalf("accepted content changed: %q %v", got, err)
	}

	// Kind/type pairing is the fold's rule in both directions.
	badCreated := labContentClaim(t, p.hybrid, plan, store.TypeContentCreated, store.KindFindings,
		p.step1, "", []byte("x"), false)
	labImportBound(t, p.fluke, badCreated, p.repo, "conflict")
	badAppended := labContentClaim(t, p.hybrid, plan, store.TypeContentAppended, store.KindBrief,
		p.step1, "", []byte("x"), false)
	labImportBound(t, p.fluke, badAppended, p.repo, "conflict")

	// A backlog entry can never take create-once content — permanent, so the
	// classifier refuses rather than waits.
	entry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "No prose", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	onEntry := labContentClaim(t, p.hybrid, plan, store.TypeContentCreated, store.KindBrief,
		entry.ID, "", []byte("never"), false)
	d = labImportBound(t, p.fluke, onEntry, p.repo, "conflict")
	if !strings.Contains(d.Reason, "nodes") {
		t.Fatalf("create-once on entry reason = %q", d.Reason)
	}

	// Content identity is global: reusing an accepted id conflicts even under
	// a different kind and subject.
	reuse := labContentClaim(t, p.hybrid, plan, store.TypeContentCreated, store.KindBody,
		p.step1, "", []byte("reused"), false)
	rp := labContentPayload(t, reuse, 0)
	rp.Content = planPayload.Content
	payload, err := json.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	reuse.Actions[0].Event.Payload = payload
	d = labImportBound(t, p.fluke, reuse, p.repo, "conflict")
	if !strings.Contains(d.Reason, "content identity") {
		t.Fatalf("reused identity reason = %q", d.Reason)
	}

	// Inline bytes that contradict the recorded length/digest refuse.
	tampered := labContentClaim(t, p.hybrid, plan, store.TypeContentCreated, store.KindBody,
		p.step1, "", []byte("good bytes"), false)
	tp := labContentPayload(t, tampered, 0)
	tp.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	payload, err = json.Marshal(tp)
	if err != nil {
		t.Fatal(err)
	}
	tampered.Actions[0].Event.Payload = payload
	d = labImportBound(t, p.fluke, tampered, p.repo, "conflict")
	if !strings.Contains(d.Reason, "integrity") {
		t.Fatalf("tampered reason = %q", d.Reason)
	}

	// A spilled claim whose blob reference is not the content identity is not
	// a shape sync-content can ever install — refused up front.
	spillBad := labContentClaim(t, p.hybrid, plan, store.TypeContentCreated, store.KindBody,
		p.step2, "", []byte("spilled"), true)
	sp := labContentPayload(t, spillBad, 0)
	sp.BlobRef = "not-the-content-id"
	payload, err = json.Marshal(sp)
	if err != nil {
		t.Fatal(err)
	}
	spillBad.Actions[0].Event.Payload = payload
	d = labImportBound(t, p.fluke, spillBad, p.repo, "conflict")
	if d.Reason == "" {
		t.Fatal("mis-shaped spill was not refused")
	}
	labRebuild(t, p.fluke)
}

// Spilled bytes are the content-readiness seam: the group pends atomically —
// no partial effects, no unreadable row — stays pending across rechecks while
// the blob is absent or contradicts its metadata, and integrates whole once
// verified bytes are installed. The retained decision carries the full
// ContentWritten payload (blob_ref, byte_len, sha256) as inspectable evidence.
func TestSharedStoreContentSpilledPending(t *testing.T) {
	p := newLabPair(t)
	brief := make([]byte, store.SpillThreshold+16)
	for i := range brief {
		brief[i] = byte(i)
	}
	finding := make([]byte, store.SpillThreshold+32)
	for i := range finding {
		finding[i] = byte(i * 7)
	}
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		b, err := tx.ContentDraft(p.matter, store.KindBrief, brief)
		if err != nil {
			return nil, err
		}
		f, err := tx.ContentDraft(p.step1, store.KindFindings, finding)
		if err != nil {
			return nil, err
		}
		return []store.Draft{b, f}, nil
	})
	if len(source.Actions) != 2 {
		t.Fatalf("spilled group actions = %d", len(source.Actions))
	}
	bp, fp := labContentPayload(t, source, 0), labContentPayload(t, source, 1)
	if bp.Bytes != nil || bp.BlobRef != bp.Content || fp.Bytes != nil || fp.BlobRef != fp.Content {
		t.Fatalf("payloads did not spill: %+v %+v", bp, fp)
	}

	d := labImportBound(t, p.fluke, source, p.repo, "pending")
	if d.Reason != "content bytes unavailable" {
		t.Fatalf("missing-bytes reason = %q", d.Reason)
	}
	// Atomic: neither member landed — no content rows, no new events.
	if segs, err := p.fluke.ContentSegments(labCtx, p.matter, store.KindBrief); err != nil || len(segs) != 0 {
		t.Fatalf("pending brief segments = %+v %v", segs, err)
	}
	if segs, err := p.fluke.ContentSegments(labCtx, p.step1, store.KindFindings); err != nil || len(segs) != 0 {
		t.Fatalf("pending finding segments = %+v %v", segs, err)
	}
	before := len(labEvents(t, p.fluke))
	followRecheck(t, p.fluke, source, "pending")
	if len(labEvents(t, p.fluke)) != before {
		t.Fatal("pending recheck minted events")
	}

	// Bytes present but contradicting the recorded metadata pend too —
	// recoverable, never projected.
	corruptDir := filepath.Join(filepath.Dir(p.fluke.Path()), "blobs")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, bp.BlobRef), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	d = followRecheck(t, p.fluke, source, "pending")
	if d.Reason != "content bytes failed verification" {
		t.Fatalf("corrupt-bytes reason = %q", d.Reason)
	}

	// Verified install flips the whole unit: both spilled rows land and read
	// back byte-exact through the same path SegmentBytes serves natively.
	labInstallBlob(t, p.hybrid, p.fluke, bp.BlobRef)
	labInstallBlob(t, p.hybrid, p.fluke, fp.BlobRef)
	d = followRecheck(t, p.fluke, source, "integrated")
	if len(d.Effects) != 2 {
		t.Fatalf("integrated effects = %v", d.Effects)
	}
	segs, err := p.fluke.ContentSegments(labCtx, p.matter, store.KindBrief)
	if err != nil || len(segs) != 1 || !segs[0].Spilled {
		t.Fatalf("integrated brief segments = %+v %v", segs, err)
	}
	got, err := p.fluke.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("spilled brief bytes = %d %v", len(got), err)
	}
	fsegs, err := p.fluke.ContentSegments(labCtx, p.step1, store.KindFindings)
	if err != nil || len(fsegs) != 1 || !fsegs[0].Spilled {
		t.Fatalf("integrated finding segments = %+v %v", fsegs, err)
	}
	got, err = p.fluke.SegmentBytes(labCtx, fsegs[0].ID)
	if err != nil || !bytes.Equal(got, finding) {
		t.Fatalf("spilled finding bytes = %d %v", len(got), err)
	}
	labRebuild(t, p.fluke)
	segs, err = p.fluke.ContentSegments(labCtx, p.matter, store.KindBrief)
	if err != nil || len(segs) != 1 || !segs[0].Spilled {
		t.Fatalf("rebuilt brief segments = %+v %v", segs, err)
	}
}

// The wire path admits the whole content family: create-once writes show up
// in ExchangeStatus automatically and findings appends ride the same
// portable units, folding against the receiver's own repo binding.
func TestSharedStoreContentWire(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	status, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba := status.Binding
	bb := exchangeBind(t, sb, b, endpoint)
	upload := func(groups ...string) {
		t.Helper()
		exchangePass(t, sa, ca, ba, groups...)
		exchangePass(t, sb, cb, bb)
	}

	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Wire", "wire")
	if err != nil {
		t.Fatal(err)
	}
	upload(sourceGroup(t, sa, matter.ID, store.TypeMatterCreated))

	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, matter.ID, store.KindWorkplan, []byte("wire plan")); err != nil {
		t.Fatal(err)
	}
	planGroup := sourceGroup(t, sa, matter.ID, store.TypeContentCreated)
	st, err := sa.ExchangeStatus(labCtx, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.PendingUpload, planGroup) {
		t.Fatalf("content group not pending upload: %+v", st.PendingUpload)
	}
	upload(planGroup)
	sourceDecision(t, sb, planGroup, "integrated")
	got, err := sb.Content(labCtx, matter.ID, store.KindWorkplan)
	if err != nil || string(got) != "wire plan" {
		t.Fatalf("receiver workplan = %q %v", got, err)
	}

	if _, err := writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, matter.ID, []byte("wire finding")); err != nil {
		t.Fatal(err)
	}
	findingGroup := sourceGroup(t, sa, matter.ID, store.TypeContentAppended)
	upload(findingGroup)
	sourceDecision(t, sb, findingGroup, "integrated")
	got, err = sb.Content(labCtx, matter.ID, store.KindFindings)
	if err != nil || string(got) != "wire finding" {
		t.Fatalf("receiver findings = %q %v", got, err)
	}
	labRebuild(t, sb)
}

// labSpilledBrief writes one over-threshold Brief on a fresh Matter and
// returns the node plus the deterministic bytes, so digest assertions never
// depend on a fixture file.
func labSpilledBrief(t *testing.T, s *store.Store, repo, title string, seed byte) (store.Node, []byte) {
	t.Helper()
	n, err := writesurface.CreateMatter(labCtx, s, store.ActorHuman, repo, title, "")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, store.SpillThreshold+int(seed)+1)
	for i := range data {
		data[i] = byte(i)*seed + seed
	}
	if _, err := writesurface.WriteOnce(labCtx, s, store.ActorHuman, repo, n.Locator, store.KindBrief, data); err != nil {
		t.Fatal(err)
	}
	return n, data
}

// labDigest is the payload/storage digest of data — the same value the
// ContentWritten payload records and the blob routes key on.
func labDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func labCount(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}

// labBlobPutBeforeChange asserts the feed saw a PUT for digest strictly
// between the changes POSTs surrounding group's position in feed order —
// upload-before-commit, not merely "a PUT happened someday".
func labBlobPutBeforeChange(t *testing.T, feed *labExchangeFeed, group, digest string) {
	t.Helper()
	posts := feed.postChangesIndices()
	order := feed.changeOrder()
	at := -1
	for i, g := range order {
		if g == group {
			at = i
		}
	}
	if at < 0 || at >= len(posts) {
		t.Fatalf("group %s has no committed changes POST", group)
	}
	put := feed.requestIndex("PUT", "/blobs/"+digest)
	if put < 0 || put > posts[at] {
		t.Fatalf("blob PUT for %s (request %d) did not precede its changes POST (request %d)", digest, put, posts[at])
	}
	if at > 0 && put < posts[at-1] {
		t.Fatalf("blob PUT for %s (request %d) landed before the previous group's changes POST (request %d)", digest, put, posts[at-1])
	}
}

// Spilled content crosses the wire end to end: the sender's RunExchange PUTs
// each unique digest before the changes POST that names it, the receiver
// pends on absent-then-installed bytes across passes, and the integrated
// rows read back byte-exact through SegmentBytes.
func TestSharedStoreContentBlobWire(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	status, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba := status.Binding
	bb := exchangeBind(t, sb, b, endpoint)

	n, brief := labSpilledBrief(t, sa, a.repo, "Blob Wire", 3)
	finding := make([]byte, store.SpillThreshold+40)
	for i := range finding {
		finding[i] = byte(i * 5)
	}
	if _, err := writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, n.Locator, finding); err != nil {
		t.Fatal(err)
	}
	briefGroup := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	findingGroup := sourceGroup(t, sa, n.ID, store.TypeContentAppended)
	briefDigest, findingDigest := labDigest(brief), labDigest(finding)

	// RunExchange auto-selects every pending group: each spilled blob must
	// land before the changes POST that makes its group receivable, and the
	// sender's own pass costs no blob GETs — it has no pending imports.
	if _, err := sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	labBlobPutBeforeChange(t, feed, briefGroup, briefDigest)
	labBlobPutBeforeChange(t, feed, findingGroup, findingDigest)
	if got, ok := feed.blob(briefDigest); !ok || !bytes.Equal(got, brief) {
		t.Fatalf("feed blob %s = %d bytes (present %v)", briefDigest, len(got), ok)
	}
	if got, ok := feed.blob(findingDigest); !ok || !bytes.Equal(got, finding) {
		t.Fatalf("feed blob %s = %d bytes (present %v)", findingDigest, len(got), ok)
	}
	if feed.hasRequest("GET", "/blobs/") {
		t.Fatal("sender pass fetched blobs it does not need")
	}

	// Receiver pass one retains the changes and pends the content groups:
	// receipt is durable, but the blob fetch runs against retained decisions,
	// which only exist after the import below.
	exchangePass(t, sb, cb, bb)
	d := sourceDecision(t, sb, briefGroup, "pending")
	if d.Reason != "content bytes unavailable" {
		t.Fatalf("brief pending reason = %q", d.Reason)
	}
	sourceDecision(t, sb, findingGroup, "pending")

	// Pass two fetches, verifies and installs both blobs; the recheck flips
	// both groups whole.
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, briefGroup, "integrated")
	sourceDecision(t, sb, findingGroup, "integrated")
	segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 || !segs[0].Spilled {
		t.Fatalf("receiver brief segments = %+v %v", segs, err)
	}
	got, err := sb.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief bytes = %d %v", len(got), err)
	}
	got, err = sb.Content(labCtx, n.ID, store.KindFindings)
	if err != nil || !bytes.Equal(got, finding) {
		t.Fatalf("receiver findings = %d %v", len(got), err)
	}

	// An explicit re-upload PUTs again — the contract's idempotent retry —
	// and neither side gains effects or bytes.
	senderEvents := len(labEvents(t, sa))
	receiverEvents := len(labEvents(t, sb))
	exchangePass(t, sa, ca, ba, briefGroup, findingGroup)
	exchangePass(t, sb, cb, bb)
	if len(labEvents(t, sa)) != senderEvents || len(labEvents(t, sb)) != receiverEvents {
		t.Fatal("blob retry pass duplicated effects")
	}
	if got, ok := feed.blob(briefDigest); !ok || !bytes.Equal(got, brief) {
		t.Fatal("blob retry corrupted stored bytes")
	}
	labRebuild(t, sb)
}

// Download-side failure modes all converge on the same honest state: the
// group stays pending with inspectable evidence until verified bytes exist
// locally — absent blobs, wrong server bytes, local corruption the sync
// refuses to overwrite, and transient GET errors that fail the pass without
// losing the durable receipt.
func TestSharedStoreContentBlobPending(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
	bb := exchangeBind(t, sb, b, endpoint)
	blobDir := filepath.Join(filepath.Dir(sb.Path()), "blobs")

	// spilledMatter writes one over-threshold Brief on a fresh Matter and
	// uploads its birth and content groups through the feed.
	spilledMatter := func(title string, seed byte) (store.Node, []byte, string) {
		n, data := labSpilledBrief(t, sa, a.repo, title, seed)
		birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
		content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
		exchangePass(t, sa, ca, ba, birth, content)
		return n, data, content
	}
	assertBrief := func(n store.Node, want []byte) {
		t.Helper()
		segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
		if err != nil || len(segs) != 1 || !segs[0].Spilled {
			t.Fatalf("receiver brief segments = %+v %v", segs, err)
		}
		got, err := sb.SegmentBytes(labCtx, segs[0].ID)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("receiver brief bytes = %d %v", len(got), err)
		}
	}

	// Absent-then-served: the blob 404s until the feed stores it, and the
	// pending group heals on the pass after it does.
	n1, brief1, content1 := spilledMatter("Absent Then Served", 5)
	digest1 := labDigest(brief1)
	feed.removeBlob(digest1)
	exchangePass(t, sb, cb, bb) // retains changes; import pends before any fetch
	exchangePass(t, sb, cb, bb) // GET 404: indistinguishable absent, stays pending
	d := sourceDecision(t, sb, content1, "pending")
	if d.Reason != "content bytes unavailable" {
		t.Fatalf("absent-bytes reason = %q", d.Reason)
	}
	if !feed.hasRequest("GET", "/blobs/"+digest1) {
		t.Fatal("absent blob was never fetched")
	}
	feed.setBlob(digest1, brief1)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content1, "integrated")
	assertBrief(n1, brief1)

	// Wrong-then-right: bytes that fail digest verification are never
	// installed — the group pends exactly as if the blob were absent.
	n2, brief2, content2 := spilledMatter("Wrong Then Right", 7)
	digest2 := labDigest(brief2)
	good2, _ := feed.blob(digest2)
	feed.setBlob(digest2, []byte("server-side wrong bytes"))
	exchangePass(t, sb, cb, bb)
	exchangePass(t, sb, cb, bb)
	d = sourceDecision(t, sb, content2, "pending")
	if d.Reason != "content bytes unavailable" {
		t.Fatalf("wrong-bytes reason = %q", d.Reason)
	}
	p2 := labContentPayload(t, d.Source, 0)
	if _, err := os.Stat(filepath.Join(blobDir, p2.BlobRef)); !os.IsNotExist(err) {
		t.Fatal("unverified bytes were installed")
	}
	feed.setBlob(digest2, good2)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content2, "integrated")
	assertBrief(n2, brief2)

	// Corrupt-then-reaped: a local file contradicting the payload's metadata
	// pends as "failed verification"; verified sync bytes deliberately never
	// overwrite local damage — removing the orphan (ReapOrphanBlobs' job,
	// since a pending group's blob is unreferenced) is the repair path.
	n3, brief3, content3 := spilledMatter("Corrupt Then Reaped", 11)
	exchangePass(t, sb, cb, bb)
	p3 := labContentPayload(t, sourceDecision(t, sb, content3, "pending").Source, 0)
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(blobDir, p3.BlobRef)
	if err := os.WriteFile(corruptPath, []byte("local corruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sb, cb, bb) // fetch verifies, install declines to clobber
	d = sourceDecision(t, sb, content3, "pending")
	if d.Reason != "content bytes failed verification" {
		t.Fatalf("corrupt-bytes reason = %q", d.Reason)
	}
	if err := os.Remove(corruptPath); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content3, "integrated")
	assertBrief(n3, brief3)

	// Transient GET failure: the pass fails honestly — cursor and inbox rows
	// are already durable — and the next healthy pass completes the install.
	n4, brief4, content4 := spilledMatter("Transient Get", 13)
	exchangePass(t, sb, cb, bb)
	feed.serveBlobStatus(0, http.StatusInternalServerError)
	err := sb.ExchangePass(labCtx, cb, bb, nil)
	if err == nil || !strings.Contains(err.Error(), "blob HTTP 500") {
		t.Fatalf("blob GET failure = %v", err)
	}
	sourceDecision(t, sb, content4, "pending")
	feed.serveBlobStatus(0, 0)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content4, "integrated")
	assertBrief(n4, brief4)
	labRebuild(t, sb)
}

// A failed blob PUT aborts the pass before the changes POST: the group is
// never ack-able without its bytes, PendingUpload retains it, and the retry
// completes the whole transfer.
func TestSharedStoreContentBlobUploadFailure(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	if _, err := sa.PairExchange(labCtx, a.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	bb := exchangeBind(t, sb, b, endpoint)

	n, brief := labSpilledBrief(t, sa, a.repo, "Upload Failure", 19)
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	digest := labDigest(brief)

	feed.serveBlobStatus(http.StatusInternalServerError, 0)
	if _, err := sa.RunExchange(labCtx, ca, a.repo); err == nil || !strings.Contains(err.Error(), "blob HTTP 500") {
		t.Fatalf("blob PUT failure = %v", err)
	}
	if !slices.Contains(feed.changeOrder(), birth) {
		t.Fatal("blob-free group did not upload")
	}
	if slices.Contains(feed.changeOrder(), content) {
		t.Fatal("content group posted without its blob")
	}
	if !feed.hasRequest("PUT", "/blobs/"+digest) {
		t.Fatal("blob PUT was never attempted")
	}

	feed.serveBlobStatus(0, 0)
	if _, err := sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	if got, ok := feed.blob(digest); !ok || !bytes.Equal(got, brief) {
		t.Fatal("retried blob upload stored wrong bytes")
	}
	exchangePass(t, sb, cb, bb)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content, "integrated")
	segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 {
		t.Fatalf("receiver brief segments = %+v %v", segs, err)
	}
	got, err := sb.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief bytes = %d %v", len(got), err)
	}
	labRebuild(t, sb)
}

// The real Cloud blob API carries spilled content end to end: Laravel
// verifies length and digest on PUT, serves verified bytes under
// X-Blob-SHA256 on GET, and the receiver integrates byte-exact.
func TestLaravelExchangeSpilledContent(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ba := exchangeBind(t, sa, a, f.endpoint())
	bb := exchangeBind(t, sb, b, f.endpoint())
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Member}

	n, brief := labSpilledBrief(t, sa, a.repo, "Laravel Blobs", 23)
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	exchangePass(t, sa, ca, ba, birth, content)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content, "pending")
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content, "integrated")
	segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 || !segs[0].Spilled {
		t.Fatalf("receiver brief segments = %+v %v", segs, err)
	}
	got, err := sb.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief bytes = %d %v", len(got), err)
	}
	labRebuild(t, sb)
}

// labFillBytes is a deterministic byte pattern for size-boundary fixtures.
func labFillBytes(n int, seed byte) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i)*seed + seed
	}
	return data
}

// labUnicodeBytes repeats a six-byte multibyte unit (λ + 🧪) to length n, so
// spilled fixtures exercise non-ASCII content at scale.
func labUnicodeBytes(n int) []byte {
	return bytes.Repeat([]byte("λ🧪"), n/6+1)[:n]
}

// Step-07's round-trip matrix: every admitted content kind on every supported
// owner — Matter/Stage/Step nodes and backlog entries for findings — across
// empty, small, non-ASCII, exactly-SpillThreshold (inline: spill is strictly
// greater) and larger-than-1 MiB payloads. Everything crosses the real feed
// transport, and after both stores reopen and the receiver rebuilds every
// segment is compared byte-exact against independently computed length and
// SHA-256 of the source bytes — never against the other store's copy.
func TestSharedStoreContentRoundTripMatrix(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}}
	if _, err := sa.PairExchange(labCtx, a.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	bb := exchangeBind(t, sb, b, endpoint)

	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Matrix", "matrix")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, "matrix", "Phase")
	if err != nil {
		t.Fatal(err)
	}
	step, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo, "matrix/phase", "Do")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := writesurface.BacklogAdd(labCtx, sa, store.ActorHuman, a.repo,
		store.ProvenanceIntake, "Triage", "", "")
	if err != nil {
		t.Fatal(err)
	}

	type cell struct {
		subject string
		kind    store.ContentKind
		data    []byte
		spilled bool
		group   string
	}
	cells := []cell{
		{matter.ID, store.KindBrief, []byte{}, false, ""},
		{matter.ID, store.KindWorkplan, []byte("plan: λ  🧪 — naïve"), false, ""},
		{stage.ID, store.KindBody, bytes.Repeat([]byte{'x'}, store.SpillThreshold), false, ""},
		{step.ID, store.KindBrief, labFillBytes(store.SpillThreshold+1, 5), true, ""},
		{stage.ID, store.KindWorkplan, labFillBytes(2*store.SpillThreshold+77, 9), true, ""},
		{step.ID, store.KindBody, labUnicodeBytes(3*store.SpillThreshold + 123), true, ""},
		{matter.ID, store.KindFindings, []byte{}, false, ""},
		{step.ID, store.KindFindings, []byte("finding: π r², , 🧪"), false, ""},
		{entry.ID, store.KindFindings, bytes.Repeat([]byte{'f'}, store.SpillThreshold), false, ""},
		{matter.ID, store.KindFindings, labFillBytes(store.SpillThreshold+1, 3), true, ""},
		{step.ID, store.KindFindings, labFillBytes(4*store.SpillThreshold, 7), true, ""},
		{entry.ID, store.KindFindings, labUnicodeBytes(1024), false, ""},
		{entry.ID, store.KindFindings, labFillBytes(store.SpillThreshold+33, 11), true, ""},
	}
	// wants[subject|kind][digest] is the independently computed expectation:
	// the source bytes themselves, their length and their spill placement.
	type wantBytes struct {
		data    []byte
		spilled bool
	}
	wants := map[string]map[string]wantBytes{}
	for i := range cells {
		c := &cells[i]
		var err error
		if c.kind.AppendOnlyKind() {
			_, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, c.subject, c.data)
		} else {
			_, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, c.subject, c.kind, c.data)
		}
		if err != nil {
			t.Fatalf("%s on %s: %v", c.kind, c.subject, err)
		}
		eventType := store.TypeContentCreated
		if c.kind.AppendOnlyKind() {
			eventType = store.TypeContentAppended
		}
		c.group = sourceGroup(t, sa, c.subject, eventType)
		key := c.subject + "|" + string(c.kind)
		if wants[key] == nil {
			wants[key] = map[string]wantBytes{}
		}
		wants[key][labDigest(c.data)] = wantBytes{data: c.data, spilled: c.spilled}
	}

	// One ordinary sender pass carries births, the backlog entry and every
	// content group — spilled blobs land before the changes that name them.
	if _, err := sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	// Receiver pass one retains and pends the spilled groups; pass two
	// fetches, verifies, installs and flips them whole.
	exchangePass(t, sb, cb, bb)
	exchangePass(t, sb, cb, bb)
	for _, c := range cells {
		sourceDecision(t, sb, c.group, "integrated")
	}

	// Reopen both stores, rebuild the receiver's projection, then verify
	// every segment on both sides against the source bytes' own metadata.
	saPath, sbPath := sa.Path(), sb.Path()
	if err := sa.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
	sa, sb = labOpen(t, saPath), labOpen(t, sbPath)
	labRebuild(t, sb)
	verify := func(s *store.Store, label string) {
		t.Helper()
		for key, expected := range wants {
			parts := strings.SplitN(key, "|", 2)
			segs, err := s.ContentSegments(labCtx, parts[0], store.ContentKind(parts[1]))
			if err != nil {
				t.Fatalf("%s %s: %v", label, key, err)
			}
			if len(segs) != len(expected) {
				t.Fatalf("%s %s: %d segments, want %d", label, key, len(segs), len(expected))
			}
			for _, seg := range segs {
				want, ok := expected[seg.SHA256]
				if !ok {
					t.Fatalf("%s %s: unexpected segment %s", label, key, seg.ID)
				}
				if seg.ByteLen != int64(len(want.data)) || seg.Spilled != want.spilled {
					t.Fatalf("%s %s segment %s: len=%d spilled=%v, want len=%d spilled=%v",
						label, key, seg.ID, seg.ByteLen, seg.Spilled, len(want.data), want.spilled)
				}
				got, err := s.SegmentBytes(labCtx, seg.ID)
				if err != nil || !bytes.Equal(got, want.data) {
					t.Fatalf("%s %s segment %s: %d bytes %v", label, key, seg.ID, len(got), err)
				}
				sum := sha256.Sum256(got)
				if hex.EncodeToString(sum[:]) != seg.SHA256 {
					t.Fatalf("%s %s segment %s: read-back digest contradicts record", label, key, seg.ID)
				}
			}
		}
	}
	verify(sa, "sender")
	verify(sb, "receiver")

	// The concatenating read API agrees: the Matter's findings are the empty
	// append plus the spilled one, and the Step's body is the 3 MiB blob.
	got, err := sb.Content(labCtx, matter.ID, store.KindFindings)
	if err != nil || !bytes.Equal(got, cells[9].data) {
		t.Fatalf("receiver matter findings = %d bytes %v", len(got), err)
	}
	got, err = sb.Content(labCtx, step.ID, store.KindBody)
	if err != nil || !bytes.Equal(got, cells[5].data) {
		t.Fatalf("receiver step body = %d bytes %v", len(got), err)
	}
	got, err = sa.Content(labCtx, entry.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	var entryWant []byte
	for _, c := range cells[8:] {
		if c.subject == entry.ID {
			entryWant = append(entryWant, c.data...)
		}
	}
	if !bytes.Equal(got, entryWant) {
		t.Fatal("sender entry findings concatenation differs")
	}
}

// A changes POST that commits server-side but loses its response is retried
// whole: the blob re-PUT is idempotent, the feed dedups the re-posted group
// exactly like Cloud's (installation, group) key, and the receiver sees one
// copy of the work — never a duplicate effect.
func TestSharedStoreContentBlobAckLost(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	status, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba := status.Binding
	bb := exchangeBind(t, sb, b, endpoint)

	n, brief := labSpilledBrief(t, sa, a.repo, "Ack Lost", 29)
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	digest := labDigest(brief)
	exchangePass(t, sa, ca, ba, birth)

	feed.failNextChanges()
	if err := sa.ExchangePass(labCtx, ca, ba, []string{content}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("lost-ack pass = %v", err)
	}
	// The change committed despite the failed response; the local ack did not.
	if got := labCount(feed.changeOrder(), content); got != 1 {
		t.Fatalf("committed copies of %s = %d", content, got)
	}
	st, err := sa.ExchangeStatus(labCtx, a.repo)
	if err != nil || !slices.Contains(st.PendingUpload, content) {
		t.Fatalf("unacknowledged group not pending: %+v %v", st, err)
	}

	// Retry: same cached envelope, idempotent blob PUT, deduped re-post.
	exchangePass(t, sa, ca, ba, content)
	if got := labCount(feed.changeOrder(), content); got != 1 {
		t.Fatal("retry duplicated the committed change")
	}
	if got, ok := feed.blob(digest); !ok || !bytes.Equal(got, brief) {
		t.Fatal("blob retry stored wrong bytes")
	}
	exchangePass(t, sb, cb, bb)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content, "integrated")
	segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 {
		t.Fatalf("receiver brief segments = %+v %v", segs, err)
	}
	got, err := sb.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief bytes = %d %v", len(got), err)
	}
	labRebuild(t, sb)
}

// A blob GET that dies mid-body fails the pass honestly — cursor and inbox
// rows are already durable — installs nothing, and the next healthy pass
// completes the install. A crashed install's leftover temp file is debris
// the reaper collects, never mistaken for content.
func TestSharedStoreContentBlobDownloadInterrupted(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
	bb := exchangeBind(t, sb, b, endpoint)
	blobDir := filepath.Join(filepath.Dir(sb.Path()), "blobs")

	n, brief := labSpilledBrief(t, sa, a.repo, "Truncated Get", 31)
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	exchangePass(t, sa, ca, ba, birth, content)
	exchangePass(t, sb, cb, bb) // retains the changes; the group pends

	feed.truncateBlobGets(true)
	err := sb.ExchangePass(labCtx, cb, bb, nil)
	feed.truncateBlobGets(false)
	if err == nil {
		t.Fatal("truncated blob GET did not fail the pass")
	}
	d := sourceDecision(t, sb, content, "pending")
	p := labContentPayload(t, d.Source, 0)
	if _, err := os.Stat(filepath.Join(blobDir, p.BlobRef)); !os.IsNotExist(err) {
		t.Fatal("truncated download installed bytes")
	}

	// The temp-file half of the install crash window: a stale
	// .exchange-blob-* file is an unreferenced orphan, collected by the
	// reaper without touching the pending group's (absent) blob.
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, ".exchange-blob-crashed"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	reaped, err := sb.ReapOrphanBlobs(labCtx, time.Now())
	if err != nil || !slices.Contains(reaped, ".exchange-blob-crashed") {
		t.Fatalf("stale installer temp not reaped: %v %v", reaped, err)
	}

	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content, "integrated")
	segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 {
		t.Fatalf("receiver brief segments = %+v %v", segs, err)
	}
	got, err := sb.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief bytes = %d %v", len(got), err)
	}
	// A completed install leaves no staging residue behind.
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".exchange-blob-") {
			t.Fatalf("installer temp residue %s", e.Name())
		}
	}
	labRebuild(t, sb)
}

// Verified bytes and the local effect commit are separate durable steps: an
// apply crash after the blob install leaves the group pending with its bytes
// already in place, and a reopened store completes it exactly once.
func TestSharedStoreContentBlobApplyCrash(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
	bb := exchangeBind(t, sb, b, endpoint)

	n, brief := labSpilledBrief(t, sa, a.repo, "Apply Crash", 37)
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	exchangePass(t, sa, ca, ba, birth, content)
	exchangePass(t, sb, cb, bb) // retains; the spilled group pends

	// The fault fires inside ApplyExchangeInbox's effect insert — after this
	// pass's blob fetch already installed verified bytes.
	db, err := sql.Open("sqlite", b.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TRIGGER reject_effect BEFORE INSERT ON events
		WHEN NEW.actor='system:shared-store-experiment' BEGIN SELECT RAISE(ABORT,'injected integration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := sb.ExchangePass(labCtx, cb, bb, nil); err == nil {
		t.Fatal("apply fault did not fire")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_effect`); err != nil {
		t.Fatal(err)
	}
	d := sourceDecision(t, sb, content, "pending")
	p := labContentPayload(t, d.Source, 0)
	got, err := os.ReadFile(filepath.Join(filepath.Dir(sb.Path()), "blobs", p.BlobRef))
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatal("verified blob did not survive the apply crash")
	}

	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
	sb = labOpen(t, b.db)
	// Offline completion needs no server: the durable inbox rechecks the
	// pending group against the already-installed bytes.
	if err := sb.ApplyExchangeInbox(labCtx, bb); err != nil {
		t.Fatal(err)
	}
	sourceDecision(t, sb, content, "integrated")
	segs, err := sb.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 {
		t.Fatalf("receiver brief segments = %+v %v", segs, err)
	}
	before := len(labEvents(t, sb))
	if err := sb.ApplyExchangeInbox(labCtx, bb); err != nil {
		t.Fatal(err)
	}
	if len(labEvents(t, sb)) != before {
		t.Fatal("inbox replay duplicated effects")
	}
	got, err = sb.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief bytes = %d %v", len(got), err)
	}
	labRebuild(t, sb)
}

// Missing or corrupt source bytes are reported honestly and never published:
// the cached envelope still names the blob, but SegmentBytes cannot serve it,
// so the pass fails before any PUT or changes POST — and a restored file
// completes the same group on retry.
func TestSharedStoreContentBlobMissingSource(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
	bb := exchangeBind(t, sb, b, endpoint)

	n, brief := labSpilledBrief(t, sa, a.repo, "Missing Source", 41)
	birth := sourceGroup(t, sa, n.ID, store.TypeMatterCreated)
	content := sourceGroup(t, sa, n.ID, store.TypeContentCreated)
	exchangePass(t, sa, ca, ba, birth)
	segs, err := sa.ContentSegments(labCtx, n.ID, store.KindBrief)
	if err != nil || len(segs) != 1 {
		t.Fatalf("sender brief segments = %+v %v", segs, err)
	}
	blobPath := filepath.Join(filepath.Dir(sa.Path()), "blobs", segs[0].ID)
	digest := labDigest(brief)

	for _, corrupt := range []bool{false, true} {
		if corrupt {
			if err := os.WriteFile(blobPath, []byte("corrupt source"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(blobPath); err != nil {
			t.Fatal(err)
		}
		if err := sa.ExchangePass(labCtx, ca, ba, []string{content}); err == nil {
			t.Fatalf("corrupt=%v: unservable source bytes did not fail the pass", corrupt)
		}
		if feed.hasRequest("PUT", "/blobs/"+digest) {
			t.Fatal("blob PUT attempted without verifiable source bytes")
		}
		if slices.Contains(feed.changeOrder(), content) {
			t.Fatal("change published without its blob")
		}
	}
	if err := os.WriteFile(blobPath, brief, 0o600); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sa, ca, ba, content)
	if got, ok := feed.blob(digest); !ok || !bytes.Equal(got, brief) {
		t.Fatal("restored source bytes uploaded wrong")
	}
	exchangePass(t, sb, cb, bb)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, content, "integrated")
	got, err := sb.Content(labCtx, n.ID, store.KindBrief)
	if err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief = %d bytes %v", len(got), err)
	}
	labRebuild(t, sb)
}

// Wrong recorded metadata never installs: a spilled claim whose byte_len
// contradicts the served bytes, and one whose sha256 names different bytes,
// both stay pending with inspectable evidence — the group is never projected
// onto an unreadable reference.
func TestSharedStoreContentBlobMetadataMismatch(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	status, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba := status.Binding
	bb := exchangeBind(t, sb, b, endpoint)

	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Meta", "meta")
	if err != nil {
		t.Fatal(err)
	}
	birth := sourceGroup(t, sa, matter.ID, store.TypeMatterCreated)
	exchangePass(t, sa, ca, ba, birth)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, birth, "integrated")
	base, err := sa.ExperimentExport(labCtx, ba.InstallationID, birth)
	if err != nil {
		t.Fatal(err)
	}

	post := func(src store.ExperimentSource) string {
		t.Helper()
		raw, err := json.Marshal(src)
		if err != nil {
			t.Fatal(err)
		}
		change := store.ExchangeChange{
			Version: 1, InstallationID: ba.InstallationID,
			GroupID: src.Actions[0].Event.ID, SourceRepoID: a.repo, Source: string(raw),
		}
		fixtureHTTP(t, "POST", endpoint+"/changes", "token",
			map[string]any{"changes": []store.ExchangeChange{change}}, http.StatusNoContent)
		return change.GroupID
	}
	blobDir := filepath.Join(filepath.Dir(sb.Path()), "blobs")

	// Length lie: the payload records one more byte than the blob holds.
	data := labFillBytes(store.SpillThreshold+64, 13)
	lengthLie := labContentClaim(t, sa, base, store.TypeContentCreated, store.KindBrief,
		matter.ID, birth, data, true)
	lp := labContentPayload(t, lengthLie, 0)
	lp.ByteLen++
	payload, err := json.Marshal(lp)
	if err != nil {
		t.Fatal(err)
	}
	lengthLie.Actions[0].Event.Payload = payload
	lengthGroup := post(lengthLie)
	feed.setBlob(lp.SHA256, data)

	// Digest lie: the payload names bytes that are not what the route serves.
	other := labFillBytes(4096, 17)
	otherDigest := labDigest(other)
	digestLie := labContentClaim(t, sa, base, store.TypeContentCreated, store.KindBody,
		matter.ID, birth, data, true)
	dp := labContentPayload(t, digestLie, 0)
	dp.SHA256 = otherDigest
	payload, err = json.Marshal(dp)
	if err != nil {
		t.Fatal(err)
	}
	digestLie.Actions[0].Event.Payload = payload
	digestGroup := post(digestLie)
	feed.setBlob(otherDigest, data) // served bytes hash to lp.SHA256, not otherDigest

	for range 2 {
		exchangePass(t, sb, cb, bb)
	}
	for _, group := range []string{lengthGroup, digestGroup} {
		d := sourceDecision(t, sb, group, "pending")
		if d.Reason != "content bytes unavailable" {
			t.Fatalf("%s pending reason = %q", group, d.Reason)
		}
	}
	// Neither claim projected a content row or installed sidecar bytes.
	for _, ref := range []string{lp.BlobRef, dp.BlobRef} {
		if _, err := os.Stat(filepath.Join(blobDir, ref)); !os.IsNotExist(err) {
			t.Fatalf("unverified bytes installed at %s", ref)
		}
	}
	if segs, err := sb.ContentSegments(labCtx, matter.ID, store.KindBrief); err != nil || len(segs) != 0 {
		t.Fatalf("length-lie brief projected: %+v %v", segs, err)
	}
	labRebuild(t, sb)
}

// Content has no authoring ceiling — only the spill floor — so a Brief can
// exceed the blob route's whole-object cap. Such content is simply outside
// the admitted portable set: the group stays local evidence, never enters
// PendingUpload, and cannot wedge the groups queued behind it. Explicit
// upload cannot bypass that boundary, even as non-applying evidence.
func TestSharedStoreContentOversizeStaysLocal(t *testing.T) {
	feed, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 60 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 60 * time.Second}}
	status, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba := status.Binding
	bb := exchangeBind(t, sb, b, endpoint)

	big, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Too Big", "too-big")
	if err != nil {
		t.Fatal(err)
	}
	oversize := labFillBytes(labBlobMaxBytes+1, 19)
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, big.Locator, store.KindBrief, oversize); err != nil {
		t.Fatal(err)
	}
	oversizeGroup := sourceGroup(t, sa, big.ID, store.TypeContentCreated)
	normal, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Normal", "normal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, normal.Locator, store.KindBrief, []byte("fine")); err != nil {
		t.Fatal(err)
	}
	normalGroup := sourceGroup(t, sa, normal.ID, store.TypeContentCreated)

	done, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(done.PendingUpload, oversizeGroup) {
		t.Fatal("oversize group entered the upload queue")
	}
	if !slices.Contains(done.Unsupported, oversizeGroup) {
		t.Fatalf("oversize group not retained as unsupported: %+v", done)
	}
	if !slices.Contains(feed.changeOrder(), normalGroup) || slices.Contains(feed.changeOrder(), oversizeGroup) {
		t.Fatal("feed carries the wrong groups")
	}

	// Explicit invocation must not serialize an excluded raw envelope either.
	if err := sa.ExchangePass(labCtx, ca, ba, []string{oversizeGroup}); err == nil || !strings.Contains(err.Error(), "no portable unit") {
		t.Fatal("explicit upload bypassed the portable boundary", err)
	}
	if slices.Contains(feed.changeOrder(), oversizeGroup) {
		t.Fatal("excluded raw envelope reached the server")
	}
	if feed.hasRequest("PUT", "/blobs/") {
		t.Fatal("oversize blob PUT attempted")
	}
	exchangePass(t, sb, cb, bb)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, normalGroup, "integrated")
	got, err := sb.Content(labCtx, normal.ID, store.KindBrief)
	if err != nil || string(got) != "fine" {
		t.Fatalf("unrelated work blocked by oversize group: %q %v", got, err)
	}
	// The oversize bytes stay readable locally on the sender.
	segs, err := sa.ContentSegments(labCtx, big.ID, store.KindBrief)
	if err != nil || len(segs) != 1 || !segs[0].Spilled {
		t.Fatalf("sender oversize segments = %+v %v", segs, err)
	}
	back, err := sa.SegmentBytes(labCtx, segs[0].ID)
	if err != nil || !bytes.Equal(back, oversize) {
		t.Fatalf("sender oversize bytes = %d %v", len(back), err)
	}
	labRebuild(t, sb)
}

// Transfer-size measurements through the in-process feed: PUT and
// GET+verify timings for representative spilled payloads. These are
// loopback numbers — real transport adds network and PHP runtime cost —
// but they bound the work the client performs per blob.
func TestSharedStoreContentBlobSizing(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	c := store.ExchangeClient{HTTP: &http.Client{Timeout: 60 * time.Second}}
	for _, size := range []int{1 << 20, 4 << 20, 8 << 20} {
		data := labFillBytes(size, byte(size>>20))
		digest := labDigest(data)
		start := time.Now()
		if err := c.UploadBlob(labCtx, endpoint, digest, int64(size), bytes.NewReader(data)); err != nil {
			t.Fatalf("PUT %d bytes: %v", size, err)
		}
		put := time.Since(start)
		start = time.Now()
		body, err := c.FetchBlob(labCtx, endpoint, digest)
		if err != nil {
			t.Fatalf("GET %d bytes: %v", size, err)
		}
		got, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("GET %d bytes verify: %v", size, err)
		}
		get := time.Since(start)
		t.Logf("in-process feed blob %d B: PUT %v, GET+read %v", size, put, get)
	}
}

// Blob routes are member-token scoped by design — bytes carry no origin
// authority, so a revoked publisher loses the changes channel while blob
// bytes stay member-readable/writable; the receiver only ever installs bytes
// a verified pending payload names. Outsiders and wrong-length/wrong-digest
// writes get the same indistinguishable refusals Cloud gives. Real
// PUT/GET timings through the Laravel fixture bound the transport cost.
func TestLaravelExchangeBlobAccessAndSizing(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ba := exchangeBind(t, sa, a, f.endpoint())
	bb := exchangeBind(t, sb, b, f.endpoint())
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 60 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 60 * time.Second}, Token: f.Member}

	// Register the sender's origin so revocation has something to revoke.
	fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Owner,
		map[string]string{"installation_id": ba.InstallationID}, http.StatusOK)

	data := []byte("publisher boundary bytes")
	digest := labDigest(data)
	if err := cb.UploadBlob(labCtx, f.endpoint(), digest, int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// Outsider sees the same 404 for present and absent digests; a member's
	// wrong declared length is refused before the body is trusted.
	fixtureHTTP(t, "GET", f.endpoint()+"/blobs/"+digest, f.Outsider, nil, http.StatusNotFound)
	fixtureHTTP(t, "GET", f.endpoint()+"/blobs/"+labDigest([]byte("absent")), f.Outsider, nil, http.StatusNotFound)
	rawPut := func(url, token string, body []byte, want int) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != want {
			t.Fatalf("PUT %s -> %d, want %d", url, resp.StatusCode, want)
		}
	}
	// A fresh digest with a wrong declared length is refused 422 before the
	// body is trusted; an outsider's write is the indistinguishable 404.
	freshDigest := labDigest([]byte("never stored"))
	rawPut(f.endpoint()+"/blobs/"+freshDigest+"?length="+strconv.Itoa(len(data)+1), f.Member, data, http.StatusUnprocessableEntity)
	rawPut(f.endpoint()+"/blobs/"+digest+"?length="+strconv.Itoa(len(data)), f.Outsider, data, http.StatusNotFound)

	// Revoke the origin: the changes channel refuses it, blob bytes do not.
	fixtureHTTP(t, "DELETE", f.endpoint()+"/origins/"+ba.InstallationID, f.Owner, nil, http.StatusOK)
	if err := sa.ExchangePass(labCtx, ca, ba, nil); err == nil {
		t.Fatal("revoked origin synchronized changes")
	}
	body, err := cb.FetchBlob(labCtx, f.endpoint(), digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("member blob read after revocation: %v", err)
	}
	more := []byte("still writable bytes")
	if err := cb.UploadBlob(labCtx, f.endpoint(), labDigest(more), int64(len(more)), bytes.NewReader(more)); err != nil {
		t.Fatal("member blob write after revocation:", err)
	}

	// Real transport sizing: PUT and GET+verify through PHP/Laravel.
	for _, size := range []int{1 << 20, 4 << 20} {
		blob := labFillBytes(size, byte(size>>20))
		d := labDigest(blob)
		start := time.Now()
		if err := cb.UploadBlob(labCtx, f.endpoint(), d, int64(size), bytes.NewReader(blob)); err != nil {
			t.Fatalf("Laravel PUT %d: %v", size, err)
		}
		put := time.Since(start)
		start = time.Now()
		body, err := cb.FetchBlob(labCtx, f.endpoint(), d)
		if err != nil {
			t.Fatalf("Laravel GET %d: %v", size, err)
		}
		got, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || !bytes.Equal(got, blob) {
			t.Fatalf("Laravel GET %d verify: %v", size, err)
		}
		get := time.Since(start)
		t.Logf("Laravel blob %d B: PUT %v, GET+verify %v", size, put, get)
	}
	_ = bb
}
