package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// ContinuityBinding carries the destination identity authenticated by the
// caller's transport. The store compares every signed field to this binding;
// it never accepts a detached verified=true assertion.
type ContinuityBinding struct {
	SourceAuthoritySPKI      string
	DestinationOrigin        string
	DestinationAuthoritySPKI string
	DestinationArtifactKeyID string
}

// ContinuityProducts returns the exact immutable source handoff artifacts.
type ContinuityProducts struct {
	BundleManifest, Relinquishment     []byte
	BundleDigest, RelinquishmentDigest string
	Prefix                             PrefixAnchor
}

// ActivationProduct is the exact persisted next-epoch activation wrapper.
type ActivationProduct struct {
	Artifact []byte
	Digest   string
	Epoch    uint64
	Prefix   PrefixAnchor
}

type ownerAttestation struct {
	Schema        string  `cbor:"schema"`
	Action        string  `cbor:"action"`
	Domain        string  `cbor:"domain_id"`
	Current       uint64  `cbor:"current_epoch"`
	Next          *uint64 `cbor:"next_epoch"`
	SubjectSchema string  `cbor:"subject_schema"`
	SubjectDigest string  `cbor:"subject_digest"`
	Subject       []byte  `cbor:"subject"`
	Nonce         []byte  `cbor:"nonce"`
	Issued        string  `cbor:"issued_at"`
	Expires       string  `cbor:"expires_at"`
	Loss          bool    `cbor:"loss_accepted"`
}

func verifyContinuityBinding(b ContinuityBinding) error {
	if !validDigest(b.SourceAuthoritySPKI) || !validDigest(b.DestinationAuthoritySPKI) || !validDigest(b.DestinationArtifactKeyID) {
		return ErrInvalidProof
	}
	u, err := url.Parse(b.DestinationOrigin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return ErrInvalidProof
	}
	return nil
}

func ownerAttestationFor(ctx context.Context, tx *sql.Tx, raw []byte, domain string, epoch uint64, action string, at time.Time) (ownerAttestation, []byte, error) {
	var a ownerAttestation
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return a, nil, err
	}
	payload, err := ownerArtifact(raw, d.OwnerPublicKey, domain, d.OwnerKeyID, "owner-attestation", "wipd.owner-attestation/1", epoch)
	if err != nil {
		return a, nil, ErrInvalidProof
	}
	if err = closedPayload(payload, &a, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted"); err != nil {
		return a, nil, ErrInvalidProof
	}
	if a.Schema != "wipd.owner-attestation/1" || a.Action != action || a.Domain != domain || a.Current != epoch || len(a.Nonce) != 16 || a.SubjectDigest != digestBytes(a.Subject) || interval(a.Issued, a.Expires, at.UTC()) != nil {
		return a, nil, ErrInvalidProof
	}
	issued, _ := utcTime(a.Issued)
	expires, _ := utcTime(a.Expires)
	if expires.Sub(issued) > 10*time.Minute {
		return a, nil, ErrInvalidProof
	}
	return a, payload, nil
}

func ownerNonceExists(ctx context.Context, tx *sql.Tx, domain, nonce string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM owner_nonce_uses WHERE domain_id=? AND nonce=?) + (SELECT count(*) FROM claim_stand_down_proofs WHERE domain_id=? AND owner_nonce=?)`, domain, nonce, domain, nonce).Scan(&n)
	return n != 0, err
}

func checkWriteAdmission(ctx context.Context, tx *sql.Tx, domain string, epoch uint64) error {
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return err
	}
	if d.ActiveEpoch != epoch {
		return ErrFenced
	}
	var closed int
	var closedEpoch sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT admission_closed,closed_epoch FROM authority_continuity WHERE domain_id=?`, domain).Scan(&closed, &closedEpoch); err != nil {
		return err
	}
	if closed != 0 && closedEpoch.Valid && uint64(closedEpoch.Int64) == d.ActiveEpoch {
		return ErrFenced
	}
	var migrationPending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM continuity_products a WHERE a.domain_id=? AND a.authority_epoch=? AND a.kind='migration-authorization' AND NOT EXISTS(SELECT 1 FROM continuity_products z WHERE z.domain_id=a.domain_id AND z.authority_epoch=a.authority_epoch AND z.kind='migration-seal')`, domain, d.ActiveEpoch).Scan(&migrationPending); err != nil {
		return err
	}
	if migrationPending != 0 {
		return ErrFenced
	}
	return nil
}

func consumeOwnerNonce(ctx context.Context, tx *sql.Tx, domain, nonce, action string, raw []byte, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_nonce_uses(domain_id,nonce,action,artifact_digest,consumed_at) VALUES(?,?,?,?,?)`, domain, nonce, action, digestBytes(raw), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return ErrFenced
	}
	return nil
}

type bundleEntry struct {
	Kind        string `cbor:"kind"`
	LogicalName string `cbor:"logical_name"`
	ByteLength  uint64 `cbor:"byte_length"`
	Digest      string `cbor:"digest"`
	RecordCount uint64 `cbor:"record_count"`
}

type bundlePayload struct {
	Schema      string `cbor:"schema"`
	Domain      string `cbor:"domain_id"`
	Epoch       uint64 `cbor:"authority_epoch"`
	StoreSchema string `cbor:"store_schema"`
	Prefix      struct {
		Count   uint64  `cbor:"event_count"`
		EventID *string `cbor:"high_water_event_id"`
		Digest  string  `cbor:"prefix_digest"`
	} `cbor:"prefix"`
	BlobManifestDigest string        `cbor:"blob_manifest_digest"`
	ArtifactHead       *string       `cbor:"artifact_chain_head"`
	Entries            []bundleEntry `cbor:"entries"`
}

func currentArtifactHead(ctx context.Context, tx *sql.Tx, domain string, epoch uint64) (*string, error) {
	var head sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT a.digest FROM authority_artifacts a WHERE a.domain_id=? AND a.epoch=? AND a.generation=(SELECT max(generation) FROM artifact_keys WHERE domain_id=? AND epoch=?) ORDER BY a.sequence DESC LIMIT 1`, domain, epoch, domain, epoch).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &head.String, nil
}

var bundleTables = []string{
	"domains", "repo_memberships", "artifact_keys", "authority_artifacts", "environment_cas", "environments", "environment_certificates", "enrollment_consumptions", "environment_renewals",
	"submissions", "terminal_receipts", "authority_events", "matters", "blob_products", "blob_chunks", "blob_references",
	"claim_acquire_intents", "claim_stand_down_proofs", "anonymous_batches", "claims", "claim_grants", "claim_journals", "claim_journal_entries", "claim_closes",
	"owner_nonce_uses", "continuity_products",
}

var bundleTableKinds = map[string]string{
	"domains": "canonical-db", "repo_memberships": "environment-registry",
	"artifact_keys": "artifact-ledger", "authority_artifacts": "artifact-ledger",
	"environment_cas": "environment-registry", "environments": "environment-registry", "environment_certificates": "environment-registry",
	"enrollment_consumptions": "environment-registry", "environment_renewals": "environment-registry",
	"submissions": "receipt-ledger", "terminal_receipts": "receipt-ledger", "authority_events": "receipt-ledger", "matters": "canonical-db",
	"blob_products": "canonical-db", "blob_chunks": "canonical-db", "blob_references": "canonical-db",
	"claim_acquire_intents": "claim-registry", "claim_stand_down_proofs": "claim-registry", "anonymous_batches": "claim-registry",
	"claims": "claim-registry", "claim_grants": "claim-registry", "claim_journals": "claim-registry", "claim_journal_entries": "claim-registry", "claim_closes": "claim-registry",
	"owner_nonce_uses": "artifact-ledger", "continuity_products": "artifact-ledger",
	"referenced-blob-bytes": "blob",
}

func tableSection(ctx context.Context, tx *sql.Tx, table, domain string, epoch uint64) ([]byte, uint64, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
		return nil, 0, err
	}
	if exists == 0 {
		return nil, 0, ErrInvalidStore
	}
	query := `SELECT * FROM "` + table + `" WHERE domain_id=?`
	args := []any{domain}
	if table == "authority_artifacts" {
		query += ` AND digest NOT IN (SELECT digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind IN ('bundle-manifest','authority-relinquishment'))`
		args = append(args, domain, epoch)
	}
	if table == "continuity_products" {
		query += ` AND (authority_epoch!=? OR kind NOT IN ('activation-intent','bundle-manifest','authority-relinquishment','authority-activation'))`
		args = append(args, epoch)
	}
	if table == "owner_nonce_uses" {
		var currentIntent []byte
		if err := tx.QueryRowContext(ctx, `SELECT artifact FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='activation-intent'`, domain, epoch).Scan(&currentIntent); err == nil {
			var wrapper signedArtifact
			if artifactDecoder.Unmarshal(currentIntent, &wrapper) == nil {
				if payload, e := ownerArtifact(currentIntent, func() ed25519.PublicKey {
					var key []byte
					_ = tx.QueryRowContext(ctx, `SELECT owner_public_key FROM domains WHERE domain_id=?`, domain).Scan(&key)
					return ed25519.PublicKey(key)
				}(), domain, func() string {
					var id string
					_ = tx.QueryRowContext(ctx, `SELECT owner_key_id FROM domains WHERE domain_id=?`, domain).Scan(&id)
					return id
				}(), "owner-attestation", "wipd.owner-attestation/1", wrapper.Epoch); e == nil {
					var intent ownerAttestation
					if closedPayload(payload, &intent, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") == nil && len(intent.Nonce) == 16 {
						query += ` AND nonce!=?`
						args = append(args, fmt.Sprintf("%x", intent.Nonce))
					}
				}
			}
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	cols, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, 0, err
	}
	var records [][]byte
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			break
		}
		record := make(map[string]any, len(cols))
		for i, name := range cols {
			switch v := values[i].(type) {
			case int64:
				record[name] = uint64(v)
			case []byte:
				record[name] = bytes.Clone(v)
			default:
				record[name] = v
			}
		}
		encoded, e := artifactEncoder.Marshal(record)
		if e != nil {
			_ = rows.Close()
			return nil, 0, e
		}
		records = append(records, encoded)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(records, func(i, j int) bool { return bytes.Compare(records[i], records[j]) < 0 })
	raw, err := artifactEncoder.Marshal(records)
	return raw, uint64(len(records)), err
}

func bundleClosure(ctx context.Context, tx *sql.Tx, storeRoot, domain string, epoch uint64) (PrefixAnchor, string, []bundleEntry, error) {
	var active uint64
	if err := tx.QueryRowContext(ctx, `SELECT active_epoch FROM domains WHERE domain_id=?`, domain).Scan(&active); err != nil {
		return PrefixAnchor{}, "", nil, err
	}
	if active != epoch {
		return PrefixAnchor{}, "", nil, ErrFenced
	}
	prefix, err := currentAnchor(ctx, tx, domain)
	if err != nil {
		return PrefixAnchor{}, "", nil, err
	}
	entries := make([]bundleEntry, 0, len(bundleTables)+1)
	for _, table := range bundleTables {
		raw, count, e := tableSection(ctx, tx, table, domain, epoch)
		if e != nil {
			return PrefixAnchor{}, "", nil, e
		}
		entries = append(entries, bundleEntry{
			Kind: bundleTableKinds[table], LogicalName: table, ByteLength: uint64(len(raw)), Digest: digestBytes(raw), RecordCount: count,
		})
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.digest,p.byte_length FROM blob_references r JOIN blob_products p USING(domain_id,digest) WHERE r.domain_id=? AND r.first_position<=? ORDER BY r.digest`, domain, prefix.EventCount)
	if err != nil {
		return PrefixAnchor{}, "", nil, err
	}
	type blob struct {
		digest string
		length uint64
	}
	var blobs []blob
	for rows.Next() {
		var b blob
		if err = rows.Scan(&b.digest, &b.length); err != nil {
			break
		}
		blobs = append(blobs, b)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return PrefixAnchor{}, "", nil, err
	}
	manifest := make([]BlobManifestEntry, 0, len(blobs))
	blobBytes := make([]any, 0, len(blobs))
	for _, b := range blobs {
		var verified int
		if err = tx.QueryRowContext(ctx, `SELECT verified FROM blob_products WHERE domain_id=? AND digest=?`, domain, b.digest).Scan(&verified); err != nil || verified != 1 {
			return PrefixAnchor{}, "", nil, ErrBlobAbsent
		}
		if err = verifyProduct(filepath.Join(storeRoot, "blobs"), b.digest, b.length); err != nil {
			return PrefixAnchor{}, "", nil, err
		}
		data, e := os.ReadFile(filepath.Join(storeRoot, "blobs", productName(b.digest)))
		if e != nil {
			return PrefixAnchor{}, "", nil, e
		}
		manifest = append(manifest, BlobManifestEntry{Digest: b.digest, ByteLength: b.length, Requirement: "lazy"})
		blobBytes = append(blobBytes, map[string]any{"digest": b.digest, "byte_length": b.length, "bytes": data})
	}
	blobDigest, err := manifestChain(manifest)
	if err != nil {
		return PrefixAnchor{}, "", nil, err
	}
	raw, err := artifactEncoder.Marshal(blobBytes)
	if err != nil {
		return PrefixAnchor{}, "", nil, err
	}
	entries = append(entries, bundleEntry{
		Kind: "blob", LogicalName: "referenced-blob-bytes", ByteLength: uint64(len(raw)), Digest: digestBytes(raw), RecordCount: uint64(len(blobBytes)),
	})
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].LogicalName < entries[j].LogicalName
	})
	return prefix, blobDigest, entries, nil
}

