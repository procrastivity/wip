//go:build sharedstoreexperiment

package store_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
