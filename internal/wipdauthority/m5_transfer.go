package wipdauthority

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (app *m5LabHandler) serveTransferExchange(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame, kind string, start authoritystore.PrefixAnchor) {
	ctx, cancel, control, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	product, err := app.transferProduct(ctx, kind, start, time.Now().UTC())
	if readOnlyExchangeStopped(ctx, writer, frame.RequestID, control) {
		return
	}
	if err != nil {
		code := "transfer.incomplete"
		if errors.Is(err, authoritystore.ErrPrefixMismatch) {
			code = "transfer.prefix-mismatch"
		}
		writeLabProblem(writer, frame.RequestID, code)
		return
	}
	_ = writeLabFrames(writer, frame.RequestID, product)
}

func (app *m5LabHandler) transferProduct(ctx context.Context, kind string, start authoritystore.PrefixAnchor, now time.Time) ([]labFrameRecord, error) {
	current, err := app.store.CurrentPrefixAnchor(ctx, app.profile.domainID)
	if err != nil {
		return nil, err
	}
	if start.EventCount > current.EventCount {
		return nil, authoritystore.ErrPrefixMismatch
	}
	if current.EventCount-start.EventCount > labMaxTransferEvents {
		return nil, authoritystore.ErrResourceLimit
	}
	transferID, err := randomULID(now)
	if err != nil {
		return nil, err
	}
	snapshotID, err := randomULID(now)
	if err != nil {
		return nil, err
	}
	transfer, err := app.store.StartTransfer(ctx, app.profile.domainID, app.profile.epoch, kind, "wipd.store/1", start, snapshotID, transferID, now)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = app.store.ReleaseSnapshot(context.Background(), app.profile.domainID, app.profile.epoch, transfer.SnapshotID)
	}()
	if transfer.EventCount > labMaxTransferEvents || transfer.EventByteLength > labMaxTransferBytes ||
		transfer.EventCount != uint64(len(transfer.Snapshot.Delta.Events)) || len(transfer.Snapshot.Manifest.Entries) > labMaxManifestEntries {
		return nil, authoritystore.ErrResourceLimit
	}
	if transfer.Snapshot.Delta.Start != start || transfer.Snapshot.Delta.End != current || transfer.Snapshot.Manifest.AsOf != current {
		return nil, authoritystore.ErrPrefixMismatch
	}
	startAnchor := wireAnchor(transfer.Snapshot.Delta.Start)
	endAnchor := wireAnchor(transfer.Snapshot.Delta.End)
	var startRecord any
	switch kind {
	case "seed":
		startRecord = wipdwire.SeedStart{
			Schema: "wipd.seed-start/1", TransferID: transfer.ID, DomainID: app.profile.domainID,
			Epoch: app.profile.epoch, StoreSchema: "wipd.store/1", SnapshotID: transfer.SnapshotID,
			Prefix: struct {
				Start wipdwire.PrefixAnchor `cbor:"start"`
				End   wipdwire.PrefixAnchor `cbor:"end"`
			}{Start: startAnchor, End: endAnchor},
			EventCount: transfer.EventCount, EventByteLength: transfer.EventByteLength,
			ManifestDigest: transfer.Snapshot.Manifest.Digest,
		}
	case "pull":
		startRecord = wipdwire.PullStart{
			Schema: "wipd.pull-start/1", TransferID: transfer.ID, DomainID: app.profile.domainID,
			Epoch: app.profile.epoch,
			Prefix: struct {
				Start wipdwire.PrefixAnchor `cbor:"start"`
				End   wipdwire.PrefixAnchor `cbor:"end"`
			}{Start: startAnchor, End: endAnchor},
			EventCount: transfer.EventCount, EventByteLength: transfer.EventByteLength,
			ManifestDigest: transfer.Snapshot.Manifest.Digest,
		}
	default:
		return nil, errors.New("unsupported lab transfer kind")
	}
	product := make([]labFrameRecord, 0, len(transfer.Snapshot.Delta.Events)+3)
	startPayload, err := wipdwire.EncodeCanonical(startRecord)
	if err != nil {
		return nil, err
	}
	startKind := "seed.start"
	if kind == "pull" {
		startKind = "pull.start"
	}
	product = append(product, labFrameRecord{kind: startKind, payload: startPayload})
	var totalEventBytes uint64
	for _, event := range transfer.Snapshot.Delta.Events {
		if uint64(len(event.Record)) > labMaxTransferBytes-totalEventBytes {
			return nil, authoritystore.ErrResourceLimit
		}
		totalEventBytes += uint64(len(event.Record))
		payload, encodeErr := wipdwire.EncodeCanonical(wipdwire.EventRecord{EventID: event.EventID, Record: event.Record})
		if encodeErr != nil {
			return nil, encodeErr
		}
		product = append(product, labFrameRecord{kind: "event.record", payload: payload})
	}
	if totalEventBytes != transfer.EventByteLength {
		return nil, authoritystore.ErrInvalidStore
	}
	entries := make([]wipdwire.BlobManifestEntry, 0, len(transfer.Snapshot.Manifest.Entries))
	for _, entry := range transfer.Snapshot.Manifest.Entries {
		entries = append(entries, wipdwire.BlobManifestEntry{Digest: entry.Digest, ByteLength: entry.ByteLength, Requirement: entry.Requirement})
	}
	manifest := wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: transfer.Snapshot.Manifest.DomainID,
		Epoch: transfer.Snapshot.Manifest.Epoch, AsOf: wireAnchor(transfer.Snapshot.Manifest.AsOf),
		Entries: entries, Digest: transfer.Snapshot.Manifest.Digest,
	}
	manifestPayload, err := wipdwire.EncodeCanonical(manifest)
	if err != nil {
		return nil, err
	}
	product = append(product, labFrameRecord{kind: "blob.manifest", payload: manifestPayload})
	if kind == "seed" {
		end := wipdwire.SeedEnd{
			Schema: "wipd.seed-end/1", TransferID: transfer.ID,
			VerifiedPrefix: endAnchor, ManifestDigest: transfer.Snapshot.Manifest.Digest, Complete: true,
		}
		payload, encodeErr := wipdwire.EncodeCanonical(end)
		if encodeErr != nil {
			return nil, encodeErr
		}
		product = append(product, labFrameRecord{kind: "seed.end", payload: payload})
	} else {
		end := wipdwire.PullEnd{
			TransferID: transfer.ID, VerifiedPrefix: endAnchor,
			ManifestDigest: transfer.Snapshot.Manifest.Digest, Complete: true,
		}
		payload, encodeErr := wipdwire.EncodeCanonical(end)
		if encodeErr != nil {
			return nil, encodeErr
		}
		product = append(product, labFrameRecord{kind: "pull.end", payload: payload})
	}
	return product, nil
}

func authorityAnchor(anchor wipdwire.PrefixAnchor) authoritystore.PrefixAnchor {
	result := authoritystore.PrefixAnchor{EventCount: anchor.EventCount, Digest: anchor.Digest}
	if anchor.EventID != nil {
		result.EventID = *anchor.EventID
	}
	return result
}

func writeLabFrames(writer http.ResponseWriter, requestID string, records []labFrameRecord) error {
	frames := make([][]byte, 0, len(records))
	var total int
	for sequence, record := range records {
		wire, err := wipdwire.EncodeFrame(wipdwire.Frame{
			RequestID: requestID, Sequence: uint64(sequence), Kind: record.kind, Payload: record.payload,
		})
		if err != nil || len(wire) > maxLabResponseBytes || total > labMaxTransferBytes-len(wire) {
			return authoritystore.ErrResourceLimit
		}
		total += len(wire)
		frames = append(frames, wire)
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	for _, wire := range frames {
		if _, err := writer.Write(wire); err != nil {
			return err
		}
	}
	return nil
}