func addContinuityProduct(ctx context.Context, tx *sql.Tx, domain, kind string, raw []byte, digest string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO continuity_products(domain_id,kind,authority_epoch,artifact,digest) SELECT ?,?,active_epoch,?,? FROM domains WHERE domain_id=?`, domain, kind, raw, digest, domain)
	if err != nil {
		return ErrFenced
	}
	return nil
}

// QuiesceAndRelinquish verifies a closed owner intent and atomically closes
// source admission with the exact signed bundle/relinquishment products.
func (s *Store) QuiesceAndRelinquish(ctx context.Context, domain string, intent []byte, binding ContinuityBinding, at time.Time, sign Signer) (ContinuityProducts, error) {
	var out ContinuityProducts
	if !ulid.MatchString(domain) || at.IsZero() || sign == nil || verifyContinuityBinding(binding) != nil {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, ErrInvalidStore
	}
	if err := checkStep3State(s.db); err != nil {
		return out, err
	}
	if err := checkStep4State(s.db); err != nil {
		return out, err
	}
	if err := checkStep6State(s.db); err != nil {
		return out, err
	}
	if err := checkStep5State(s.db); err != nil {
		return out, err
	}
	if err := checkStep7State(s.db); err != nil {
		return out, err
	}
	if err := checkStep7Closure(s.db, filepath.Dir(s.blobs)); err != nil {
		return out, err
	}
	if err := checkBlobFiles(s.db, s.blobs); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	var priorIntent, priorBundle, priorRel []byte
	var priorIntentDigest, priorBundleDigest, priorRelDigest string
	intentErr := tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='activation-intent'`, domain, d.ActiveEpoch).Scan(&priorIntent, &priorIntentDigest)
	if intentErr == nil {
		if !bytes.Equal(priorIntent, intent) || digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), intent...)) != priorIntentDigest {
			return out, ErrFenced
		}
		var intentWrapper signedArtifact
		if artifactDecoder.Unmarshal(intent, &intentWrapper) != nil || intentWrapper.Kind != "owner-attestation" || intentWrapper.Epoch != d.ActiveEpoch {
			return out, fmt.Errorf("%w: activation intent wrapper", ErrInvalidStore)
		}
		payload, e := ownerArtifact(intent, d.OwnerPublicKey, domain, d.OwnerKeyID, "owner-attestation", "wipd.owner-attestation/1", intentWrapper.Epoch)
		if e != nil {
			return out, fmt.Errorf("%w: activation intent signature", ErrInvalidStore)
		}
		var proof ownerAttestation
		if closedPayload(payload, &proof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || proof.Action != "activation-intent" || proof.SubjectSchema != "wipd.activation-intent-subject/1" || proof.SubjectDigest != digestBytes(proof.Subject) || proof.Loss {
			return out, ErrInvalidProof
		}
		var subject struct {
			Schema      string `cbor:"schema"`
			Source      string `cbor:"source_authority_spki"`
			Origin      string `cbor:"destination_origin"`
			Destination string `cbor:"destination_authority_spki"`
			KeyID       string `cbor:"destination_artifact_key_id"`
			Next        uint64 `cbor:"next_epoch"`
		}
		if closedPayload(proof.Subject, &subject, "schema", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "next_epoch") != nil || subject.Schema != "wipd.activation-intent-subject/1" || subject.Source != binding.SourceAuthoritySPKI || subject.Origin != binding.DestinationOrigin || subject.Destination != binding.DestinationAuthoritySPKI || subject.KeyID != binding.DestinationArtifactKeyID || subject.Next != proof.Current+1 || proof.Next == nil || *proof.Next != subject.Next {
			return out, ErrFenced
		}
		if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='bundle-manifest'`, domain, d.ActiveEpoch).Scan(&priorBundle, &priorBundleDigest); err != nil {
			return out, fmt.Errorf("%w: retained bundle: %v", ErrInvalidStore, err)
		}
		if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='authority-relinquishment'`, domain, d.ActiveEpoch).Scan(&priorRel, &priorRelDigest); err != nil {
			return out, fmt.Errorf("%w: retained relinquishment: %v", ErrInvalidStore, err)
		}
		var wrapper signedArtifact
		if artifactDecoder.Unmarshal(priorBundle, &wrapper) != nil {
			return out, fmt.Errorf("%w: retained bundle wrapper", ErrInvalidStore)
		}
		var bundleValue bundlePayload
		if artifactDecoder.Unmarshal(wrapper.Payload, &bundleValue) != nil {
			return out, fmt.Errorf("%w: retained bundle payload", ErrInvalidStore)
		}
		var prefix PrefixAnchor
		if e := decodeBundlePrefix(wrapper.Payload, &prefix); e != nil {
			return out, fmt.Errorf("%w: retained bundle prefix: %v", ErrInvalidStore, e)
		}
		return ContinuityProducts{BundleManifest: priorBundle, Relinquishment: priorRel, BundleDigest: priorBundleDigest, RelinquishmentDigest: priorRelDigest, Prefix: prefix}, nil
	}
	if !errors.Is(intentErr, sql.ErrNoRows) {
		return out, intentErr
	}
	a, _, err := ownerAttestationFor(ctx, tx, intent, domain, d.ActiveEpoch, "activation-intent", at)
	if err != nil {
		return out, err
	}
	var sub struct {
		Schema      string `cbor:"schema"`
		Source      string `cbor:"source_authority_spki"`
		Origin      string `cbor:"destination_origin"`
		Destination string `cbor:"destination_authority_spki"`
		KeyID       string `cbor:"destination_artifact_key_id"`
		Next        uint64 `cbor:"next_epoch"`
	}
	if err = closedPayload(a.Subject, &sub, "schema", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "next_epoch"); err != nil || sub.Schema != "wipd.activation-intent-subject/1" || sub.Source != binding.SourceAuthoritySPKI || sub.Origin != binding.DestinationOrigin || sub.Destination != binding.DestinationAuthoritySPKI || sub.KeyID != binding.DestinationArtifactKeyID || d.ActiveEpoch == ^uint64(0) || sub.Next != d.ActiveEpoch+1 || a.Next == nil || *a.Next != sub.Next || a.Loss {
		return out, ErrInvalidProof
	}
	nonce := fmt.Sprintf("%x", a.Nonce)
	used, err := ownerNonceExists(ctx, tx, domain, nonce)
	if err != nil {
		return out, err
	}
	if used {
		return out, ErrFenced
	}
	var activeClaims, pending, unknown, transfers, incomplete int
	for query, dest := range map[string]*int{
		`SELECT count(*) FROM claims c WHERE c.domain_id=? AND c.close_command_id IS NULL AND c.authority_epoch=(SELECT active_epoch FROM domains d WHERE d.domain_id=c.domain_id)`: &activeClaims,
		`SELECT count(*) FROM submissions WHERE domain_id=? AND state='submitted'`:                                                                                                  &pending,
		`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND state IN ('pending-return','unknown')`:                                                                    &unknown,
		`SELECT count(*) FROM transfers t JOIN snapshots s USING(snapshot_id) WHERE s.domain_id=? AND s.expires_at>?`:                                                               &transfers,
		`SELECT count(*) FROM blob_products WHERE domain_id=? AND verified=0`:                                                                                                       &incomplete,
	} {
		if query == `SELECT count(*) FROM transfers t JOIN snapshots s USING(snapshot_id) WHERE s.domain_id=? AND s.expires_at>?` {
			err = tx.QueryRowContext(ctx, query, domain, at.UnixNano()).Scan(dest)
		} else {
			err = tx.QueryRowContext(ctx, query, domain).Scan(dest)
		}
		if err != nil {
			return out, err
		}
	}
	if activeClaims+pending+unknown+transfers+incomplete != 0 {
		return out, ErrFenced
	}
	prefix, blobDigest, entries, err := bundleClosure(ctx, tx, filepath.Dir(s.blobs), domain, d.ActiveEpoch)
	if err != nil {
		return out, err
	}
	head, err := currentArtifactHead(ctx, tx, domain, d.ActiveEpoch)
	if err != nil {
		return out, err
	}
	var eventID *string
	if prefix.EventID != "" {
		eventID = &prefix.EventID
	}
	p := bundlePayload{Schema: "wipd.bundle-manifest/1", Domain: domain, Epoch: d.ActiveEpoch, StoreSchema: "wipd.store/1", BlobManifestDigest: blobDigest, ArtifactHead: head, Entries: entries}
	p.Prefix.Count, p.Prefix.EventID, p.Prefix.Digest = prefix.EventCount, eventID, prefix.Digest
	bundleRaw, err := artifactEncoder.Marshal(p)
	if err != nil {
		return out, err
	}
	bundle, err := appendSignedArtifactTx(ctx, tx, domain, d.ActiveEpoch, "bundle-manifest", "wipd.bundle-manifest/1", bundleRaw, at, sign)
	if err != nil {
		return out, err
	}
	if err = addContinuityProduct(ctx, tx, domain, "activation-intent", intent, digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), intent...))); err != nil {
		return out, err
	}
	if err = addContinuityProduct(ctx, tx, domain, "bundle-manifest", bundle.Wrapper, bundle.Digest); err != nil {
		return out, err
	}
	relPayload, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.authority-relinquishment/1", "domain_id": domain, "authority_epoch": d.ActiveEpoch, "bundle_manifest_digest": bundle.Digest, "artifact_chain_head": bundle.Digest, "quiesced_at": at.UTC().Format(time.RFC3339Nano), "admission_closed": true})
	if err != nil {
		return out, err
	}
	rel, err := appendSignedArtifactTx(ctx, tx, domain, d.ActiveEpoch, "authority-relinquishment", "wipd.authority-relinquishment/1", relPayload, at, sign)
	if err != nil {
		return out, err
	}
	if err = addContinuityProduct(ctx, tx, domain, "authority-relinquishment", rel.Wrapper, rel.Digest); err != nil {
		return out, err
	}
	if err = consumeOwnerNonce(ctx, tx, domain, nonce, "activation-intent", intent, at); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE authority_continuity SET admission_closed=1,closed_epoch=? WHERE domain_id=? AND (closed_epoch IS NULL OR closed_epoch<?)`, d.ActiveEpoch, domain, d.ActiveEpoch); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return ContinuityProducts{BundleManifest: bundle.Wrapper, Relinquishment: rel.Wrapper, BundleDigest: bundle.Digest, RelinquishmentDigest: rel.Digest, Prefix: prefix}, nil
}

type handoffSubject struct {
	Schema               string `cbor:"schema"`
	IntentDigest         string `cbor:"activation_intent_digest"`
	Source               string `cbor:"source_authority_spki"`
	Origin               string `cbor:"destination_origin"`
	Destination          string `cbor:"destination_authority_spki"`
	KeyID                string `cbor:"destination_artifact_key_id"`
	BundleDigest         string `cbor:"bundle_artifact_digest"`
	RelinquishmentDigest string `cbor:"relinquishment_artifact_digest"`
	SourceHead           string `cbor:"source_artifact_chain_head"`
}

type restoreSubject struct {
	Schema           string         `cbor:"schema"`
	DeadSource       string         `cbor:"dead_source_authority_spki"`
	Origin           string         `cbor:"destination_origin"`
	Destination      string         `cbor:"destination_authority_spki"`
	KeyID            string         `cbor:"destination_artifact_key_id"`
	BundleDigest     string         `cbor:"bundle_or_proof_digest"`
	Prefix           map[string]any `cbor:"recovered_prefix"`
	ArtifactHead     string         `cbor:"recovered_artifact_chain_head"`
	UnresolvedDigest string         `cbor:"unresolved_old_commands_digest"`
}

// ActivateVerifiedBundle consumes the source's durable planned-handoff
// products. Binding is the caller-owned authenticated destination identity;
// every attested field is compared to it and to the owner-certified key.
func (s *Store) ActivateVerifiedBundle(ctx context.Context, domain string, finalAttestation, destinationArtifactKey []byte, binding ContinuityBinding, at time.Time, sign Signer) (ActivationProduct, error) {
	return s.activateVerified(ctx, domain, nil, finalAttestation, destinationArtifactKey, binding, at, sign, false)
}

// ActivateDisasterRestore requires a previously retained source-signed bundle
// already present in the candidate artifact chain, but no fresh source
// relinquishment or source response. The owner loss attestation binds the
// exact recovered prefix, chain head, and unresolved-command set.
func (s *Store) ActivateDisasterRestore(ctx context.Context, domain string, bundleManifest, restoreAttestation, destinationArtifactKey []byte, binding ContinuityBinding, at time.Time, sign Signer) (ActivationProduct, error) {
	return s.activateVerified(ctx, domain, bundleManifest, restoreAttestation, destinationArtifactKey, binding, at, sign, true)
}

func artifactProductDigest(raw []byte) string {
	return digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), raw...))
}

func decodeBundlePrefix(raw []byte, out *PrefixAnchor) error {
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !closedCBOR(fields["prefix"], "event_count", "high_water_event_id", "prefix_digest") {
		return ErrInvalidProof
	}
	var p struct {
		Count   uint64  `cbor:"event_count"`
		EventID *string `cbor:"high_water_event_id"`
		Digest  string  `cbor:"prefix_digest"`
	}
	if artifactDecoder.Unmarshal(fields["prefix"], &p) != nil || !validDigest(p.Digest) || (p.Count == 0) != (p.EventID == nil) {
		return ErrInvalidProof
	}
	out.EventCount, out.Digest = p.Count, p.Digest
	if p.EventID != nil {
		out.EventID = *p.EventID
	}
	return nil
}

func unresolvedSubmissionDigest(ctx context.Context, tx *sql.Tx, domain string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT command_id,request_hash,epoch,environment_id,environment_sequence,command FROM submissions WHERE domain_id=? AND state='submitted' ORDER BY environment_id,environment_sequence,command_id`, domain)
	if err != nil {
		return "", err
	}
	var records []any
	for rows.Next() {
		var id, hash, environment string
		var epoch, sequence uint64
		var command []byte
		if err = rows.Scan(&id, &hash, &epoch, &environment, &sequence, &command); err != nil {
			break
		}
		records = append(records, map[string]any{"command_id": id, "request_hash": hash, "authority_epoch": epoch, "environment_id": environment, "environment_sequence": sequence, "canonical_command": command})
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return "", err
	}
	raw, err := artifactEncoder.Marshal(records)
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

