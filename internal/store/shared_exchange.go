package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExchangeBinding is explicit local configuration, never inferred from Git or
// a remote URL. Endpoint includes /api/organizations/{org}/repos/{repo}.
type ExchangeBinding struct {
	LocalRepo, Endpoint, InstallationID string
	Connection, Account                 string
	AssociationID                       string
}

// ExchangeChange v1 retains exact source JSON as a string. Authenticated uploader
// and receipt metadata are server observations, not original actor/time claims.
type ExchangeChange struct {
	Version        int    `json:"version"`
	InstallationID string `json:"installation_id"`
	GroupID        string `json:"group_id"`
	SourceRepoID   string `json:"source_repo_id"`
	Source         string `json:"source"`
	Sequence       int64  `json:"sequence,omitempty"`
	UploaderID     int64  `json:"uploader_id,omitempty"`
	ReceivedAt     string `json:"received_at,omitempty"`
}

// BindExchange initializes only opt-in disposable lab tables. Rebinding is an
// error: metadata changes must never switch/merge native Repo identities.
func (s *Store) BindExchange(ctx context.Context, repo, endpoint string) (ExchangeBinding, error) {
	return s.bindExchange(ctx, repo, endpoint, nil)
}

func (s *Store) bindExchange(ctx context.Context, repo, endpoint string, boundary *string, identity ...string) (ExchangeBinding, error) {
	if _, err := s.Repo(ctx, repo); err != nil {
		return ExchangeBinding{}, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ExchangeBinding{}, fmt.Errorf("exchange: invalid explicit endpoint")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if err := s.EnableSharedStoreExperiment(ctx); err != nil {
		return ExchangeBinding{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExchangeBinding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureExchangeReset(ctx, tx); err != nil {
		return ExchangeBinding{}, err
	}
	if len(identity) >= 2 {
		if err := exchangeConnectionResetting(ctx, tx, identity[0]); err != nil {
			return ExchangeBinding{}, err
		}
	}
	if len(identity) >= 3 {
		generation, err := exchangeGeneration(ctx, tx, repo)
		if err != nil {
			return ExchangeBinding{}, err
		}
		if generation != identity[2] {
			return ExchangeBinding{}, fmt.Errorf("exchange: association changed during pairing; retry explicitly")
		}
	}
	if len(identity) >= 4 {
		generation, err := exchangeConnectionGeneration(ctx, tx, identity[0])
		if err != nil {
			return ExchangeBinding{}, err
		}
		if generation != identity[3] {
			return ExchangeBinding{}, fmt.Errorf("exchange: connection forgotten during pairing; retry explicitly")
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_installation (singleton INTEGER PRIMARY KEY CHECK(singleton=1), id TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS exchange_bindings (repo TEXT PRIMARY KEY REFERENCES repos(id), endpoint TEXT NOT NULL, cursor INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS exchange_exports (repo TEXT NOT NULL REFERENCES exchange_bindings(repo), group_id TEXT NOT NULL, envelope TEXT NOT NULL, PRIMARY KEY(repo,group_id));
	CREATE TABLE IF NOT EXISTS exchange_inbox (repo TEXT NOT NULL REFERENCES exchange_bindings(repo), sequence INTEGER NOT NULL, envelope TEXT NOT NULL, applied INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(repo,sequence));`)
	if err != nil {
		return ExchangeBinding{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ExchangeBinding{}, err
	}
	random[6] = (random[6] & 15) | 64
	random[8] = (random[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", random[0:4], random[4:6], random[6:8], random[8:10], random[10:16])
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_installation VALUES(1,?)`, id); err != nil {
		return ExchangeBinding{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_bindings(repo,endpoint) VALUES(?,?)`, repo, endpoint); err != nil {
		return ExchangeBinding{}, err
	}
	b := ExchangeBinding{LocalRepo: repo}
	if err := tx.QueryRowContext(ctx, `SELECT b.endpoint,i.id FROM exchange_bindings b CROSS JOIN exchange_installation i WHERE b.repo=?`, repo).Scan(&b.Endpoint, &b.InstallationID); err != nil {
		return b, err
	}
	if b.Endpoint != endpoint {
		return b, fmt.Errorf("exchange: binding is immutable")
	}
	b.AssociationID, b.InstallationID, err = installExchangeAssociation(ctx, tx, repo, b.InstallationID)
	if err != nil {
		return b, err
	}
	// Additive opt-in metadata: legacy rows, source keys and all transfer state
	// remain in place. Reading an older store does not initialize this table.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_accounts (
	 repo TEXT PRIMARY KEY REFERENCES exchange_bindings(repo), version INTEGER NOT NULL,
	 connection TEXT NOT NULL, account TEXT NOT NULL)`); err != nil {
		return b, err
	}
	if len(identity) >= 2 {
		if identity[0] == "" || identity[1] == "" {
			return b, fmt.Errorf("exchange: connection and verified account required")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_accounts VALUES(?,1,?,?)`, repo, identity[0], identity[1]); err != nil {
			return b, err
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT version,connection,account FROM exchange_accounts WHERE repo=?`, repo).Scan(&version, &b.Connection, &b.Account); err != nil {
			return b, err
		}
		if version != 1 || b.Connection != identity[0] || b.Account != identity[1] {
			return b, fmt.Errorf("exchange: connection/account is immutable")
		}
	}
	if boundary != nil {
		_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_cli (
		 repo TEXT PRIMARY KEY REFERENCES exchange_bindings(repo), version INTEGER NOT NULL,
		 boundary TEXT NOT NULL, active INTEGER NOT NULL);
		 CREATE TABLE IF NOT EXISTS exchange_upload_ack (
		 repo TEXT NOT NULL REFERENCES exchange_bindings(repo), group_id TEXT NOT NULL,
		 PRIMARY KEY(repo,group_id));`)
		if err != nil {
			return b, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_cli VALUES(?,1,?,1)`, repo, *boundary); err != nil {
			return b, err
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT version FROM exchange_cli WHERE repo=?`, repo).Scan(&version); err != nil {
			return b, err
		}
		if version != 1 {
			return b, fmt.Errorf("exchange: unsupported pairing version %d", version)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE exchange_cli SET active=1 WHERE repo=?`, repo); err != nil {
			return b, err
		}
	}
	return b, tx.Commit()
}

// ExchangeClient is deliberately explicit, with no startup hooks or daemon.
// Tokens remain in caller memory, not local binding rows or exported source.
type ExchangeClient struct {
	HTTP  *http.Client
	Token string
}

func (c ExchangeClient) request(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	// Never forward a bearer token to redirects outside the explicit binding.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("exchange: HTTP %d", resp.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

// ExchangeBlobError is a non-2xx blob endpoint response. Status preserves the
// contract's distinct outcomes — 404 absent-or-unauthorized, 409
// declared-length conflict, 413/422 body rejected — for callers that must
// tell them apart.
type ExchangeBlobError struct {
	Status int
}

func (e ExchangeBlobError) Error() string {
	return fmt.Sprintf("exchange: blob HTTP %d", e.Status)
}

// errExchangeBlobUnavailable marks the blob endpoint's indistinguishable
// absent-or-unauthorized 404: pending work legitimately waits on bytes that
// have not arrived, so it is a wait state, never a pass failure.
var errExchangeBlobUnavailable = errors.New("exchange: blob unavailable")

// blobRequest is request's binary sibling: same bearer auth, base URL and
// no-redirect policy, but an octet-stream body the caller streams rather than
// a JSON document the client marshals.
func (c ExchangeClient) blobRequest(ctx context.Context, method, endpoint string, body io.Reader, length int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = length
	}
	// Never forward a bearer token to redirects outside the explicit binding.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

// UploadBlob stores one content-addressed blob: PUT {endpoint}/blobs/{digest}
// with the declared length as a query parameter and the raw bytes as the
// body. 200 and 201 are both success — the server re-verifies bytes on an
// exact retry, so callers need not distinguish first store from replay.
func (c ExchangeClient) UploadBlob(ctx context.Context, endpoint, digest string, length int64, body io.Reader) error {
	resp, err := c.blobRequest(ctx, http.MethodPut,
		fmt.Sprintf("%s/blobs/%s?length=%d", endpoint, digest, length), body, length)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return nil
	}
	return ExchangeBlobError{Status: resp.StatusCode}
}

// FetchBlob downloads one content-addressed blob, returning the body for the
// caller to verify and install. A 404 — absent or unauthorized, which the
// contract makes indistinguishable — reports errExchangeBlobUnavailable; a
// present X-Blob-SHA256 header naming a different digest is a protocol
// violation and fails the call.
func (c ExchangeClient) FetchBlob(ctx context.Context, endpoint, digest string) (io.ReadCloser, error) {
	resp, err := c.blobRequest(ctx, http.MethodGet, endpoint+"/blobs/"+digest, nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, errExchangeBlobUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, ExchangeBlobError{Status: resp.StatusCode}
	}
	if got := resp.Header.Get("X-Blob-SHA256"); got != "" && got != digest {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("exchange: blob header digest %q does not match %q", got, digest)
	}
	return resp.Body, nil
}

// ExchangePass uploads requested committed originals, downloads committed pages
// to a durable inbox+cursor, then applies/rechecks locally. No HTTP call holds a
// local writer transaction. A crash between receipt and application is safe.
func (s *Store) ExchangePass(ctx context.Context, c ExchangeClient, b ExchangeBinding, groups []string) error {
	if err := checkExchangeBinding(ctx, s.db, b); err != nil {
		return err
	}
	if err := c.request(ctx, "POST", b.Endpoint+"/origins", map[string]string{"installation_id": b.InstallationID}, nil); err != nil {
		return err
	}
	for _, group := range groups {
		change, err := s.exchangeExport(ctx, b, group)
		if err != nil {
			return err
		}
		// Spilled bytes must be durable on the server before the change that
		// names them: a blob failure aborts here so nothing ack-able uploads
		// without its content.
		if err := s.exchangeUploadChangeBlobs(ctx, c, b, change); err != nil {
			return err
		}
		if err := c.request(ctx, "POST", b.Endpoint+"/changes", map[string]any{"changes": []ExchangeChange{change}}, nil); err != nil {
			return err
		}
	}
	for {
		if err := checkExchangeBinding(ctx, s.db, b); err != nil {
			return err
		}
		var after int64
		if err := s.db.QueryRowContext(ctx, `SELECT cursor FROM exchange_bindings WHERE repo=? AND endpoint=?`, b.LocalRepo, b.Endpoint).Scan(&after); err != nil {
			return err
		}
		var page struct {
			Changes []ExchangeChange `json:"changes"`
			Cursor  int64            `json:"cursor"`
		}
		if err := c.request(ctx, "GET", fmt.Sprintf("%s/changes?after=%d", b.Endpoint, after), nil, &page); err != nil {
			return err
		}
		if len(page.Changes) == 0 {
			if page.Cursor != after {
				return fmt.Errorf("exchange: empty page changed cursor")
			}
			break
		}
		if err := s.receiveExchangePage(ctx, b, after, page.Cursor, page.Changes); err != nil {
			return err
		}
	}
	// Pending groups' spilled bytes are fetched and verified-installed before
	// the recheck below flips them: receipt is already durable, so a fetch
	// failure fails this pass without losing inbox rows.
	if err := s.exchangeFetchBlobs(ctx, c, b); err != nil {
		return err
	}
	return s.ApplyExchangeInbox(ctx, b)
}

func (s *Store) exchangeExport(ctx context.Context, b ExchangeBinding, group string) (ExchangeChange, error) {
	if err := checkExchangeBinding(ctx, s.db, b); err != nil {
		return ExchangeChange{}, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, b.LocalRepo, group).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		var source ExperimentSource
		source, err = s.ExperimentExport(ctx, b.InstallationID, group)
		if err != nil {
			return ExchangeChange{}, err
		}
		// A mixed command group uploads only its portable unit, separately
		// identified as a derivative of the raw group. No caller, including
		// an explicit ExchangePass, may upload local-only source evidence.
		unit, portable := experimentPortableUnit(source, b.LocalRepo)
		if !portable {
			return ExchangeChange{}, fmt.Errorf("exchange: source group has no portable unit")
		}
		source = unit
		for _, a := range source.Actions {
			if a.Event.Repo != b.LocalRepo {
				return ExchangeChange{}, fmt.Errorf("exchange: source outside binding")
			}
		}
		var encoded []byte
		encoded, err = json.Marshal(source)
		if err != nil {
			return ExchangeChange{}, err
		}
		change := ExchangeChange{Version: 1, InstallationID: b.InstallationID, GroupID: group, SourceRepoID: b.LocalRepo, Source: string(encoded)}
		encoded, err = json.Marshal(change)
		if err != nil {
			return ExchangeChange{}, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return ExchangeChange{}, err
		}
		defer func() { _ = tx.Rollback() }()
		if err := checkExchangeBinding(ctx, tx, b); err != nil {
			return ExchangeChange{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_exports VALUES(?,?,?)`, b.LocalRepo, group, string(encoded)); err != nil {
			return ExchangeChange{}, err
		}
		if err := tx.Commit(); err != nil {
			return ExchangeChange{}, err
		}
		// A concurrent pass may have captured earlier correspondence evidence.
		if err := s.db.QueryRowContext(ctx, `SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, b.LocalRepo, group).Scan(&raw); err != nil {
			return ExchangeChange{}, err
		}
	}
	if err != nil {
		return ExchangeChange{}, err
	}
	if err := checkExchangeBinding(ctx, s.db, b); err != nil {
		return ExchangeChange{}, err
	}
	var change ExchangeChange
	if err := json.Unmarshal([]byte(raw), &change); err != nil {
		return ExchangeChange{}, err
	}
	// Old captures may contain excluded actions, including in Derivative.
	// Never replay those bytes or silently rewrite an immutable retry: the
	// server might already have committed the original before a lost reply.
	var source ExperimentSource
	if err := json.Unmarshal([]byte(change.Source), &source); err != nil {
		return ExchangeChange{}, err
	}
	unit, portable := experimentPortableUnit(source, b.LocalRepo)
	if !portable || len(unit.Actions) != len(source.Actions) || (source.Derivative != nil && len(source.Derivative.Omitted) != 0) {
		return ExchangeChange{}, fmt.Errorf("exchange: cached envelope contains local-only evidence; refusing to transmit or rewrite immutable source group %s", group)
	}
	return change, nil
}

// exchangeDigestShaped reports whether s is a lowercase sha256 hex digest —
// the only form the blob routes accept, checked client-side before it is ever
// placed in a URL.
func exchangeDigestShaped(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// exchangeSpilledPayload decodes one action payload as ContentWritten and
// reports the admitted spill shape: bytes absent, blob reference equal to the
// identity-shaped content id, a well-formed digest, and a length inside the
// blob route's whole-object cap — anything larger can never be installed, so
// its bytes are never transferred. It is deliberately the classifier's
// storage shape and nothing more — payloads outside it are retained evidence
// whose blobs sync-content can never install.
func exchangeSpilledPayload(raw json.RawMessage) (ContentWritten, bool) {
	var p ContentWritten
	if json.Unmarshal(raw, &p) != nil {
		return ContentWritten{}, false
	}
	if p.Bytes != nil || p.BlobRef == "" || p.BlobRef != p.Content ||
		!IsIdentityShaped(p.Content) || !exchangeDigestShaped(p.SHA256) ||
		p.ByteLen > exchangeBlobMaxBytes {
		return ContentWritten{}, false
	}
	return p, true
}

// exchangeSpilledRefs collects the unique spilled blob references one source
// carries, in first-seen order. Identical bytes written under two content
// identities share one digest — dedupe happens per digest at upload, while
// the reference stays per content row.
func exchangeSpilledRefs(source ExperimentSource) []ContentWritten {
	var out []ContentWritten
	seen := map[string]bool{}
	for _, a := range source.Actions {
		p, ok := exchangeSpilledPayload(a.Event.Payload)
		if !ok || seen[p.BlobRef] {
			continue
		}
		seen[p.BlobRef] = true
		out = append(out, p)
	}
	return out
}

// exchangeUploadChangeBlobs uploads every spilled blob one exported change
// names, before its changes POST makes the group receivable.
func (s *Store) exchangeUploadChangeBlobs(ctx context.Context, c ExchangeClient, b ExchangeBinding, change ExchangeChange) error {
	var source ExperimentSource
	if err := json.Unmarshal([]byte(change.Source), &source); err != nil {
		return err
	}
	return s.exchangeUploadBlobs(ctx, c, b, source)
}

// exchangeUploadBlobs PUTs each unique spilled digest once per call; the
// server's idempotent store makes pass retries safe. Bytes come from
// SegmentBytes, which verifies them against the source content row, and are
// re-checked against the payload's own length and digest before upload — a
// source whose recorded metadata disagrees with its bytes fails the pass
// rather than publishing evidence that can never verify.
func (s *Store) exchangeUploadBlobs(ctx context.Context, c ExchangeClient, b ExchangeBinding, source ExperimentSource) error {
	if err := checkExchangeBinding(ctx, s.db, b); err != nil {
		return err
	}
	needs := map[string]ContentWritten{}
	var digests []string
	for _, p := range exchangeSpilledRefs(source) {
		if _, ok := needs[p.SHA256]; !ok {
			needs[p.SHA256] = p
			digests = append(digests, p.SHA256)
		}
	}
	for _, digest := range digests {
		p := needs[digest]
		data, err := s.SegmentBytes(ctx, p.Content)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != p.ByteLen || hex.EncodeToString(sum[:]) != digest {
			return fmt.Errorf("exchange: spilled content %s does not match its recorded length/digest", p.Content)
		}
		if err := c.UploadBlob(ctx, b.Endpoint, digest, p.ByteLen, bytes.NewReader(data)); err != nil {
			return fmt.Errorf("exchange: upload blob %s: %w", digest, err)
		}
	}
	return nil
}

// exchangeBlobMaxBytes is the Cloud whole-object cap. A response larger than
// this can never be installed — the upload side could never have stored it —
// so the read is bounded and the excess simply fails verification.
const exchangeBlobMaxBytes = 32 << 20

// exchangeBlobNeed is one pending payload's install requirement: the local
// sidecar name and declared length for one content digest.
type exchangeBlobNeed struct {
	ref     string
	byteLen int64
}

// exchangeFetchBlobs installs verified bytes for every pending group's
// spilled content. It enumerates retained decisions — the same set
// ApplyExchangeInbox rechecks — so blobs owed by earlier passes or an
// offline reopen heal on the next pass, not only blobs named by this pass's
// pages. A 404 leaves the group pending on "content bytes unavailable"; any
// other fetch error fails the pass, matching how page and upload errors
// propagate, so cursor state stays honest.
func (s *Store) exchangeFetchBlobs(ctx context.Context, c ExchangeClient, b ExchangeBinding) error {
	if err := checkExchangeBinding(ctx, s.db, b); err != nil {
		return err
	}
	decisions, err := experimentActivities(ctx, s.db, false, b.LocalRepo)
	if err != nil {
		return err
	}
	// needs[digest] collects every pending ref expecting those bytes;
	// identical content under two identities legitimately shares one digest.
	// One ref names one payload — the first retained claim wins, so forged
	// duplicates can never make two digests fight over one sidecar name.
	needs := map[string][]exchangeBlobNeed{}
	claimed := map[string]bool{}
	for _, d := range decisions {
		if d.Disposition != "pending" {
			continue
		}
		for _, a := range d.Source.Actions {
			p, ok := exchangeSpilledPayload(a.Event.Payload)
			if !ok || claimed[p.BlobRef] {
				continue
			}
			claimed[p.BlobRef] = true
			state, err := s.spilledContentState(p.BlobRef, p.ByteLen, p.SHA256)
			if err != nil {
				return err
			}
			if state == "ready" {
				continue
			}
			needs[p.SHA256] = append(needs[p.SHA256], exchangeBlobNeed{ref: p.BlobRef, byteLen: p.ByteLen})
		}
	}
	digests := make([]string, 0, len(needs))
	for digest := range needs {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		if err := s.exchangeInstallBlob(ctx, c, b, digest, needs[digest]); err != nil {
			return err
		}
	}
	return nil
}

// exchangeInstallBlob downloads one digest and installs it under every ref
// that needs it. Bytes that fail the payload's length/digest verification
// are never written: the group simply stays pending, exactly as if the blob
// were absent.
func (s *Store) exchangeInstallBlob(ctx context.Context, c ExchangeClient, b ExchangeBinding, digest string, needs []exchangeBlobNeed) error {
	body, err := c.FetchBlob(ctx, b.Endpoint, digest)
	if errors.Is(err, errExchangeBlobUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, exchangeBlobMaxBytes+1))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) > exchangeBlobMaxBytes || hex.EncodeToString(sum[:]) != digest {
		return nil
	}
	for _, need := range needs {
		if int64(len(data)) != need.byteLen {
			continue
		}
		if err := s.installExchangeBlob(need.ref, data, need.byteLen, digest); err != nil {
			return err
		}
	}
	return nil
}

// installExchangeBlob writes verified bytes to the store-owned sidecar path
// atomically: a temp file in the same directory, then a hard link into place
// so an existing name is never clobbered — not even by a racing installer.
// A file that already verifies is left alone; one that does not is local
// damage the classifier already reports as "invalid", and conservative sync
// never overwrites it — ReapOrphanBlobs is the repair path, since a pending
// group's blob is unreferenced by any content row.
func (s *Store) installExchangeBlob(ref string, data []byte, byteLen int64, digest string) error {
	if err := os.MkdirAll(s.blobDir, 0o700); err != nil {
		return fmt.Errorf("store: preparing the blob directory %s: %w", s.blobDir, err)
	}
	state, err := s.spilledContentState(ref, byteLen, digest)
	if err != nil {
		return err
	}
	if state != "missing" {
		return nil
	}
	tmp, err := os.CreateTemp(s.blobDir, ".exchange-blob-")
	if err != nil {
		return fmt.Errorf("store: staging blob %s: %w", ref, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: staging blob %s: %w", ref, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: staging blob %s: %w", ref, err)
	}
	if err := os.Link(tmpName, filepath.Join(s.blobDir, ref)); err != nil {
		if os.IsExist(err) {
			return nil // a concurrent verified install won; its bytes stand
		}
		return fmt.Errorf("store: installing blob %s: %w", ref, err)
	}
	return nil
}

func (s *Store) receiveExchangePage(ctx context.Context, b ExchangeBinding, after, cursor int64, changes []ExchangeChange) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkExchangeBinding(ctx, tx, b); err != nil {
		return err
	}
	for _, change := range changes {
		if change.Version != 1 || change.Sequence != after+1 {
			return fmt.Errorf("exchange: unsupported version or noncontiguous page")
		}
		var source ExperimentSource
		if err := json.Unmarshal([]byte(change.Source), &source); err != nil {
			return err
		}
		if source.Host != change.InstallationID || len(source.Actions) == 0 || source.Actions[0].Event.ID != change.GroupID {
			return fmt.Errorf("exchange: invalid envelope")
		}
		if source.Derivative != nil && source.Derivative.Parent != change.GroupID {
			return fmt.Errorf("exchange: invalid envelope")
		}
		for _, a := range source.Actions {
			if a.Event.Repo != change.SourceRepoID {
				return fmt.Errorf("exchange: source Repo mismatch")
			}
		}
		raw, err := json.Marshal(change)
		if err != nil {
			return err
		}
		var old string
		err = tx.QueryRowContext(ctx, `SELECT envelope FROM exchange_inbox WHERE repo=? AND sequence=?`, b.LocalRepo, change.Sequence).Scan(&old)
		if err == nil && old != string(raw) {
			return fmt.Errorf("exchange: committed feed changed")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exchange_inbox(repo,sequence,envelope) VALUES(?,?,?)`, b.LocalRepo, change.Sequence, string(raw)); err != nil {
			return err
		}
		after = change.Sequence
	}
	if cursor != after {
		return fmt.Errorf("exchange: cursor does not match durable page")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE exchange_bindings SET cursor=MAX(cursor,?) WHERE repo=? AND endpoint=?`, cursor, b.LocalRepo, b.Endpoint); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyExchangeInbox needs no server. Restarted processes finish receipt even
// while offline. Pending groups are reconsidered once after this incoming batch.
func (s *Store) ApplyExchangeInbox(ctx context.Context, b ExchangeBinding) error {
	if err := checkExchangeBinding(ctx, s.db, b); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,envelope FROM exchange_inbox WHERE repo=? AND applied=0 ORDER BY sequence`, b.LocalRepo)
	if err != nil {
		return err
	}
	type incoming struct {
		seq    int64
		change ExchangeChange
	}
	var incomingRows []incoming
	for rows.Next() {
		var seq int64
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var change ExchangeChange
		if err := json.Unmarshal([]byte(raw), &change); err != nil {
			_ = rows.Close()
			return err
		}
		incomingRows = append(incomingRows, incoming{seq, change})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, row := range incomingRows {
		var source ExperimentSource
		if err := json.Unmarshal([]byte(row.change.Source), &source); err != nil {
			return err
		}
		if _, err := s.experimentImport(ctx, source, b.LocalRepo, b); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = checkExchangeBinding(ctx, tx, b); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE exchange_inbox SET applied=1 WHERE repo=? AND sequence=?`, b.LocalRepo, row.seq)
		}
		if err == nil {
			err = tx.Commit()
		}
		_ = tx.Rollback()
		if err != nil {
			return err
		}
	}
	// Reconsider only canonical evidence mapped to this pairing. An explicit
	// pass must not apply pending work in another (possibly unpaired) Repo.
	decisions, err := experimentActivities(ctx, s.db, false, b.LocalRepo)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.Disposition == "pending" {
			if _, err := s.experimentRecheck(ctx, d.key, b); err != nil {
				return err
			}
		}
	}
	return nil
}