func (s *Store) activateVerified(ctx context.Context, domain string, bundleInput, attestation, destinationKey []byte, binding ContinuityBinding, at time.Time, sign Signer, disaster bool) (ActivationProduct, error) {
	var out ActivationProduct
	if !ulid.MatchString(domain) || at.IsZero() || sign == nil || verifyContinuityBinding(binding) != nil {
		return out, ErrInvalidProof
	}
	if len(attestation) == 0 {
		return out, ErrFenced
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, ErrInvalidStore
	}
	if err := checkStep3State(s.db); err != nil {
		return out, err
	}
	if err := checkStep4State(s.db); err != nil {
		return out, err
	}
	if err := checkStep6State(s.db); err != nil {
		return out, err
	}
	if err := checkStep5State(s.db); err != nil {
		return out, err
	}
	if err := checkStep7State(s.db); err != nil {
		return out, err
	}
	if !disaster {
		if err := checkStep7Closure(s.db, filepath.Dir(s.blobs)); err != nil {
			return out, err
		}
	}
	if err := checkBlobFiles(s.db, s.blobs); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch == ^uint64(0) {
		return out, ErrInvalidProof
	}
	next := d.ActiveEpoch + 1
	var attestationWrapper signedArtifact
	if canonicalDecode(attestation, new(map[string]cbor.RawMessage)) != nil || artifactDecoder.Unmarshal(attestation, &attestationWrapper) != nil || attestationWrapper.Kind != "owner-attestation" || attestationWrapper.DomainID != domain {
		return out, ErrInvalidProof
	}
	// Exact committed replay returns the immutable activation; it never
	// advances a second epoch or extends the artifact chain.
	var committed, priorOwner []byte
	var digest string
	var storedOwnerDigest string
	activationErr := tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='authority-activation'`, domain, attestationWrapper.Epoch).Scan(&committed, &digest)
	if activationErr == nil {
		if attestationWrapper.Epoch == ^uint64(0) || attestationWrapper.Epoch+1 != d.ActiveEpoch {
			return out, ErrFenced
		}
		if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='owner-attestation'`, domain, attestationWrapper.Epoch).Scan(&priorOwner, &storedOwnerDigest); err != nil || !bytes.Equal(priorOwner, attestation) {
			return out, ErrFenced
		}
		var wrapper signedArtifact
		if artifactDecoder.Unmarshal(committed, &wrapper) != nil || wrapper.Epoch != d.ActiveEpoch {
			return out, ErrInvalidStore
		}
		action := "planned-handoff"
		if disaster {
			action = "disaster-restore"
		}
		payload, e := ownerArtifact(priorOwner, d.OwnerPublicKey, domain, d.OwnerKeyID, "owner-attestation", "wipd.owner-attestation/1", wrapper.Epoch-1)
		if e != nil {
			return out, ErrInvalidStore
		}
		var proof ownerAttestation
		if closedPayload(payload, &proof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || proof.Action != action || proof.SubjectDigest != digestBytes(proof.Subject) {
			return out, ErrInvalidStore
		}
		if disaster {
			var subject restoreSubject
			if closedPayload(proof.Subject, &subject, "schema", "dead_source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "bundle_or_proof_digest", "recovered_prefix", "recovered_artifact_chain_head", "unresolved_old_commands_digest") != nil || subject.DeadSource != binding.SourceAuthoritySPKI || subject.Origin != binding.DestinationOrigin || subject.Destination != binding.DestinationAuthoritySPKI || subject.KeyID != binding.DestinationArtifactKeyID {
				return out, ErrFenced
			}
		} else {
			var subject handoffSubject
			if closedPayload(proof.Subject, &subject, "schema", "activation_intent_digest", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "bundle_artifact_digest", "relinquishment_artifact_digest", "source_artifact_chain_head") != nil || subject.Source != binding.SourceAuthoritySPKI || subject.Origin != binding.DestinationOrigin || subject.Destination != binding.DestinationAuthoritySPKI || subject.KeyID != binding.DestinationArtifactKeyID {
				return out, ErrFenced
			}
		}
		var keyCert []byte
		if err = tx.QueryRowContext(ctx, `SELECT certificate FROM artifact_keys WHERE domain_id=? AND epoch=? AND generation=1`, domain, wrapper.Epoch).Scan(&keyCert); err != nil || !bytes.Equal(keyCert, destinationKey) {
			return out, ErrFenced
		}
		var activationPayload struct {
			InstalledPrefix map[string]any `cbor:"installed_prefix"`
		}
		if artifactDecoder.Unmarshal(wrapper.Payload, &activationPayload) != nil {
			return out, ErrInvalidStore
		}
		prefix, e := prefixFromMap(activationPayload.InstalledPrefix)
		if e != nil {
			return out, ErrInvalidStore
		}
		return ActivationProduct{Artifact: committed, Digest: digest, Epoch: wrapper.Epoch, Prefix: prefix}, nil
	}
	if !errors.Is(activationErr, sql.ErrNoRows) {
		return out, activationErr
	}
	if attestationWrapper.Epoch != d.ActiveEpoch {
		return out, ErrFenced
	}
	var bundle, rel, intent []byte
	var bundleDigest, relDigest, intentDigest string
	if disaster {
		bundle = bytes.Clone(bundleInput)
		if len(bundle) == 0 {
			return out, ErrInvalidProof
		}
		bundleDigest = artifactProductDigest(bundle)
		var installed []byte
		var installedDigest string
		e := tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='bundle-manifest'`, domain, d.ActiveEpoch).Scan(&installed, &installedDigest)
		if e == nil {
			if !bytes.Equal(installed, bundle) || installedDigest != bundleDigest {
				return out, ErrFenced
			}
		} else if errors.Is(e, sql.ErrNoRows) {
			if e = addContinuityProduct(ctx, tx, domain, "bundle-manifest", bundle, bundleDigest); e != nil {
				return out, e
			}
		} else {
			return out, e
		}
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='bundle-manifest'`, domain, d.ActiveEpoch).Scan(&bundle, &bundleDigest); err != nil {
			return out, ErrFenced
		}
		if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='authority-relinquishment'`, domain, d.ActiveEpoch).Scan(&rel, &relDigest); err != nil {
			return out, ErrFenced
		}
		if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='activation-intent'`, domain, d.ActiveEpoch).Scan(&intent, &intentDigest); err != nil {
			return out, ErrFenced
		}
		if !bytes.Equal(bundleInput, nil) {
			return out, ErrInvalidProof
		}
	}
	var bundleWrapper signedArtifact
	var bundleFields map[string]cbor.RawMessage
	if canonicalDecode(bundle, &bundleFields) != nil || !exactKeys(bundleFields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") || artifactDecoder.Unmarshal(bundle, &bundleWrapper) != nil || bundleWrapper.Kind != "bundle-manifest" || bundleWrapper.PayloadSchema != "wipd.bundle-manifest/1" || bundleWrapper.DomainID != domain || bundleWrapper.Generation == nil || bundleWrapper.Sequence == nil || artifactProductDigest(bundle) != bundleDigest {
		return out, ErrInvalidProof
	}
	var chainDigest string
	if err = tx.QueryRowContext(ctx, `SELECT digest FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? AND sequence=? AND wrapper=?`, domain, bundleWrapper.Epoch, *bundleWrapper.Generation, *bundleWrapper.Sequence, bundle).Scan(&chainDigest); err != nil || chainDigest != bundleDigest {
		return out, ErrInvalidProof
	}
	var bp bundlePayload
	if closedPayload(bundleWrapper.Payload, &bp, "schema", "domain_id", "authority_epoch", "store_schema", "prefix", "blob_manifest_digest", "artifact_chain_head", "entries") != nil || bp.Schema != "wipd.bundle-manifest/1" || bp.Domain != domain || bp.Epoch != d.ActiveEpoch || bp.StoreSchema != "wipd.store/1" || !sameOptionalDigest(bp.ArtifactHead, bundleWrapper.Predecessor) {
		return out, ErrInvalidProof
	}
	prefix, err := bundlePrefix(bundleWrapper.Payload)
	if err != nil {
		return out, err
	}
	// A candidate must be exactly this retained prefix (not an asserted
	// caller anchor, divergent branch, or truncation of ahead state).
	actualPrefix, blobDigest, entries, err := bundleClosure(ctx, tx, filepath.Dir(s.blobs), domain, d.ActiveEpoch)
	if err != nil {
		return out, err
	}
	if prefix != actualPrefix || blobDigest != bp.BlobManifestDigest || !equalBundleEntries(entries, bp.Entries) {
		return out, ErrPrefixMismatch
	}
	currentHead, err := currentArtifactHead(ctx, tx, domain, d.ActiveEpoch)
	if err != nil {
		return out, err
	}
	var ownerProof ownerAttestation
	ownerProof, _, err = ownerAttestationFor(ctx, tx, attestation, domain, d.ActiveEpoch, func() string {
		if disaster {
			return "disaster-restore"
		}
		return "planned-handoff"
	}(), at)
	if err != nil {
		return out, err
	}
	var subjectNonce string
	if disaster {
		var subject restoreSubject
		if err = closedPayload(ownerProof.Subject, &subject, "schema", "dead_source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "bundle_or_proof_digest", "recovered_prefix", "recovered_artifact_chain_head", "unresolved_old_commands_digest"); err != nil || subject.Schema != "wipd.disaster-restore-subject/1" || subject.DeadSource != binding.SourceAuthoritySPKI || subject.Origin != binding.DestinationOrigin || subject.Destination != binding.DestinationAuthoritySPKI || subject.KeyID != binding.DestinationArtifactKeyID || subject.BundleDigest != bundleDigest || !prefixMapEqual(subject.Prefix, prefix) || currentHead == nil || subject.ArtifactHead != *currentHead || ownerProof.Next == nil || *ownerProof.Next != next || !ownerProof.Loss {
			return out, ErrInvalidProof
		}
		unresolved, e := unresolvedSubmissionDigest(ctx, tx, domain)
		if e != nil {
			return out, e
		}
		if unresolved != subject.UnresolvedDigest {
			return out, ErrInvalidProof
		}
		subjectNonce = fmt.Sprintf("%x", ownerProof.Nonce)
		if relDigest != "" || intentDigest != "" {
			return out, ErrInvalidProof
		}
	} else {
		if ownerProof.Loss || ownerProof.Next == nil || *ownerProof.Next != next {
			return out, ErrInvalidProof
		}
		var subject handoffSubject
		if err = closedPayload(ownerProof.Subject, &subject, "schema", "activation_intent_digest", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "bundle_artifact_digest", "relinquishment_artifact_digest", "source_artifact_chain_head"); err != nil || subject.Schema != "wipd.planned-handoff-subject/1" || subject.IntentDigest != intentDigest || subject.Source != binding.SourceAuthoritySPKI || subject.Origin != binding.DestinationOrigin || subject.Destination != binding.DestinationAuthoritySPKI || subject.KeyID != binding.DestinationArtifactKeyID || subject.BundleDigest != bundleDigest || subject.RelinquishmentDigest != relDigest || subject.SourceHead != relDigest || currentHead == nil || *currentHead != relDigest {
			return out, ErrInvalidProof
		}
		var relWrapper signedArtifact
		var relPayload struct {
			Schema string `cbor:"schema"`
			Domain string `cbor:"domain_id"`
			Epoch  uint64 `cbor:"authority_epoch"`
			Bundle string `cbor:"bundle_manifest_digest"`
			Head   string `cbor:"artifact_chain_head"`
			Closed bool   `cbor:"admission_closed"`
		}
		if artifactDecoder.Unmarshal(rel, &relWrapper) != nil || relWrapper.Kind != "authority-relinquishment" || relWrapper.PayloadSchema != "wipd.authority-relinquishment/1" || relWrapper.Predecessor == nil || *relWrapper.Predecessor != bundleDigest || closedPayload(relWrapper.Payload, &relPayload, "schema", "domain_id", "authority_epoch", "bundle_manifest_digest", "artifact_chain_head", "quiesced_at", "admission_closed") != nil || relPayload.Domain != domain || relPayload.Epoch != d.ActiveEpoch || relPayload.Bundle != bundleDigest || relPayload.Head != bundleDigest || !relPayload.Closed {
			return out, ErrInvalidProof
		}
		var intentPayload []byte
		var intentWrapper signedArtifact
		if artifactDecoder.Unmarshal(intent, &intentWrapper) != nil {
			return out, ErrInvalidStore
		}
		intentPayload, err = ownerArtifact(intent, d.OwnerPublicKey, domain, d.OwnerKeyID, "owner-attestation", "wipd.owner-attestation/1", d.ActiveEpoch)
		if err != nil {
			return out, ErrInvalidStore
		}
		var intentAtt ownerAttestation
		if closedPayload(intentPayload, &intentAtt, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || intentAtt.Action != "activation-intent" || intentAtt.SubjectSchema != "wipd.activation-intent-subject/1" || intentAtt.SubjectDigest != digestBytes(intentAtt.Subject) {
			return out, ErrInvalidStore
		}
		var bindingSubject struct {
			Schema      string `cbor:"schema"`
			Source      string `cbor:"source_authority_spki"`
			Origin      string `cbor:"destination_origin"`
			Destination string `cbor:"destination_authority_spki"`
			KeyID       string `cbor:"destination_artifact_key_id"`
			Next        uint64 `cbor:"next_epoch"`
		}
		if closedPayload(intentAtt.Subject, &bindingSubject, "schema", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "next_epoch") != nil || bindingSubject.Source != binding.SourceAuthoritySPKI || bindingSubject.Origin != binding.DestinationOrigin || bindingSubject.Destination != binding.DestinationAuthoritySPKI || bindingSubject.KeyID != binding.DestinationArtifactKeyID || bindingSubject.Next != next {
			return out, ErrInvalidProof
		}
		subjectNonce = fmt.Sprintf("%x", ownerProof.Nonce)
	}
	used, err := ownerNonceExists(ctx, tx, domain, subjectNonce)
	if err != nil {
		return out, err
	}
	if used {
		return out, ErrFenced
	}
	var owner []byte
	var ownerID string
	if err = tx.QueryRowContext(ctx, `SELECT owner_public_key,owner_key_id FROM domains WHERE domain_id=?`, domain).Scan(&owner, &ownerID); err != nil {
		return out, err
	}
	keyPayload, err := ownerArtifact(destinationKey, owner, domain, ownerID, "authority-artifact-key", "wipd.authority-artifact-key/1", next)
	if err != nil {
		return out, ErrInvalidProof
	}
	var kp artifactKeyPayload
	if closedPayload(keyPayload, &kp, "schema", "domain_id", "authority_epoch", "key_generation", "key_id", "ed25519_public_key", "not_before", "not_after") != nil || kp.Schema != "wipd.authority-artifact-key/1" || kp.DomainID != domain || kp.Epoch != next || kp.Generation != 1 || kp.KeyID != binding.DestinationArtifactKeyID || len(kp.PublicKey) != ed25519.PublicKeySize || interval(kp.NotBefore, kp.NotAfter, at.UTC()) != nil || kp.KeyID == ownerID {
		return out, ErrInvalidProof
	}
	keyID, err := spkiID(ed25519.PublicKey(kp.PublicKey))
	if err != nil || keyID != kp.KeyID {
		return out, ErrInvalidProof
	}
	var duplicate int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifact_keys WHERE domain_id=? AND key_id=?)+(SELECT count(*) FROM environment_cas WHERE domain_id=? AND key_id=?)+(SELECT count(*) FROM environment_certificates WHERE domain_id=? AND spki_digest=?)`, domain, keyID, domain, keyID, domain, keyID).Scan(&duplicate); err != nil || duplicate != 0 {
		return out, ErrInvalidProof
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_keys(domain_id,epoch,generation,key_id,public_key,certificate,not_before,not_after) VALUES(?,?,?,?,?,?,?,?)`, domain, next, 1, keyID, kp.PublicKey, destinationKey, kp.NotBefore, kp.NotAfter); err != nil {
		return out, ErrFenced
	}
	ownerDigest := artifactProductDigest(attestation)
	var prefixValue any = map[string]any{"event_count": prefix.EventCount, "high_water_event_id": nil, "prefix_digest": prefix.Digest}
	if prefix.EventID != "" {
		prefixValue = map[string]any{"event_count": prefix.EventCount, "high_water_event_id": prefix.EventID, "prefix_digest": prefix.Digest}
	}
	payload, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.authority-activation/1", "domain_id": domain, "previous_epoch": d.ActiveEpoch, "authority_epoch": next, "owner_attestation_digest": ownerDigest, "installed_bundle_or_proof_digest": bundleDigest, "installed_prefix": prefixValue, "activated_at": at.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return out, err
	}
	appended, err := appendSignedArtifactTx(ctx, tx, domain, next, "authority-activation", "wipd.authority-activation/1", payload, at, sign)
	if err != nil {
		return out, err
	}
	if err = addContinuityProduct(ctx, tx, domain, "owner-attestation", attestation, ownerDigest); err != nil {
		return out, err
	}
	if err = addContinuityProduct(ctx, tx, domain, "authority-activation", appended.Wrapper, appended.Digest); err != nil {
		return out, err
	}
	if err = consumeOwnerNonce(ctx, tx, domain, subjectNonce, ownerProof.Action, attestation, at); err != nil {
		return out, err
	}
	if disaster {
		if _, err = tx.ExecContext(ctx, `UPDATE authority_continuity SET admission_closed=1,closed_epoch=? WHERE domain_id=? AND (closed_epoch IS NULL OR closed_epoch<?)`, d.ActiveEpoch, domain, d.ActiveEpoch); err != nil {
			return out, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO epoch_promotions(domain_id,from_epoch,to_epoch,prior_fence_digest,promotion_proof_digest) VALUES(?,?,?,?,?)`, domain, d.ActiveEpoch, next, func() string {
		if disaster {
			return *currentHead
		}
		return relDigest
	}(), appended.Digest); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE domains SET active_epoch=? WHERE domain_id=? AND active_epoch=?`, next, domain, d.ActiveEpoch); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return ActivationProduct{Artifact: appended.Wrapper, Digest: appended.Digest, Epoch: next, Prefix: prefix}, nil
}

func bundlePrefix(raw []byte) (PrefixAnchor, error) {
	var p PrefixAnchor
	err := decodeBundlePrefix(raw, &p)
	return p, err
}

func prefixFromMap(m map[string]any) (PrefixAnchor, error) {
	var out PrefixAnchor
	if m == nil {
		return out, ErrInvalidProof
	}
	raw, err := artifactEncoder.Marshal(m)
	if err != nil {
		return out, ErrInvalidProof
	}
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !exactKeys(fields, "event_count", "high_water_event_id", "prefix_digest") {
		return out, ErrInvalidProof
	}
	var p struct {
		Count   uint64  `cbor:"event_count"`
		EventID *string `cbor:"high_water_event_id"`
		Digest  string  `cbor:"prefix_digest"`
	}
	if artifactDecoder.Unmarshal(raw, &p) != nil || !validDigest(p.Digest) || (p.Count == 0) != (p.EventID == nil) || (p.EventID != nil && !ulid.MatchString(*p.EventID)) {
		return out, ErrInvalidProof
	}
	out.EventCount, out.Digest = p.Count, p.Digest
	if p.EventID != nil {
		out.EventID = *p.EventID
	}
	return out, nil
}

type migrationAuthorizationSubject struct {
	Schema       string `cbor:"schema"`
	MigrationID  string `cbor:"migration_id"`
	SourceDigest string `cbor:"source_store_digest"`
	AuditDigest  string `cbor:"coupling_audit_digest"`
	Origin       string `cbor:"destination_origin"`
	Destination  string `cbor:"destination_domain_id"`
	Epoch        uint64 `cbor:"destination_epoch"`
	Cutover      string `cbor:"cutover_at"`
}

// AuthorizeMigration persists a verified owner authorization as a distinct
// proof point and closes ordinary admission until the proof and seal exist.
func (s *Store) AuthorizeMigration(ctx context.Context, domain string, authorization []byte, at time.Time) error {
	if !ulid.MatchString(domain) || at.IsZero() {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	for _, validate := range []func(*sql.DB) error{checkStep3State, checkStep4State, checkStep6State, checkStep5State, checkStep7State} {
		if err := validate(s.db); err != nil {
			return err
		}
	}
	var retained []byte
	var retainedDigest string
	err := s.db.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-authorization'`, domain).Scan(&retained, &retainedDigest)
	if err == nil {
		if bytes.Equal(retained, authorization) && retainedDigest == artifactProductDigest(authorization) {
			return nil
		}
		return ErrFenced
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return err
	}
	var admissionClosed int
	if err = tx.QueryRowContext(ctx, `SELECT admission_closed FROM authority_continuity WHERE domain_id=?`, domain).Scan(&admissionClosed); err != nil || admissionClosed != 0 {
		return ErrFenced
	}
	var busy int
	for _, query := range []string{`SELECT count(*) FROM submissions WHERE domain_id=? AND state='submitted'`, `SELECT count(*) FROM claims c WHERE c.domain_id=? AND c.close_command_id IS NULL AND c.authority_epoch=(SELECT active_epoch FROM domains d WHERE d.domain_id=c.domain_id)`, `SELECT count(*) FROM blob_products WHERE domain_id=? AND verified=0`} {
		if err = tx.QueryRowContext(ctx, query, domain).Scan(&busy); err != nil || busy != 0 {
			return ErrFenced
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND state IN ('pending-return','unknown')`, domain).Scan(&busy); err != nil || busy != 0 {
		return ErrFenced
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM transfers t JOIN snapshots s USING(snapshot_id) WHERE s.domain_id=? AND s.expires_at>?`, domain, at.UnixNano()).Scan(&busy); err != nil || busy != 0 {
		return ErrFenced
	}
	a, _, err := ownerAttestationFor(ctx, tx, authorization, domain, d.ActiveEpoch, "migration-authorize", at)
	if err != nil {
		return err
	}
	var sub migrationAuthorizationSubject
	if err = closedPayload(a.Subject, &sub, "schema", "migration_id", "source_store_digest", "coupling_audit_digest", "destination_origin", "destination_domain_id", "destination_epoch", "cutover_at"); err != nil || sub.Schema != "wipd.migration-authorization-subject/1" || !ulid.MatchString(sub.MigrationID) || !validDigest(sub.SourceDigest) || !validDigest(sub.AuditDigest) || sub.Destination != domain || sub.Epoch != d.ActiveEpoch || sub.Origin == "" || a.Next != nil || a.Loss {
		return ErrInvalidProof
	}
	if _, err = utcTime(sub.Cutover); err != nil {
		return err
	}
	nonce := fmt.Sprintf("%x", a.Nonce)
	used, err := ownerNonceExists(ctx, tx, domain, nonce)
	if err != nil {
		return err
	}
	if used {
		return ErrFenced
	}
	if err = addContinuityProduct(ctx, tx, domain, "migration-authorization", authorization, digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), authorization...))); err != nil {
		return err
	}
	if err = consumeOwnerNonce(ctx, tx, domain, nonce, "migration-authorize", authorization, at); err != nil {
		return err
	}
	return tx.Commit()
}

type migrationProof struct {
	Schema            string            `cbor:"schema"`
	MigrationID       string            `cbor:"migration_id"`
	SourceSchema      string            `cbor:"source_store_schema"`
	SourceDigest      string            `cbor:"source_store_digest"`
	BackupDigest      string            `cbor:"source_backup_digest"`
	SourceHighWater   map[string]any    `cbor:"source_high_water"`
	AuditDigest       string            `cbor:"coupling_audit_digest"`
	Components        []cbor.RawMessage `cbor:"components"`
	Unions            []cbor.RawMessage `cbor:"selected_domain_unions"`
	Groups            []cbor.RawMessage `cbor:"groups"`
	CloneBindings     []cbor.RawMessage `cbor:"clone_bindings"`
	SyntheticID       *string           `cbor:"synthetic_binding_command_id"`
	SyntheticHash     *string           `cbor:"synthetic_binding_request_hash"`
	SyntheticRange    cbor.RawMessage   `cbor:"synthetic_binding_event_range"`
	Destination       string            `cbor:"destination_domain_id"`
	Epoch             uint64            `cbor:"destination_epoch"`
	DestinationPrefix map[string]any    `cbor:"destination_prefix"`
	BlobClosure       string            `cbor:"blob_closure_digest"`
	ArtifactHead      *string           `cbor:"artifact_chain_head"`
	RollbackFence     string            `cbor:"rollback_fence"`
}

// RecordMigrationProof validates and persists the authority-signed closed
// migration proof and its chain advance without constructing imported groups.
func (s *Store) RecordMigrationProof(ctx context.Context, domain string, proofArtifact []byte, at time.Time) error {
	if !ulid.MatchString(domain) || at.IsZero() {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	if err := checkStep3State(s.db); err != nil {
		return err
	}
	if err := checkStep7State(s.db); err != nil {
		return err
	}
	var retained []byte
	var retainedDigest string
	err := s.db.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-proof'`, domain).Scan(&retained, &retainedDigest)
	if err == nil {
		if bytes.Equal(retained, proofArtifact) && retainedDigest == artifactProductDigest(proofArtifact) {
			return nil
		}
		return ErrFenced
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return err
	}
	var auth []byte
	var authDigest string
	if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-authorization'`, domain).Scan(&auth, &authDigest); err != nil {
		return ErrFenced
	}
	var att ownerAttestation
	var ownerPayload []byte
	att, ownerPayload, err = ownerAttestationFor(ctx, tx, auth, domain, d.ActiveEpoch, "migration-authorize", at)
	if err != nil {
		return ErrInvalidStore
	}
	var authorization migrationAuthorizationSubject
	if err = closedPayload(att.Subject, &authorization, "schema", "migration_id", "source_store_digest", "coupling_audit_digest", "destination_origin", "destination_domain_id", "destination_epoch", "cutover_at"); err != nil {
		return ErrInvalidStore
	}
	_ = ownerPayload
	var fields map[string]cbor.RawMessage
	var signed signedArtifact
	if err = canonicalDecode(proofArtifact, &fields); err != nil || !exactKeys(fields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") || artifactDecoder.Unmarshal(proofArtifact, &signed) != nil || signed.Kind != "migration-proof" || signed.PayloadSchema != "wipd.migration-proof/1" || signed.DomainID != domain || signed.Epoch != d.ActiveEpoch {
		return ErrInvalidProof
	}
	var p migrationProof
	if err = closedPayload(signed.Payload, &p, "schema", "migration_id", "source_store_schema", "source_store_digest", "source_backup_digest", "source_high_water", "coupling_audit_digest", "components", "selected_domain_unions", "groups", "clone_bindings", "synthetic_binding_command_id", "synthetic_binding_request_hash", "synthetic_binding_event_range", "destination_domain_id", "destination_epoch", "destination_prefix", "blob_closure_digest", "artifact_chain_head", "rollback_fence"); err != nil {
		return ErrInvalidProof
	}
	prefix, blobDigest, _, err := bundleClosure(ctx, tx, filepath.Dir(s.blobs), domain, d.ActiveEpoch)
	if err != nil {
		return err
	}
	head, err := currentArtifactHead(ctx, tx, domain, d.ActiveEpoch)
	if err != nil {
		return err
	}
	if p.Schema != "wipd.migration-proof/1" || p.MigrationID != authorization.MigrationID || p.SourceDigest != authorization.SourceDigest || p.AuditDigest != authorization.AuditDigest || p.Destination != domain || p.Epoch != d.ActiveEpoch || p.BlobClosure != blobDigest || p.RollbackFence != "refuse-after-first-destination-submission" || p.SourceSchema == "" || !validDigest(p.SourceDigest) || !validDigest(p.BackupDigest) || !validDigest(p.AuditDigest) || !prefixMatchesMap(p.DestinationPrefix, prefix) || !validPrefixMap(p.SourceHighWater) || !sameOptionalDigest(p.ArtifactHead, head) || len(p.Components) == 0 || len(p.Unions) == 0 || len(p.Groups) == 0 {
		return ErrInvalidProof
	}
	if err = validateMigrationProofNested(&p); err != nil {
		return err
	}
	if signed.Generation == nil || signed.Sequence == nil {
		return ErrInvalidProof
	}
	var keyID string
	var pub []byte
	var before, after string
	var fence []byte
	var activeGeneration uint64
	if err = tx.QueryRowContext(ctx, `SELECT max(generation) FROM artifact_keys WHERE domain_id=? AND epoch=?`, domain, d.ActiveEpoch).Scan(&activeGeneration); err != nil || activeGeneration != *signed.Generation {
		return ErrInvalidProof
	}
	if err = tx.QueryRowContext(ctx, `SELECT key_id,public_key,not_before,not_after,fence FROM artifact_keys WHERE domain_id=? AND epoch=? AND generation=?`, domain, d.ActiveEpoch, *signed.Generation).Scan(&keyID, &pub, &before, &after, &fence); err != nil || fence != nil {
		return ErrInvalidProof
	}
	currentHead, err := currentArtifactHead(ctx, tx, domain, d.ActiveEpoch)
	if err != nil {
		return err
	}
	var seq uint64
	var prior sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT sequence,digest FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? ORDER BY sequence DESC LIMIT 1`, domain, d.ActiveEpoch, *signed.Generation).Scan(&seq, &prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var expectedPrior *string
	if prior.Valid {
		expectedPrior = &prior.String
	}
	if !sameOptionalDigest(p.ArtifactHead, currentHead) || *signed.Sequence != seq+1 || (signed.Predecessor == nil) != (expectedPrior == nil) || (signed.Predecessor != nil && (expectedPrior == nil || *signed.Predecessor != *expectedPrior)) {
		return ErrInvalidProof
	}
	issued, e := utcTime(signed.IssuedAt)
	if e != nil || interval(before, after, issued) != nil {
		return ErrInvalidProof
	}
	digest, err := verifyAuthorityArtifact(proofArtifact, pub, domain, keyID, d.ActiveEpoch, *signed.Generation, *signed.Sequence, expectedPrior)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO authority_artifacts VALUES(?,?,?,?,?,?,?)`, domain, d.ActiveEpoch, *signed.Generation, *signed.Sequence, digest, nullableString(expectedPrior), proofArtifact); err != nil {
		return err
	}
	if err = addContinuityProduct(ctx, tx, domain, "migration-proof", proofArtifact, digest); err != nil {
		return err
	}
	return tx.Commit()
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
func sameOptionalDigest(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func prefixMatchesMap(m map[string]any, p PrefixAnchor) bool {
	return validPrefixMap(m) && prefixMapEqual(m, p)
}

func validPrefixMap(m map[string]any) bool {
	if m == nil {
		return false
	}
	raw, err := artifactEncoder.Marshal(m)
	if err != nil {
		return false
	}
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !exactKeys(fields, "event_count", "high_water_event_id", "prefix_digest") {
		return false
	}
	var p struct {
		Count   uint64  `cbor:"event_count"`
		EventID *string `cbor:"high_water_event_id"`
		Digest  string  `cbor:"prefix_digest"`
	}
	if artifactDecoder.Unmarshal(raw, &p) != nil || !validDigest(p.Digest) || (p.Count == 0) != (p.EventID == nil) || (p.EventID != nil && !ulid.MatchString(*p.EventID)) {
		return false
	}
	return true
}

func prefixMapEqual(m map[string]any, p PrefixAnchor) bool {
	if m == nil {
		return false
	}
	raw, err := artifactEncoder.Marshal(m)
	if err != nil {
		return false
	}
	var v struct {
		Count   uint64  `cbor:"event_count"`
		EventID *string `cbor:"high_water_event_id"`
		Digest  string  `cbor:"prefix_digest"`
	}
	if artifactDecoder.Unmarshal(raw, &v) != nil {
		return false
	}
	var id *string
	if p.EventID != "" {
		id = &p.EventID
	}
	return v.Count == p.EventCount && sameOptionalString(v.EventID, id) && v.Digest == p.Digest
}
func sameOptionalString(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func validateMigrationProofNested(p *migrationProof) error {
	for _, raw := range p.Components {
		var m map[string]cbor.RawMessage
		if canonicalDecode(raw, &m) != nil || !exactKeys(m, "component_id", "repo_ids", "facts_digest") {
			return ErrInvalidProof
		}
		var v struct {
			ID     string   `cbor:"component_id"`
			Repos  []string `cbor:"repo_ids"`
			Digest string   `cbor:"facts_digest"`
		}
		if artifactDecoder.Unmarshal(raw, &v) != nil || !ulid.MatchString(v.ID) || len(v.Repos) == 0 || !validDigest(v.Digest) {
			return ErrInvalidProof
		}
		for _, id := range v.Repos {
			if !ulid.MatchString(id) {
				return ErrInvalidProof
			}
		}
	}
	for _, raw := range p.Groups {
		var m map[string]cbor.RawMessage
		if canonicalDecode(raw, &m) != nil || !exactKeys(m, "correlation_origin", "environment_id", "environment_sequence", "acted_at", "request_hash", "event_range") {
			return ErrInvalidProof
		}
		var v struct {
			Origin      string `cbor:"correlation_origin"`
			Environment string `cbor:"environment_id"`
			Sequence    uint64 `cbor:"environment_sequence"`
			Acted       string `cbor:"acted_at"`
			Hash        string `cbor:"request_hash"`
		}
		if artifactDecoder.Unmarshal(raw, &v) != nil || !ulid.MatchString(v.Origin) || !ulid.MatchString(v.Environment) || v.Sequence == 0 || !validDigest(v.Hash) {
			return ErrInvalidProof
		}
		if _, e := utcTime(v.Acted); e != nil {
			return e
		}
		if err := validateEventRange(rawField(raw, "event_range")); err != nil {
			return ErrInvalidProof
		}
	}
	for _, raw := range p.Unions {
		var ids []string
		if artifactDecoder.Unmarshal(raw, &ids) != nil || len(ids) == 0 {
			return ErrInvalidProof
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !ulid.MatchString(id) || seen[id] {
				return ErrInvalidProof
			}
			seen[id] = true
		}
	}
	for _, raw := range p.CloneBindings {
		if !closedCBOR(raw, "clone_id", "environment_id") {
			return ErrInvalidProof
		}
		var v struct {
			Clone       string `cbor:"clone_id"`
			Environment string `cbor:"environment_id"`
		}
		if artifactDecoder.Unmarshal(raw, &v) != nil || !ulid.MatchString(v.Clone) || !ulid.MatchString(v.Environment) {
			return ErrInvalidProof
		}
	}
	if p.SyntheticHash != nil && !validDigest(*p.SyntheticHash) {
		return ErrInvalidProof
	}
	if p.SyntheticID != nil && !ulid.MatchString(*p.SyntheticID) {
		return ErrInvalidProof
	}
	if (p.SyntheticID == nil) != (p.SyntheticHash == nil) || (p.SyntheticID == nil) != isCBORNull(p.SyntheticRange) {
		return ErrInvalidProof
	}
	if p.SyntheticID != nil {
		if err := validateEventRange(p.SyntheticRange); err != nil {
			return err
		}
	}
	return nil
}

func isCBORNull(raw []byte) bool { return len(raw) == 1 && raw[0] == 0xf6 }
func validateEventRange(raw []byte) error {
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !exactKeys(fields, "first_event_id", "last_event_id", "event_count") {
		return ErrInvalidProof
	}
	var r struct {
		First string `cbor:"first_event_id"`
		Last  string `cbor:"last_event_id"`
		Count uint64 `cbor:"event_count"`
	}
	if artifactDecoder.Unmarshal(raw, &r) != nil || !ulid.MatchString(r.First) || !ulid.MatchString(r.Last) || r.Count == 0 {
		return ErrInvalidProof
	}
	return nil
}

func rawField(raw []byte, key string) []byte {
	var m map[string]cbor.RawMessage
	_ = artifactDecoder.Unmarshal(raw, &m)
	return m[key]
}

func closedCBOR(raw []byte, keys ...string) bool {
	var m map[string]cbor.RawMessage
	return canonicalDecode(raw, &m) == nil && exactKeys(m, keys...)
}

type migrationSealSubject struct {
	Schema              string `cbor:"schema"`
	MigrationID         string `cbor:"migration_id"`
	AuthorizationDigest string `cbor:"authorization_artifact_digest"`
	ProofDigest         string `cbor:"proof_artifact_digest"`
	Destination         string `cbor:"destination_domain_id"`
	Epoch               uint64 `cbor:"destination_epoch"`
}

// AcceptMigrationSeal persists the distinct owner seal only when it names the
// exact retained authorization and proof artifacts.
func (s *Store) AcceptMigrationSeal(ctx context.Context, domain string, seal []byte, at time.Time) error {
	if !ulid.MatchString(domain) || at.IsZero() {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	if err := checkStep3State(s.db); err != nil {
		return err
	}
	if err := checkStep7State(s.db); err != nil {
		return err
	}
	var retained []byte
	var retainedDigest string
	err := s.db.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-seal'`, domain).Scan(&retained, &retainedDigest)
	if err == nil {
		if bytes.Equal(retained, seal) && retainedDigest == artifactProductDigest(seal) {
			return nil
		}
		return ErrFenced
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return err
	}
	var auth, proof []byte
	var authDigest, proofDigest string
	if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-authorization'`, domain).Scan(&auth, &authDigest); err != nil {
		return ErrFenced
	}
	if err = tx.QueryRowContext(ctx, `SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-proof'`, domain).Scan(&proof, &proofDigest); err != nil {
		return ErrFenced
	}
	a, _, err := ownerAttestationFor(ctx, tx, seal, domain, d.ActiveEpoch, "migration-seal", at)
	if err != nil {
		return err
	}
	var sub migrationSealSubject
	if err = closedPayload(a.Subject, &sub, "schema", "migration_id", "authorization_artifact_digest", "proof_artifact_digest", "destination_domain_id", "destination_epoch"); err != nil || sub.Schema != "wipd.migration-seal-subject/1" || sub.Destination != domain || sub.Epoch != d.ActiveEpoch || sub.AuthorizationDigest != authDigest || sub.ProofDigest != proofDigest || a.Next != nil || a.Loss {
		return ErrInvalidProof
	}
	var authorization migrationAuthorizationSubject
	var authAtt ownerAttestation
	payload, e := ownerArtifact(auth, d.OwnerPublicKey, domain, d.OwnerKeyID, "owner-attestation", "wipd.owner-attestation/1", d.ActiveEpoch)
	if e != nil {
		return ErrInvalidStore
	}
	if closedPayload(payload, &authAtt, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || closedPayload(authAtt.Subject, &authorization, "schema", "migration_id", "source_store_digest", "coupling_audit_digest", "destination_origin", "destination_domain_id", "destination_epoch", "cutover_at") != nil || authorization.MigrationID != sub.MigrationID {
		return ErrInvalidStore
	}
	nonce := fmt.Sprintf("%x", a.Nonce)
	used, err := ownerNonceExists(ctx, tx, domain, nonce)
	if err != nil {
		return err
	}
	if used {
		return ErrFenced
	}
	if err = addContinuityProduct(ctx, tx, domain, "migration-seal", seal, digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), seal...))); err != nil {
		return err
	}
	if err = consumeOwnerNonce(ctx, tx, domain, nonce, "migration-seal", seal, at); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckMigrationRollback reports whether migration rollback has been
// permanently fenced. The caller still owns verification of the retained
// pre-cutover backup; this method only enforces the post-submission boundary.
func (s *Store) CheckMigrationRollback(ctx context.Context, domain string) error {
	if !ulid.MatchString(domain) {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	if err := checkStep7State(s.db); err != nil {
		return err
	}
	var authorization, seal, fenced int
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM continuity_products WHERE domain_id=? AND kind='migration-authorization'),
		(SELECT count(*) FROM continuity_products WHERE domain_id=? AND kind='migration-seal'),
		(SELECT rollback_fenced FROM authority_continuity WHERE domain_id=?)`, domain, domain, domain).Scan(&authorization, &seal, &fenced); err != nil {
		return ErrInvalidProof
	}
	if authorization != 0 && seal == 0 {
		return ErrFenced
	}
	if fenced != 0 {
		return ErrMigrationRollbackForbidden
	}
	return nil
}

func checkStep7State(db *sql.DB) error {
	var bad int
	if err := db.QueryRow(`SELECT count(*) FROM domains d LEFT JOIN authority_continuity c USING(domain_id) WHERE c.domain_id IS NULL`).Scan(&bad); err != nil || bad != 0 {
		return ErrInvalidStore
	}
	if err := db.QueryRow(`SELECT count(*) FROM continuity_products WHERE digest NOT LIKE 'sha256:%'`).Scan(&bad); err != nil || bad != 0 {
		return ErrInvalidStore
	}
	rows, err := db.Query(`SELECT domain_id,kind,authority_epoch,artifact,digest FROM continuity_products ORDER BY domain_id,authority_epoch,kind`)
	if err != nil {
		return err
	}
	type product struct {
		domain, kind, digest string
		epoch                uint64
		raw                  []byte
	}
	var products []product
	for rows.Next() {
		var p product
		if err = rows.Scan(&p.domain, &p.kind, &p.epoch, &p.raw, &p.digest); err != nil {
			break
		}
		products = append(products, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range products {
		if !ulid.MatchString(p.domain) || p.epoch == 0 || !validDigest(p.digest) {
			return ErrInvalidStore
		}
		if p.kind == "activation-intent" || p.kind == "owner-attestation" || p.kind == "migration-authorization" || p.kind == "migration-seal" {
			var owner []byte
			var ownerID string
			var epoch uint64
			if db.QueryRow(`SELECT owner_public_key,owner_key_id,active_epoch FROM domains WHERE domain_id=?`, p.domain).Scan(&owner, &ownerID, &epoch) != nil || p.epoch > epoch {
				return ErrInvalidStore
			}
			var a signedArtifact
			if artifactDecoder.Unmarshal(p.raw, &a) != nil {
				return ErrInvalidStore
			}
			if artifactProductDigest(p.raw) != p.digest {
				return ErrInvalidStore
			}
			payload, e := ownerArtifact(p.raw, owner, p.domain, ownerID, "owner-attestation", "wipd.owner-attestation/1", a.Epoch)
			if e != nil {
				return ErrInvalidStore
			}
			var proof ownerAttestation
			if a.Kind != "owner-attestation" || a.Epoch != p.epoch || closedPayload(payload, &proof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || proof.Domain != p.domain || proof.Current != a.Epoch || proof.SubjectDigest != digestBytes(proof.Subject) {
				return ErrInvalidStore
			}
			expectedAction := map[string]string{"activation-intent": "activation-intent", "migration-authorization": "migration-authorize", "migration-seal": "migration-seal"}[p.kind]
			if (expectedAction != "" && proof.Action != expectedAction) || (p.kind == "owner-attestation" && proof.Action != "planned-handoff" && proof.Action != "disaster-restore") {
				return ErrInvalidStore
			}
			subjectSchemas := map[string]string{"activation-intent": "wipd.activation-intent-subject/1", "planned-handoff": "wipd.planned-handoff-subject/1", "disaster-restore": "wipd.disaster-restore-subject/1", "migration-authorize": "wipd.migration-authorization-subject/1", "migration-seal": "wipd.migration-seal-subject/1"}
			issued, timeErr := utcTime(proof.Issued)
			expires, expireErr := utcTime(proof.Expires)
			if proof.SubjectSchema != subjectSchemas[proof.Action] || timeErr != nil || expireErr != nil || !issued.Before(expires) || expires.Sub(issued) > 10*time.Minute || len(proof.Nonce) != 16 {
				return ErrInvalidStore
			}
			if proof.Action == "activation-intent" || proof.Action == "planned-handoff" || proof.Action == "disaster-restore" {
				if proof.Current == ^uint64(0) || proof.Next == nil || *proof.Next != proof.Current+1 || proof.Loss != (proof.Action == "disaster-restore") {
					return ErrInvalidStore
				}
			} else if proof.Next != nil || proof.Loss {
				return ErrInvalidStore
			}
			continue
		}
		var a signedArtifact
		if artifactDecoder.Unmarshal(p.raw, &a) != nil || a.DomainID != p.domain {
			return ErrInvalidStore
		}
		var epoch, generation, sequence uint64
		if a.Epoch == 0 || a.Generation == nil || a.Sequence == nil || (p.kind == "authority-activation" && (p.epoch == ^uint64(0) || a.Epoch != p.epoch+1)) || (p.kind != "authority-activation" && a.Epoch != p.epoch) {
			return ErrInvalidStore
		}
		epoch, generation, sequence = a.Epoch, *a.Generation, *a.Sequence
		var stored []byte
		var digest string
		if db.QueryRow(`SELECT wrapper,digest FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? AND sequence=?`, p.domain, epoch, generation, sequence).Scan(&stored, &digest) != nil || !bytes.Equal(stored, p.raw) || digest != p.digest {
			return ErrInvalidStore
		}
	}
	rows, err = db.Query(`SELECT domain_id,nonce,action,artifact_digest,consumed_at FROM owner_nonce_uses`)
	if err != nil {
		return err
	}
	type nonceUse struct{ domain, nonce, action, digest, at string }
	var uses []nonceUse
	for rows.Next() {
		var use nonceUse
		if err = rows.Scan(&use.domain, &use.nonce, &use.action, &use.digest, &use.at); err != nil {
			break
		}
		uses = append(uses, use)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, use := range uses {
		if !ulid.MatchString(use.domain) || len(use.nonce) != 32 || !validDigest(use.digest) {
			return ErrInvalidStore
		}
		if _, err = utcTime(use.at); err != nil {
			return ErrInvalidStore
		}
		if use.action == "claim-stand-down" {
			var auth []byte
			var verified string
			if db.QueryRow(`SELECT authorization,verified_at FROM claim_stand_down_proofs WHERE domain_id=? AND owner_nonce=?`, use.domain, use.nonce).Scan(&auth, &verified) != nil || digestBytes(auth) != use.digest || verified != use.at {
				return ErrInvalidStore
			}
			continue
		}
		kind := map[string]string{"activation-intent": "activation-intent", "planned-handoff": "owner-attestation", "disaster-restore": "owner-attestation", "migration-authorize": "migration-authorization", "migration-seal": "migration-seal"}[use.action]
		if kind == "" {
			return ErrInvalidStore
		}
		var owner []byte
		var ownerID string
		if db.QueryRow(`SELECT owner_public_key,owner_key_id FROM domains WHERE domain_id=?`, use.domain).Scan(&owner, &ownerID) != nil {
			return ErrInvalidStore
		}
		productRows, e := db.Query(`SELECT artifact FROM continuity_products WHERE domain_id=? AND kind=? ORDER BY authority_epoch`, use.domain, kind)
		if e != nil {
			return e
		}
		var raw []byte
		for productRows.Next() {
			var candidate []byte
			if e = productRows.Scan(&candidate); e != nil {
				break
			}
			var wrapper signedArtifact
			if artifactDecoder.Unmarshal(candidate, &wrapper) != nil {
				e = ErrInvalidStore
				break
			}
			payload, verifyErr := ownerArtifact(candidate, owner, use.domain, ownerID, "owner-attestation", "wipd.owner-attestation/1", wrapper.Epoch)
			var proof ownerAttestation
			if verifyErr == nil && closedPayload(payload, &proof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") == nil && proof.Action == use.action && fmt.Sprintf("%x", proof.Nonce) == use.nonce && digestBytes(candidate) == use.digest {
				raw = candidate
				break
			}
		}
		if e == nil {
			e = productRows.Err()
		}
		_ = productRows.Close()
		if e != nil || len(raw) == 0 {
			return ErrInvalidStore
		}
	}
	if err = db.QueryRow(`SELECT count(*) FROM (SELECT domain_id,owner_nonce,count(*) n FROM claim_stand_down_proofs GROUP BY domain_id,owner_nonce HAVING n>1)`).Scan(&bad); err != nil || bad != 0 {
		return ErrInvalidStore
	}
	if err = db.QueryRow(`SELECT count(*) FROM owner_nonce_uses n JOIN claim_stand_down_proofs p ON p.domain_id=n.domain_id AND p.owner_nonce=n.nonce WHERE n.action!='claim-stand-down'`).Scan(&bad); err != nil || bad != 0 {
		return ErrInvalidStore
	}
	if err = checkEpochPromotions(db); err != nil {
		return err
	}
	return checkMigrationContinuity(db)
}

func checkEpochPromotions(db *sql.DB) error {
	var bad int
	if err := db.QueryRow(`SELECT count(*) FROM domains d WHERE d.active_epoch!=coalesce((SELECT max(to_epoch) FROM epoch_promotions p WHERE p.domain_id=d.domain_id),d.initial_epoch)`).Scan(&bad); err != nil || bad != 0 {
		return ErrInvalidStore
	}
	rows, err := db.Query(`SELECT p.domain_id,p.from_epoch,p.to_epoch,p.prior_fence_digest,p.promotion_proof_digest,d.initial_epoch,d.active_epoch,d.owner_public_key,d.owner_key_id FROM epoch_promotions p JOIN domains d USING(domain_id) ORDER BY p.domain_id,p.from_epoch`)
	if err != nil {
		return err
	}
	type promotion struct {
		domain, prior, proof      string
		from, to, initial, active uint64
		owner                     []byte
		ownerID                   string
	}
	var all []promotion
	for rows.Next() {
		var p promotion
		if err = rows.Scan(&p.domain, &p.from, &p.to, &p.prior, &p.proof, &p.initial, &p.active, &p.owner, &p.ownerID); err != nil {
			break
		}
		all = append(all, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	var priorDomain string
	var nextEpoch, currentActive, currentInitial uint64
	for _, p := range all {
		if p.domain != priorDomain {
			if priorDomain != "" && nextEpoch != currentActive {
				return ErrInvalidStore
			}
			priorDomain, nextEpoch, currentInitial = p.domain, p.initial, p.initial
			currentActive = p.active
		} else if p.initial != currentInitial || p.active != currentActive {
			return ErrInvalidStore
		}
		if !validDigest(p.prior) || !validDigest(p.proof) || p.from != nextEpoch || p.from == ^uint64(0) || p.to != p.from+1 {
			return ErrInvalidStore
		}
		if err = checkPromotionEvidence(db, p.domain, p.from, p.to, p.prior, p.proof, p.owner, p.ownerID); err != nil {
			return err
		}
		nextEpoch = p.to
	}
	if priorDomain != "" && nextEpoch != currentActive {
		return ErrInvalidStore
	}
	return nil
}

func checkPromotionEvidence(db *sql.DB, domain string, from, to uint64, prior, proofDigest string, owner []byte, ownerID string) error {
	load := func(kind string, epoch uint64) ([]byte, string, error) {
		var raw []byte
		var digest string
		err := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind=?`, domain, epoch, kind).Scan(&raw, &digest)
		return raw, digest, err
	}
	bundle, bundleDigest, err := load("bundle-manifest", from)
	if err != nil || artifactProductDigest(bundle) != bundleDigest || !validDigest(bundleDigest) {
		return ErrInvalidStore
	}
	var bundleWrapper signedArtifact
	var bundleFields map[string]cbor.RawMessage
	if canonicalDecode(bundle, &bundleFields) != nil || !exactKeys(bundleFields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") || artifactDecoder.Unmarshal(bundle, &bundleWrapper) != nil || bundleWrapper.Kind != "bundle-manifest" || bundleWrapper.DomainID != domain || bundleWrapper.Epoch != from || bundleWrapper.PayloadSchema != "wipd.bundle-manifest/1" || bundleWrapper.Generation == nil || bundleWrapper.Sequence == nil {
		return ErrInvalidStore
	}
	var bp bundlePayload
	if closedPayload(bundleWrapper.Payload, &bp, "schema", "domain_id", "authority_epoch", "store_schema", "prefix", "blob_manifest_digest", "artifact_chain_head", "entries") != nil || bp.Schema != "wipd.bundle-manifest/1" || bp.Domain != domain || bp.Epoch != from || bp.StoreSchema != "wipd.store/1" || !validDigest(bp.BlobManifestDigest) || !sameOptionalDigest(bp.ArtifactHead, bundleWrapper.Predecessor) || validateBundlePayload(bundleWrapper.Payload, &bp) != nil {
		return ErrInvalidStore
	}
	prefix, err := bundlePrefix(bundleWrapper.Payload)
	if err != nil {
		return ErrInvalidStore
	}
	var finalOwner, activation []byte
	var ownerDigest, activationDigest string
	if finalOwner, ownerDigest, err = load("owner-attestation", from); err != nil || artifactProductDigest(finalOwner) != ownerDigest {
		return ErrInvalidStore
	}
	if activation, activationDigest, err = load("authority-activation", from); err != nil || activationDigest != proofDigest || artifactProductDigest(activation) != activationDigest {
		return ErrInvalidStore
	}
	var ownerWrapper signedArtifact
	if artifactDecoder.Unmarshal(finalOwner, &ownerWrapper) != nil || ownerWrapper.Epoch != from {
		return ErrInvalidStore
	}
	ownerPayload, err := ownerArtifact(finalOwner, owner, domain, ownerID, "owner-attestation", "wipd.owner-attestation/1", from)
	if err != nil {
		return ErrInvalidStore
	}
	var ownerProof ownerAttestation
	if closedPayload(ownerPayload, &ownerProof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || ownerProof.Domain != domain || ownerProof.Current != from || ownerProof.Next == nil || *ownerProof.Next != to || ownerProof.SubjectDigest != digestBytes(ownerProof.Subject) {
		return ErrInvalidStore
	}
	planned := ownerProof.Action == "planned-handoff" && !ownerProof.Loss && ownerProof.SubjectSchema == "wipd.planned-handoff-subject/1"
	disaster := ownerProof.Action == "disaster-restore" && ownerProof.Loss && ownerProof.SubjectSchema == "wipd.disaster-restore-subject/1"
	if !planned && !disaster {
		return ErrInvalidStore
	}
	if !ownerUseMatches(db, domain, fmt.Sprintf("%x", ownerProof.Nonce), ownerProof.Action, finalOwner) {
		return ErrInvalidStore
	}
	var binding ContinuityBinding
	if planned {
		intent, _, e := load("activation-intent", from)
		if e != nil {
			return ErrInvalidStore
		}
		var intentWrapper signedArtifact
		if artifactDecoder.Unmarshal(intent, &intentWrapper) != nil || intentWrapper.Epoch != from {
			return ErrInvalidStore
		}
		intentPayload, e := ownerArtifact(intent, owner, domain, ownerID, "owner-attestation", "wipd.owner-attestation/1", from)
		if e != nil {
			return ErrInvalidStore
		}
		var intentProof ownerAttestation
		if closedPayload(intentPayload, &intentProof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || intentProof.Action != "activation-intent" || intentProof.Domain != domain || intentProof.Current != from || intentProof.Next == nil || *intentProof.Next != to || intentProof.SubjectDigest != digestBytes(intentProof.Subject) {
			return ErrInvalidStore
		}
		if !ownerUseMatches(db, domain, fmt.Sprintf("%x", intentProof.Nonce), "activation-intent", intent) {
			return ErrInvalidStore
		}
		var is handoffSubject
		if closedPayload(ownerProof.Subject, &is, "schema", "activation_intent_digest", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "bundle_artifact_digest", "relinquishment_artifact_digest", "source_artifact_chain_head") != nil || is.Schema != "wipd.planned-handoff-subject/1" || is.IntentDigest != artifactProductDigest(intent) || is.BundleDigest != bundleDigest || is.RelinquishmentDigest != prior || is.SourceHead != prior {
			return ErrInvalidStore
		}
		var intentSubject struct {
			Schema      string `cbor:"schema"`
			Source      string `cbor:"source_authority_spki"`
			Origin      string `cbor:"destination_origin"`
			Destination string `cbor:"destination_authority_spki"`
			KeyID       string `cbor:"destination_artifact_key_id"`
			Next        uint64 `cbor:"next_epoch"`
		}
		if closedPayload(intentProof.Subject, &intentSubject, "schema", "source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "next_epoch") != nil || intentSubject.Schema != "wipd.activation-intent-subject/1" || intentSubject.Source != is.Source || intentSubject.Origin != is.Origin || intentSubject.Destination != is.Destination || intentSubject.KeyID != is.KeyID || intentSubject.Next != to {
			return ErrInvalidStore
		}
		binding = ContinuityBinding{SourceAuthoritySPKI: is.Source, DestinationOrigin: is.Origin, DestinationAuthoritySPKI: is.Destination, DestinationArtifactKeyID: is.KeyID}
		rel, relDigest, e := load("authority-relinquishment", from)
		if e != nil || relDigest != prior || artifactProductDigest(rel) != relDigest {
			return ErrInvalidStore
		}
		var rw signedArtifact
		var rp struct {
			Domain string `cbor:"domain_id"`
			Epoch  uint64 `cbor:"authority_epoch"`
			Bundle string `cbor:"bundle_manifest_digest"`
			Head   string `cbor:"artifact_chain_head"`
			At     string `cbor:"quiesced_at"`
			Closed bool   `cbor:"admission_closed"`
		}
		if artifactDecoder.Unmarshal(rel, &rw) != nil || rw.Kind != "authority-relinquishment" || rw.Epoch != from || rw.Predecessor == nil || *rw.Predecessor != bundleDigest || closedPayload(rw.Payload, &rp, "schema", "domain_id", "authority_epoch", "bundle_manifest_digest", "artifact_chain_head", "quiesced_at", "admission_closed") != nil || rp.Domain != domain || rp.Epoch != from || rp.Bundle != bundleDigest || rp.Head != bundleDigest || !rp.Closed {
			return ErrInvalidStore
		}
		if _, e = utcTime(rp.At); e != nil {
			return ErrInvalidStore
		}
	} else {
		if _, _, e := load("activation-intent", from); !errors.Is(e, sql.ErrNoRows) {
			return ErrInvalidStore
		}
		if _, _, e := load("authority-relinquishment", from); !errors.Is(e, sql.ErrNoRows) {
			return ErrInvalidStore
		}
		var rs restoreSubject
		if closedPayload(ownerProof.Subject, &rs, "schema", "dead_source_authority_spki", "destination_origin", "destination_authority_spki", "destination_artifact_key_id", "bundle_or_proof_digest", "recovered_prefix", "recovered_artifact_chain_head", "unresolved_old_commands_digest") != nil || rs.Schema != "wipd.disaster-restore-subject/1" || rs.BundleDigest != bundleDigest || rs.ArtifactHead != bundleDigest || !prefixMatchesMap(rs.Prefix, prefix) || !validDigest(rs.UnresolvedDigest) {
			return ErrInvalidStore
		}
		binding = ContinuityBinding{SourceAuthoritySPKI: rs.DeadSource, DestinationOrigin: rs.Origin, DestinationAuthoritySPKI: rs.Destination, DestinationArtifactKeyID: rs.KeyID}
	}
	if verifyContinuityBinding(binding) != nil {
		return ErrInvalidStore
	}
	var aw signedArtifact
	if artifactDecoder.Unmarshal(activation, &aw) != nil || aw.Kind != "authority-activation" || aw.DomainID != domain || aw.Epoch != to || aw.PayloadSchema != "wipd.authority-activation/1" {
		return ErrInvalidStore
	}
	var ap struct {
		Schema       string         `cbor:"schema"`
		Domain       string         `cbor:"domain_id"`
		Previous     uint64         `cbor:"previous_epoch"`
		Epoch        uint64         `cbor:"authority_epoch"`
		OwnerDigest  string         `cbor:"owner_attestation_digest"`
		BundleDigest string         `cbor:"installed_bundle_or_proof_digest"`
		Prefix       map[string]any `cbor:"installed_prefix"`
		At           string         `cbor:"activated_at"`
	}
	if closedPayload(aw.Payload, &ap, "schema", "domain_id", "previous_epoch", "authority_epoch", "owner_attestation_digest", "installed_bundle_or_proof_digest", "installed_prefix", "activated_at") != nil || ap.Schema != "wipd.authority-activation/1" || ap.Domain != domain || ap.Previous != from || ap.Epoch != to || ap.OwnerDigest != ownerDigest || ap.BundleDigest != bundleDigest || !prefixMapEqual(ap.Prefix, prefix) {
		return ErrInvalidStore
	}
	if _, err = utcTime(ap.At); err != nil {
		return ErrInvalidStore
	}
	var keyID string
	var keyCert []byte
	if db.QueryRow(`SELECT key_id,certificate FROM artifact_keys WHERE domain_id=? AND epoch=? AND generation=1`, domain, to).Scan(&keyID, &keyCert) != nil || len(keyCert) == 0 || keyID != binding.DestinationArtifactKeyID {
		return ErrInvalidStore
	}
	if disaster && prior != bundleDigest {
		return ErrInvalidStore
	}
	return nil
}

func checkMigrationContinuity(db *sql.DB) error {
	rows, err := db.Query(`SELECT domain_id,owner_public_key,owner_key_id,active_epoch FROM domains ORDER BY domain_id`)
	if err != nil {
		return err
	}
	type domainRecord struct {
		domain, ownerID string
		owner           []byte
		active          uint64
	}
	var domains []domainRecord
	for rows.Next() {
		var d domainRecord
		if err = rows.Scan(&d.domain, &d.owner, &d.ownerID, &d.active); err != nil {
			break
		}
		domains = append(domains, d)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, d := range domains {
		var authorization, proof, seal []byte
		var authorizationDigest, proofDigest, sealDigest string
		authErr := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-authorization'`, d.domain).Scan(&authorization, &authorizationDigest)
		proofErr := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-proof'`, d.domain).Scan(&proof, &proofDigest)
		sealErr := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND kind='migration-seal'`, d.domain).Scan(&seal, &sealDigest)
		var rollback int
		var rollbackCommand, rollbackHash sql.NullString
		var rollbackEpoch sql.NullInt64
		if err = db.QueryRow(`SELECT rollback_fenced,rollback_command_id,rollback_request_hash,rollback_epoch FROM authority_continuity WHERE domain_id=?`, d.domain).Scan(&rollback, &rollbackCommand, &rollbackHash, &rollbackEpoch); err != nil {
			return err
		}
		if rollback < 0 || rollback > 1 || (rollback == 0 && (rollbackCommand.Valid || rollbackHash.Valid || rollbackEpoch.Valid)) || (rollback == 1 && (!rollbackCommand.Valid || !rollbackHash.Valid || !rollbackEpoch.Valid || rollbackEpoch.Int64 <= 0 || !validDigest(rollbackHash.String))) {
			return ErrInvalidStore
		}
		if errors.Is(authErr, sql.ErrNoRows) {
			if !errors.Is(proofErr, sql.ErrNoRows) || !errors.Is(sealErr, sql.ErrNoRows) || rollback != 0 {
				return ErrInvalidStore
			}
			continue
		}
		if authErr != nil {
			return authErr
		}
		if artifactProductDigest(authorization) != authorizationDigest {
			return ErrInvalidStore
		}
		var aw signedArtifact
		if artifactDecoder.Unmarshal(authorization, &aw) != nil || aw.Kind != "owner-attestation" {
			return ErrInvalidStore
		}
		authPayload, e := ownerArtifact(authorization, d.owner, d.domain, d.ownerID, "owner-attestation", "wipd.owner-attestation/1", aw.Epoch)
		if e != nil {
			return ErrInvalidStore
		}
		var ownerAuth ownerAttestation
		if closedPayload(authPayload, &ownerAuth, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || ownerAuth.Action != "migration-authorize" || ownerAuth.Domain != d.domain || ownerAuth.Current != aw.Epoch || ownerAuth.Next != nil || ownerAuth.Loss || ownerAuth.SubjectSchema != "wipd.migration-authorization-subject/1" || ownerAuth.SubjectDigest != digestBytes(ownerAuth.Subject) {
			return ErrInvalidStore
		}
		var authSubject migrationAuthorizationSubject
		if closedPayload(ownerAuth.Subject, &authSubject, "schema", "migration_id", "source_store_digest", "coupling_audit_digest", "destination_origin", "destination_domain_id", "destination_epoch", "cutover_at") != nil || authSubject.Schema != "wipd.migration-authorization-subject/1" || !ulid.MatchString(authSubject.MigrationID) || !validDigest(authSubject.SourceDigest) || !validDigest(authSubject.AuditDigest) || authSubject.Destination != d.domain || authSubject.Epoch != aw.Epoch || authSubject.Origin == "" {
			return ErrInvalidStore
		}
		if _, e = utcTime(authSubject.Cutover); e != nil {
			return ErrInvalidStore
		}
		if !ownerUseMatches(db, d.domain, fmt.Sprintf("%x", ownerAuth.Nonce), "migration-authorize", authorization) {
			return ErrInvalidStore
		}
		if errors.Is(proofErr, sql.ErrNoRows) {
			if !errors.Is(sealErr, sql.ErrNoRows) || rollback != 0 {
				return ErrInvalidStore
			}
			continue
		}
		if proofErr != nil {
			return proofErr
		}
		if artifactProductDigest(proof) != proofDigest {
			return ErrInvalidStore
		}
		var signed signedArtifact
		var fields map[string]cbor.RawMessage
		if canonicalDecode(proof, &fields) != nil || !exactKeys(fields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") || artifactDecoder.Unmarshal(proof, &signed) != nil || signed.Kind != "migration-proof" || signed.DomainID != d.domain || signed.Epoch != aw.Epoch || signed.PayloadSchema != "wipd.migration-proof/1" || signed.Generation == nil || signed.Sequence == nil {
			return ErrInvalidStore
		}
		var p migrationProof
		if closedPayload(signed.Payload, &p, "schema", "migration_id", "source_store_schema", "source_store_digest", "source_backup_digest", "source_high_water", "coupling_audit_digest", "components", "selected_domain_unions", "groups", "clone_bindings", "synthetic_binding_command_id", "synthetic_binding_request_hash", "synthetic_binding_event_range", "destination_domain_id", "destination_epoch", "destination_prefix", "blob_closure_digest", "artifact_chain_head", "rollback_fence") != nil || p.Schema != "wipd.migration-proof/1" || p.MigrationID != authSubject.MigrationID || p.SourceDigest != authSubject.SourceDigest || p.AuditDigest != authSubject.AuditDigest || p.Destination != d.domain || p.Epoch != aw.Epoch || p.RollbackFence != "refuse-after-first-destination-submission" || !validDigest(p.BackupDigest) || !validDigest(p.BlobClosure) || !validPrefixMap(p.SourceHighWater) || !sameOptionalDigest(p.ArtifactHead, signed.Predecessor) || validateMigrationProofNested(&p) != nil {
			return ErrInvalidStore
		}
		tx, e := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if e != nil {
			return e
		}
		anchor, e := prefixFromMap(p.DestinationPrefix)
		if e == nil {
			var retained PrefixAnchor
			retained, e = anchorAt(context.Background(), tx, d.domain, anchor.EventCount)
			if e == nil && retained != anchor {
				e = ErrInvalidStore
			}
		}
		_ = tx.Rollback()
		if e != nil {
			return ErrInvalidStore
		}
		var chainWrapper []byte
		if db.QueryRow(`SELECT wrapper FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? AND sequence=?`, d.domain, aw.Epoch, *signed.Generation, *signed.Sequence).Scan(&chainWrapper) != nil || !bytes.Equal(chainWrapper, proof) {
			return ErrInvalidStore
		}
		if errors.Is(sealErr, sql.ErrNoRows) {
			if rollback != 0 {
				return ErrInvalidStore
			}
			continue
		}
		if sealErr != nil {
			return sealErr
		}
		if artifactProductDigest(seal) != sealDigest {
			return ErrInvalidStore
		}
		var sw signedArtifact
		if artifactDecoder.Unmarshal(seal, &sw) != nil || sw.Kind != "owner-attestation" || sw.Epoch != aw.Epoch {
			return ErrInvalidStore
		}
		sealPayload, e := ownerArtifact(seal, d.owner, d.domain, d.ownerID, "owner-attestation", "wipd.owner-attestation/1", sw.Epoch)
		if e != nil {
			return ErrInvalidStore
		}
		var ownerSeal ownerAttestation
		if closedPayload(sealPayload, &ownerSeal, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || ownerSeal.Action != "migration-seal" || ownerSeal.Domain != d.domain || ownerSeal.Current != aw.Epoch || ownerSeal.Next != nil || ownerSeal.Loss || ownerSeal.SubjectSchema != "wipd.migration-seal-subject/1" || ownerSeal.SubjectDigest != digestBytes(ownerSeal.Subject) {
			return ErrInvalidStore
		}
		var sealSubject migrationSealSubject
		if closedPayload(ownerSeal.Subject, &sealSubject, "schema", "migration_id", "authorization_artifact_digest", "proof_artifact_digest", "destination_domain_id", "destination_epoch") != nil || sealSubject.Schema != "wipd.migration-seal-subject/1" || sealSubject.MigrationID != authSubject.MigrationID || sealSubject.AuthorizationDigest != authorizationDigest || sealSubject.ProofDigest != proofDigest || sealSubject.Destination != d.domain || sealSubject.Epoch != aw.Epoch || !ownerUseMatches(db, d.domain, fmt.Sprintf("%x", ownerSeal.Nonce), "migration-seal", seal) {
			return ErrInvalidStore
		}
		if rollback != 0 && rollback != 1 {
			return ErrInvalidStore
		}
		if rollback == 1 {
			var matched int
			if db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=? AND request_hash=? AND epoch=?`, d.domain, rollbackCommand.String, rollbackHash.String, rollbackEpoch.Int64).Scan(&matched) != nil || matched != 1 || rollbackEpoch.Int64 < int64(aw.Epoch) || rollbackEpoch.Int64 > int64(d.active) {
				return ErrInvalidStore
			}
		}
	}
	return nil
}

func ownerUseMatches(db *sql.DB, domain, nonce, action string, raw []byte) bool {
	var storedDigest, storedAction string
	return db.QueryRow(`SELECT artifact_digest,action FROM owner_nonce_uses WHERE domain_id=? AND nonce=?`, domain, nonce).Scan(&storedDigest, &storedAction) == nil && storedAction == action && storedDigest == digestBytes(raw)
}

func checkStep7Closure(db *sql.DB, root string) error {
	var orphaned int
	if err := db.QueryRow(`SELECT count(*) FROM continuity_products p JOIN authority_continuity c USING(domain_id)
JOIN domains d USING(domain_id)
WHERE p.kind IN ('activation-intent','bundle-manifest','authority-relinquishment','owner-attestation','authority-activation')
AND (c.closed_epoch IS NULL OR p.authority_epoch>c.closed_epoch OR (p.authority_epoch<c.closed_epoch AND NOT EXISTS(
  SELECT 1 FROM epoch_promotions e WHERE e.domain_id=p.domain_id AND e.from_epoch=p.authority_epoch
  AND (p.kind!='authority-activation' OR e.promotion_proof_digest=p.digest))))`).Scan(&orphaned); err != nil || orphaned != 0 {
		return ErrInvalidStore
	}
	rows, err := db.Query(`SELECT domain_id,admission_closed,closed_epoch FROM authority_continuity ORDER BY domain_id`)
	if err != nil {
		return err
	}
	type state struct {
		domain      string
		closed      int
		closedEpoch sql.NullInt64
	}
	var states []state
	for rows.Next() {
		var v state
		if err = rows.Scan(&v.domain, &v.closed, &v.closedEpoch); err != nil {
			break
		}
		states = append(states, v)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, st := range states {
		var bundle, rel, intent, finalOwner, activation []byte
		var bd, rd, id, ownerDigest, activationDigest string
		var active, initial uint64
		var owner []byte
		var ownerID string
		if err = db.QueryRow(`SELECT owner_public_key,owner_key_id,active_epoch,initial_epoch FROM domains WHERE domain_id=?`, st.domain).Scan(&owner, &ownerID, &active, &initial); err != nil {
			return err
		}
		if !st.closedEpoch.Valid {
			var handoffProducts int
			if st.closed != 0 || active != initial || db.QueryRow(`SELECT count(*) FROM continuity_products WHERE domain_id=? AND kind IN ('activation-intent','bundle-manifest','authority-relinquishment','owner-attestation','authority-activation')`, st.domain).Scan(&handoffProducts) != nil || handoffProducts != 0 {
				return ErrInvalidStore
			}
			continue
		}
		if st.closed == 0 || st.closedEpoch.Int64 <= 0 || uint64(st.closedEpoch.Int64) > active || active-uint64(st.closedEpoch.Int64) > 1 {
			return ErrInvalidStore
		}
		sourceEpoch := uint64(st.closedEpoch.Int64)
		be := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='bundle-manifest'`, st.domain, sourceEpoch).Scan(&bundle, &bd)
		re := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='authority-relinquishment'`, st.domain, sourceEpoch).Scan(&rel, &rd)
		ie := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='activation-intent'`, st.domain, sourceEpoch).Scan(&intent, &id)
		oe := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='owner-attestation'`, st.domain, sourceEpoch).Scan(&finalOwner, &ownerDigest)
		ae := db.QueryRow(`SELECT artifact,digest FROM continuity_products WHERE domain_id=? AND authority_epoch=? AND kind='authority-activation'`, st.domain, sourceEpoch).Scan(&activation, &activationDigest)
		if be != nil {
			return ErrInvalidStore
		}
		var bundleWrapper signedArtifact
		var bundleFields map[string]cbor.RawMessage
		if canonicalDecode(bundle, &bundleFields) != nil || !exactKeys(bundleFields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") || artifactDecoder.Unmarshal(bundle, &bundleWrapper) != nil || bundleWrapper.Kind != "bundle-manifest" || bundleWrapper.PayloadSchema != "wipd.bundle-manifest/1" || bundleWrapper.DomainID != st.domain || bundleWrapper.Generation == nil || bundleWrapper.Sequence == nil || artifactProductDigest(bundle) != bd {
			return ErrInvalidStore
		}
		var bp bundlePayload
		if closedPayload(bundleWrapper.Payload, &bp, "schema", "domain_id", "authority_epoch", "store_schema", "prefix", "blob_manifest_digest", "artifact_chain_head", "entries") != nil || bp.Schema != "wipd.bundle-manifest/1" || bp.Domain != st.domain || bp.Epoch != bundleWrapper.Epoch || bp.StoreSchema != "wipd.store/1" || !validDigest(bp.BlobManifestDigest) || !sameOptionalDigest(bp.ArtifactHead, bundleWrapper.Predecessor) || validateBundlePayload(bundleWrapper.Payload, &bp) != nil {
			return ErrInvalidStore
		}
		prefix, err := bundlePrefix(bundleWrapper.Payload)
		if err != nil {
			return ErrInvalidStore
		}
		planned := ie == nil && re == nil
		disaster := errors.Is(ie, sql.ErrNoRows) && errors.Is(re, sql.ErrNoRows)
		if !planned && !disaster {
			return ErrInvalidStore
		}
		if sourceEpoch != bp.Epoch {
			return ErrInvalidStore
		}
		if active == bp.Epoch {
			if !planned || oe != sql.ErrNoRows || ae != sql.ErrNoRows {
				return ErrInvalidStore
			}
			payload, e := ownerArtifact(intent, owner, st.domain, ownerID, "owner-attestation", "wipd.owner-attestation/1", bp.Epoch)
			if e != nil {
				return ErrInvalidStore
			}
			var intentProof ownerAttestation
			if closedPayload(payload, &intentProof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || intentProof.Action != "activation-intent" || intentProof.SubjectSchema != "wipd.activation-intent-subject/1" || intentProof.SubjectDigest != digestBytes(intentProof.Subject) || intentProof.Loss {
				return ErrInvalidStore
			}
			var nonceAction string
			if db.QueryRow(`SELECT action FROM owner_nonce_uses WHERE domain_id=? AND nonce=?`, st.domain, fmt.Sprintf("%x", intentProof.Nonce)).Scan(&nonceAction) != nil || nonceAction != "activation-intent" {
				return ErrInvalidStore
			}
			if artifactProductDigest(rel) != rd {
				return ErrInvalidStore
			}
			var rw signedArtifact
			if artifactDecoder.Unmarshal(rel, &rw) != nil || rw.Kind != "authority-relinquishment" || rw.PayloadSchema != "wipd.authority-relinquishment/1" || rw.Predecessor == nil || *rw.Predecessor != bd {
				return ErrInvalidStore
			}
			var rp struct {
				Schema string `cbor:"schema"`
				Domain string `cbor:"domain_id"`
				Epoch  uint64 `cbor:"authority_epoch"`
				Bundle string `cbor:"bundle_manifest_digest"`
				Head   string `cbor:"artifact_chain_head"`
				At     string `cbor:"quiesced_at"`
				Closed bool   `cbor:"admission_closed"`
			}
			if closedPayload(rw.Payload, &rp, "schema", "domain_id", "authority_epoch", "bundle_manifest_digest", "artifact_chain_head", "quiesced_at", "admission_closed") != nil || rp.Schema != "wipd.authority-relinquishment/1" || rp.Domain != st.domain || rp.Epoch != bp.Epoch || rp.Bundle != bd || rp.Head != bd || !rp.Closed {
				return ErrInvalidStore
			}
			if _, err = utcTime(rp.At); err != nil {
				return ErrInvalidStore
			}
			tx, e := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if e != nil {
				return e
			}
			recovered, blobDigest, entries, e := bundleClosure(context.Background(), tx, root, st.domain, bp.Epoch)
			_ = tx.Rollback()
			if e != nil {
				return e
			}
			if recovered != prefix || blobDigest != bp.BlobManifestDigest || !equalBundleEntries(entries, bp.Entries) {
				return ErrInvalidStore
			}
			continue
		}
		if active != bp.Epoch+1 || ae != nil || oe != nil || !st.closedEpoch.Valid {
			return ErrInvalidStore
		}
		if artifactProductDigest(finalOwner) != ownerDigest || artifactProductDigest(activation) != activationDigest {
			return ErrInvalidStore
		}
		var finalWrapper signedArtifact
		if artifactDecoder.Unmarshal(finalOwner, &finalWrapper) != nil || finalWrapper.Kind != "owner-attestation" {
			return ErrInvalidStore
		}
		ownerPayload, e := ownerArtifact(finalOwner, owner, st.domain, ownerID, "owner-attestation", "wipd.owner-attestation/1", finalWrapper.Epoch)
		if e != nil {
			return ErrInvalidStore
		}
		var finalProof ownerAttestation
		if closedPayload(ownerPayload, &finalProof, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || finalProof.Current != bp.Epoch || finalProof.Next == nil || *finalProof.Next != active || finalProof.SubjectDigest != digestBytes(finalProof.Subject) {
			return ErrInvalidStore
		}
		if planned {
			if finalProof.Action != "planned-handoff" || finalProof.Loss || finalProof.SubjectSchema != "wipd.planned-handoff-subject/1" {
				return ErrInvalidStore
			}
		} else if finalProof.Action != "disaster-restore" || !finalProof.Loss || finalProof.SubjectSchema != "wipd.disaster-restore-subject/1" {
			return ErrInvalidStore
		}
		var nonceAction string
		if db.QueryRow(`SELECT action FROM owner_nonce_uses WHERE domain_id=? AND nonce=?`, st.domain, fmt.Sprintf("%x", finalProof.Nonce)).Scan(&nonceAction) != nil || nonceAction != finalProof.Action {
			return ErrInvalidStore
		}
		var aw signedArtifact
		if artifactDecoder.Unmarshal(activation, &aw) != nil || aw.Kind != "authority-activation" || aw.PayloadSchema != "wipd.authority-activation/1" {
			return ErrInvalidStore
		}
		var ap struct {
			Schema       string         `cbor:"schema"`
			Domain       string         `cbor:"domain_id"`
			Previous     uint64         `cbor:"previous_epoch"`
			Epoch        uint64         `cbor:"authority_epoch"`
			OwnerDigest  string         `cbor:"owner_attestation_digest"`
			BundleDigest string         `cbor:"installed_bundle_or_proof_digest"`
			Prefix       map[string]any `cbor:"installed_prefix"`
			At           string         `cbor:"activated_at"`
		}
		if closedPayload(aw.Payload, &ap, "schema", "domain_id", "previous_epoch", "authority_epoch", "owner_attestation_digest", "installed_bundle_or_proof_digest", "installed_prefix", "activated_at") != nil || ap.Schema != "wipd.authority-activation/1" || ap.Domain != st.domain || ap.Previous != bp.Epoch || ap.Epoch != active || ap.OwnerDigest != artifactProductDigest(finalOwner) || ap.BundleDigest != bd || !prefixMapEqual(ap.Prefix, prefix) {
			return ErrInvalidStore
		}
		if _, err = utcTime(ap.At); err != nil {
			return ErrInvalidStore
		}
		var from, to uint64
		var prior, promotion string
		if db.QueryRow(`SELECT from_epoch,to_epoch,prior_fence_digest,promotion_proof_digest FROM epoch_promotions WHERE domain_id=? AND to_epoch=?`, st.domain, active).Scan(&from, &to, &prior, &promotion) != nil || from != bp.Epoch || to != active || promotion != activationDigest || (!planned && prior != bd) || (planned && prior != rd) {
			return ErrInvalidStore
		}
	}
	return nil
}

func validateBundlePayload(raw []byte, p *bundlePayload) error {
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !closedCBOR(fields["prefix"], "event_count", "high_water_event_id", "prefix_digest") {
		return ErrInvalidProof
	}
	var entries []cbor.RawMessage
	if artifactDecoder.Unmarshal(fields["entries"], &entries) != nil || len(entries) != len(p.Entries) {
		return ErrInvalidProof
	}
	for _, rawEntry := range entries {
		if !closedCBOR(rawEntry, "kind", "logical_name", "byte_length", "digest", "record_count") {
			return ErrInvalidProof
		}
	}
	for i, e := range p.Entries {
		if bundleTableKinds[e.LogicalName] != e.Kind || !validDigest(e.Digest) || (i > 0 && (p.Entries[i-1].Kind > e.Kind || (p.Entries[i-1].Kind == e.Kind && p.Entries[i-1].LogicalName >= e.LogicalName))) {
			return ErrInvalidProof
		}
	}
	return nil
}

func equalBundleEntries(a, b []bundleEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
