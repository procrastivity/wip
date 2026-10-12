// Package schedulerhook implements a bounded Step 10 hook adapter. Work, Plan,
// and Ask use pinned content and deterministic transformations; Verifier checks
// use only the fixed offline runner against an immutable candidate snapshot.
// No adapter kind contacts a provider or reads a shared checkout.
package schedulerhook

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	// The adapter stores durable engagements in its private embedded SQLite ledger.
	_ "modernc.org/sqlite"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdprofile"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	// AdapterID names the bounded compiled adapter implemented by this package.
	AdapterID = "builtin.pinned-markdown"
	// AdapterVersion is the immutable protocol version for AdapterID.
	AdapterVersion       = uint16(1)
	databaseName         = "scheduler-hooks.sqlite"
	lockName             = "scheduler-hooks.lock"
	maxInputBytes        = int64(2 << 20)
	maxResultBytes       = 4 << 20
	maxVerifierBlobBytes = int64(4 << 20)
)

var (
	identityPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

	// ErrClosed reports use of an adapter after Close.
	ErrClosed = errors.New("schedulerhook: adapter is closed")
	// ErrConflict reports reuse of a domain/command key for changed hook intent.
	ErrConflict = errors.New("schedulerhook: command ID has different hook intent")
	// ErrNotFound reports a lookup for an engagement with no retained result.
	ErrNotFound = errors.New("schedulerhook: hook result not found")
	// ErrIndeterminate reports recovery when the ledger cannot prove a commit outcome.
	ErrIndeterminate = errors.New("schedulerhook: durable hook result is indeterminate")
	// ErrUnsupported reports an unimplemented hook kind, adapter, or revision.
	ErrUnsupported = errors.New("schedulerhook: unsupported hook kind or work revision")
	// ErrInvalidRequest reports a malformed or mismatched hook request.
	ErrInvalidRequest = errors.New("schedulerhook: invalid hook request")
	// ErrInvalidEvidence reports absent or invalid evidence for a useful result.
	ErrInvalidEvidence = errors.New("schedulerhook: invalid or missing retained evidence")
	// ErrCorrupt reports invalid durable engagement state.
	ErrCorrupt = errors.New("schedulerhook: durable hook ledger is corrupt")
)

// Kind identifies the supported scheduler-hook behavior.
type Kind string

const (
	// KindWork selects the bounded pinned-input work behavior.
	KindWork Kind = "work"
	// KindPlan selects the deterministic pinned-Workplan parser.
	KindPlan Kind = "plan"
	// KindAsk selects the bounded keyed policy-answer behavior.
	KindAsk Kind = "ask"
)

// WorkOutcome describes the result of deterministic work over pinned inputs.
type WorkOutcome string

const (
	// WorkCompleted and WorkFailed describe work completion status.
	WorkCompleted WorkOutcome = "completed"
	// WorkFailed indicates the pinned input could not produce a valid useful result.
	WorkFailed WorkOutcome = "failed"
	// WorkVerified indicates the pinned source passed the adapter's bounded checks.
	WorkVerified WorkOutcome = "verified"
	// WorkNotVerified indicates a bounded check did not establish the requested property.
	WorkNotVerified WorkOutcome = "not-verified"
)

// PlanOutcome describes whether a useful Plan was authored.
type PlanOutcome string

const (
	// PlanPlanned and PlanFailed describe Plan completion status.
	PlanPlanned PlanOutcome = "planned"
	// PlanFailed indicates the pinned Workplan could not produce a valid plan.
	PlanFailed PlanOutcome = "failed"
)

// Handling is the policy decision returned by Ask hooks.
type Handling string

const (
	// HandleSkip and HandleHalt are the supported Ask decisions.
	HandleSkip Handling = "skip"
	// HandleHalt directs the scheduler to halt on the keyed policy answer.
	HandleHalt Handling = "halt"
)

// PrefixAnchor is the exact installed authority prefix used by one engagement.
type PrefixAnchor struct {
	EventCount uint64  `cbor:"event_count"`
	EventID    *string `cbor:"high_water_event_id"`
	Digest     string  `cbor:"prefix_digest"`
}

// Claim is the exact claim identity at launch. A nil claim is valid only for
// Run-level Ask decisions.
type Claim struct {
	ID    string `cbor:"id"`
	Epoch uint64 `cbor:"epoch"`
}

// TargetClaim is one exact authority-fenced Matter claim in the hook intent.
type TargetClaim struct {
	MatterID string `cbor:"matter_id"`
	ClaimID  string `cbor:"claim_id"`
	Epoch    uint64 `cbor:"claim_epoch"`
}

// PinnedBlob carries bytes resolved by the authenticated Environment from a
// verified staged reference. Name, digest, and size are semantic; Bytes are
// checked against those pins and never interpreted as a path.
type PinnedBlob struct {
	Name   string
	Digest string
	Size   int64
	Bytes  []byte
}

// Request is a closed hook intent. DomainID is intentionally absent: Adapter
// binds that namespace when opened from the authenticated Environment profile.
// CommandID is the original scheduler command ID and is never rewritten here.
type Request struct {
	CommandID      string
	RequestHash    string
	Kind           Kind
	Version        uint16
	AdapterID      string
	AdapterVersion uint16
	RunID          string
	MatterID       string
	TargetID       string
	TargetKind     string
	Role           string
	RoleID         string
	EnvironmentID  string
	DispatchID     string
	Claim          *Claim
	TargetClaims   []TargetClaim
	Prefix         PrefixAnchor
	Revision       string
	Reason         string
	Contention     Handling
	Blocked        Handling
	Failed         Handling
	Inputs         []PinnedBlob
}

// StepSpec is one ordered action in a useful Plan result.
type StepSpec struct {
	Title string `cbor:"title" json:"title"`
}

// WorkResult reports deterministic work over the request's pinned inputs.
type WorkResult struct {
	Outcome      WorkOutcome `cbor:"outcome" json:"outcome"`
	Revision     string      `cbor:"revision" json:"revision"`
	EvidenceRefs []string    `cbor:"evidence_refs" json:"evidence_refs"`
	ProblemCode  string      `cbor:"problem_code" json:"problem_code"`
}

// PlanResult contains the ordered work items produced from a pinned Workplan.
type PlanResult struct {
	Outcome     PlanOutcome `cbor:"outcome" json:"outcome"`
	Steps       []StepSpec  `cbor:"steps" json:"steps"`
	ProblemCode string      `cbor:"problem_code" json:"problem_code"`
}

// AskResult contains the explicit policy handling returned for an Ask hook.
type AskResult struct {
	Handling Handling `cbor:"handling" json:"handling"`
}

// TypedResult contains exactly one kind-specific result.
type TypedResult struct {
	Kind    Kind        `cbor:"kind" json:"kind"`
	Version uint16      `cbor:"version" json:"version"`
	Work    *WorkResult `cbor:"work" json:"work"`
	Plan    *PlanResult `cbor:"plan" json:"plan"`
	Ask     *AskResult  `cbor:"ask" json:"ask"`
}

// Key identifies one engagement in the authenticated domain and original command.
type Key struct {
	DomainID  string `cbor:"domain_id" json:"domain_id"`
	CommandID string `cbor:"command_id" json:"command_id"`
}

// Result is the exact durable engagement product returned on launch and
// recovery. ResultDigest binds the result to the authenticated domain, the
// unchanged scheduler command ID, and its immutable input digest.
type Result struct {
	Key          Key         `cbor:"key" json:"key"`
	InputDigest  string      `cbor:"input_digest" json:"input_digest"`
	Typed        TypedResult `cbor:"typed_result" json:"typed_result"`
	ResultDigest string      `cbor:"result_digest" json:"result_digest"`
}

// RequestFromCommand reconstructs the immutable adapter intent from the
// canonical authority command. Blob bytes are deliberately omitted here so
// the authority can verify input identity without loading external content;
// the owning Environment fills them from its verified staged-blob journal.
func RequestFromCommand(command operation.Command) (Request, error) {
	input, ok := command.Request.Input.(operation.SchedulerHookInput)
	if command.Request.Operation != operation.SchedulerOrchestrateV1.Metadata().Operation || !ok {
		return Request{}, ErrUnsupported
	}
	request := Request{
		CommandID: command.ID, Kind: Kind(input.Kind), Version: uint16(input.Version), AdapterID: input.AdapterID,
		AdapterVersion: uint16(input.AdapterVersion), RunID: input.RunID, MatterID: input.MatterID, TargetID: input.TargetID,
		TargetKind: input.TargetKind,
		Role:       input.Role, RoleID: input.RoleID, EnvironmentID: command.EnvironmentID, DispatchID: input.DispatchID,
		Revision: input.Revision, Reason: input.Reason, Contention: Handling(input.Contention),
		Blocked: Handling(input.Blocked), Failed: Handling(input.Failed),
		Prefix: PrefixAnchor{
			EventCount: input.InstalledPrefix.EventCount, EventID: input.InstalledPrefix.EventID,
			Digest: input.InstalledPrefix.Digest,
		},
	}
	requestHash, err := command.RequestHash()
	if err != nil {
		return Request{}, ErrInvalidRequest
	}
	request.RequestHash = requestHash
	if command.Request.Claim != nil {
		epoch, parseErr := strconv.ParseUint(command.Request.Claim.Epoch, 10, 64)
		if parseErr != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != command.Request.Claim.Epoch {
			return Request{}, ErrInvalidRequest
		}
		request.Claim = &Claim{ID: command.Request.Claim.ID, Epoch: epoch}
	}
	request.TargetClaims = make([]TargetClaim, len(input.TargetClaims))
	for index, claim := range input.TargetClaims {
		request.TargetClaims[index] = TargetClaim{MatterID: claim.MatterID, ClaimID: claim.ClaimID, Epoch: claim.ClaimEpoch}
	}
	request.Inputs = make([]PinnedBlob, len(command.Request.Blobs))
	for index, blob := range command.Request.Blobs {
		request.Inputs[index] = PinnedBlob{Name: blob.Name, Digest: blob.Digest, Size: blob.Size}
	}
	return request, nil
}

type intentRecord struct {
	Schema         string        `cbor:"schema"`
	Key            Key           `cbor:"key"`
	RequestHash    string        `cbor:"request_hash"`
	Kind           Kind          `cbor:"kind"`
	Version        uint16        `cbor:"version"`
	AdapterID      string        `cbor:"adapter_id"`
	AdapterVersion uint16        `cbor:"adapter_version"`
	InputDigest    string        `cbor:"input_digest"`
	RunID          string        `cbor:"run_id"`
	MatterID       string        `cbor:"matter_id"`
	TargetID       string        `cbor:"target_id"`
	TargetKind     string        `cbor:"target_kind"`
	Role           string        `cbor:"role"`
	RoleID         *string       `cbor:"role_id"`
	EnvironmentID  string        `cbor:"owner_environment_id"`
	DispatchID     *string       `cbor:"original_dispatch_id"`
	Claim          *Claim        `cbor:"original_claim"`
	TargetClaims   []TargetClaim `cbor:"target_claims"`
	Prefix         PrefixAnchor  `cbor:"installed_prefix_anchor"`
}

// Adapter executes and durably retains bounded engagements for one authenticated Environment.
type Adapter struct {
	db                  *sql.DB
	profileRoot         string
	domainID            string
	environmentID       string
	afterCommit         func()
	afterVerifierChecks func()
}

// Open binds the ledger to an already-authenticated Environment journal.
// profileRoot is the explicitly configured isolated, domain-shared adapter
// profile; it must not come from a hook request.
func Open(profileRoot string, journal *wipdjournal.Journal) (*Adapter, error) {
	if journal == nil {
		return nil, ErrInvalidRequest
	}
	return openIdentity(profileRoot, journal.Identity())
}

func openIdentity(profileRoot string, identity wipdjournal.Identity) (*Adapter, error) {
	if !identityPattern.MatchString(identity.DomainID) || !identityPattern.MatchString(identity.EnvironmentID) ||
		!identityPattern.MatchString(identity.RepoID) || identity.AuthorityEpoch == 0 {
		return nil, ErrInvalidRequest
	}
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return nil, fmt.Errorf("schedulerhook: resolve authenticated Environment profile: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(profile.Root, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("schedulerhook: open initialization lock: %w", err)
	}
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		_ = lock.Close()
		return nil, fmt.Errorf("schedulerhook: unsafe initialization lock: %w", ErrCorrupt)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("schedulerhook: lock adapter profile: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	path := filepath.Join(profile.Root, databaseName)
	created, err := createPrivateDBFile(path)
	if err != nil {
		return nil, err
	}
	location := url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "busy_timeout(10000)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "journal_mode(DELETE)")
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schedulerhook: open ledger: %w", err)
	}
	if created {
		_, err = db.Exec(`CREATE TABLE hook_engagements (
			domain_id TEXT NOT NULL,
			command_id TEXT NOT NULL,
			intent BLOB NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('running','completed')),
			input_digest TEXT NOT NULL,
			result_digest TEXT,
			result BLOB,
			PRIMARY KEY(domain_id,command_id),
			CHECK((state='running' AND result_digest IS NULL AND result IS NULL) OR
				(state='completed' AND result_digest IS NOT NULL AND result IS NOT NULL))
		) STRICT, WITHOUT ROWID`)
		if err == nil {
			_, err = db.Exec(`CREATE TABLE hook_evidence (
			domain_id TEXT NOT NULL,
			digest TEXT NOT NULL,
			byte_length INTEGER NOT NULL CHECK(byte_length BETWEEN 0 AND 4194304),
			bytes BLOB NOT NULL,
			PRIMARY KEY(domain_id,digest)
		) STRICT, WITHOUT ROWID`)
		}
		if err == nil {
			_, err = db.Exec(`CREATE TABLE environment_hook_results (
			domain_id TEXT NOT NULL,
			environment_id TEXT NOT NULL,
			command_id TEXT NOT NULL,
			input_digest TEXT NOT NULL,
			result_digest TEXT NOT NULL,
			result BLOB NOT NULL,
			PRIMARY KEY(domain_id,environment_id,command_id)
		) STRICT, WITHOUT ROWID`)
		}
		if err == nil {
			err = createVerifierCheckTable(db)
		}
		if err == nil {
			_, err = db.Exec(`PRAGMA user_version=2`)
		}
	} else {
		var version int
		err = db.QueryRow(`PRAGMA user_version`).Scan(&version)
		if err == nil && version != 1 && version != 2 {
			err = ErrCorrupt
		}
		if err == nil {
			for _, table := range []string{"hook_engagements", "hook_evidence", "environment_hook_results"} {
				var one int
				err = db.QueryRow(`SELECT 1 FROM ` + table + ` LIMIT 1`).Scan(&one)
				if errors.Is(err, sql.ErrNoRows) {
					err = nil
				}
				if err != nil {
					break
				}
			}
		}
		if err == nil && version == 1 {
			err = createVerifierCheckTable(db)
			if err == nil {
				_, err = db.Exec(`PRAGMA user_version=2`)
			}
		}
		if err == nil {
			var one int
			err = db.QueryRow(`SELECT 1 FROM hook_check_runs LIMIT 1`).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schedulerhook: validate ledger schema: %w", err)
	}
	return &Adapter{db: db, profileRoot: profile.Root, domainID: identity.DomainID, environmentID: identity.EnvironmentID}, nil
}

func createVerifierCheckTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS hook_check_runs (
		domain_id TEXT NOT NULL,
		command_id TEXT NOT NULL,
		input_digest TEXT NOT NULL,
		check_id TEXT NOT NULL,
		record BLOB NOT NULL,
		PRIMARY KEY(domain_id,command_id,check_id)
	) STRICT, WITHOUT ROWID`)
	return err
}

func createPrivateDBFile(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if err = file.Close(); err != nil {
			return false, err
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return false, fmt.Errorf("schedulerhook: create private ledger: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("schedulerhook: unsafe ledger file: %w", ErrCorrupt)
	}
	return false, nil
}

// Close releases the adapter's SQLite ledger and profile lock.
func (a *Adapter) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	err := a.db.Close()
	a.db = nil
	return err
}

// StartOrGet atomically creates one useful typed engagement or returns its
// retained result. The first SQLite write serializes same-key starts across
// Adapter instances/processes. Pure computation and result/artifact persistence
// commit together: a pre-commit loss has no effect; post-commit loss recovers.
func (a *Adapter) StartOrGet(ctx context.Context, request Request) (Result, error) {
	if a == nil || a.db == nil {
		return Result{}, ErrClosed
	}
	if ctx == nil || !identityPattern.MatchString(request.CommandID) || !digestPattern.MatchString(request.RequestHash) ||
		!identityPattern.MatchString(request.RunID) ||
		!identityPattern.MatchString(request.MatterID) || !identityPattern.MatchString(request.TargetID) ||
		request.EnvironmentID != a.environmentID || !digestPattern.MatchString(request.Prefix.Digest) ||
		request.Prefix.EventCount == 0 && request.Prefix.EventID != nil ||
		request.Prefix.EventCount > 0 && (request.Prefix.EventID == nil || !identityPattern.MatchString(*request.Prefix.EventID)) {
		return Result{}, ErrInvalidRequest
	}
	inputDigest, err := hookInputDigest(request)
	if err != nil {
		return Result{}, err
	}
	key := Key{DomainID: a.domainID, CommandID: request.CommandID}
	intent, err := wipdwire.EncodeCanonical(intentRecord{
		Schema: "wipd.hook-intent/1", Key: key, RequestHash: request.RequestHash, Kind: request.Kind, Version: request.Version,
		AdapterID: request.AdapterID, AdapterVersion: request.AdapterVersion, InputDigest: inputDigest,
		RunID: request.RunID, MatterID: request.MatterID, TargetID: request.TargetID, TargetKind: request.TargetKind,
		Role: request.Role, RoleID: optionalIdentity(request.RoleID), EnvironmentID: request.EnvironmentID,
		DispatchID: optionalIdentity(request.DispatchID), Claim: cloneClaim(request.Claim),
		TargetClaims: append([]TargetClaim(nil), request.TargetClaims...), Prefix: clonePrefix(request.Prefix),
	})
	if err != nil {
		return Result{}, err
	}
	if request.Kind == KindWork && request.Role == "verifier" && request.Revision == operation.SchedulerStep10VerifierRevision {
		return a.startStep10Verifier(ctx, request, key, intent, inputDigest)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback() }()
	insert, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO hook_engagements
		(domain_id,command_id,intent,state,input_digest) VALUES(?,?,?,'running',?)`, key.DomainID, key.CommandID, intent, inputDigest)
	if err != nil {
		return Result{}, err
	}
	inserted, err := insert.RowsAffected()
	if err != nil {
		return Result{}, err
	}
	if inserted == 0 {
		var storedIntent, storedResult []byte
		var state, storedDigest, resultDigest string
		if err = tx.QueryRowContext(ctx, `SELECT intent,state,input_digest,coalesce(result_digest,''),result
			FROM hook_engagements WHERE domain_id=? AND command_id=?`, key.DomainID, key.CommandID).
			Scan(&storedIntent, &state, &storedDigest, &resultDigest, &storedResult); err != nil {
			return Result{}, err
		}
		if !bytes.Equal(storedIntent, intent) || storedDigest != inputDigest {
			return Result{}, ErrConflict
		}
		if state != "completed" {
			return Result{}, ErrIndeterminate
		}
		result, err := decodeResult(key, inputDigest, resultDigest, storedResult)
		if err != nil {
			return Result{}, err
		}
		if err = tx.Commit(); err != nil {
			return Result{}, err
		}
		return result, nil
	}
	if err = validateRequest(request); err != nil {
		return Result{}, err
	}
	typed, artifacts, err := compute(request)
	if err != nil {
		return Result{}, err
	}
	for digest, content := range artifacts {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hook_evidence(domain_id,digest,byte_length,bytes)
			VALUES(?,?,?,?)`, key.DomainID, digest, len(content), content); err != nil {
			return Result{}, err
		}
		var stored []byte
		var size int
		if err = tx.QueryRowContext(ctx, `SELECT byte_length,bytes FROM hook_evidence WHERE domain_id=? AND digest=?`,
			key.DomainID, digest).Scan(&size, &stored); err != nil || size != len(content) || !bytes.Equal(stored, content) {
			return Result{}, ErrCorrupt
		}
	}
	resultDigest, err := hookResultDigest(key, inputDigest, typed)
	if err != nil {
		return Result{}, err
	}
	resultBytes, err := wipdwire.EncodeCanonical(typed)
	if err != nil {
		return Result{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE hook_engagements SET state='completed',result_digest=?,result=?
		WHERE domain_id=? AND command_id=? AND state='running'`, resultDigest, resultBytes, key.DomainID, key.CommandID)
	if err != nil {
		return Result{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return Result{}, ErrCorrupt
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	if a.afterCommit != nil {
		a.afterCommit()
	}
	return Result{Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest}, nil
}

// StartOrInstall is the Environment-facing recovery boundary. It first
// recovers or starts the adapter engagement under the original command ID,
// then installs that exact result in a separate durable transaction. If the
// process dies after the adapter commit but before this installation, retry
// obtains the retained adapter result and installs it without re-engagement.
func (a *Adapter) StartOrInstall(ctx context.Context, request Request) (Result, error) {
	result, err := a.StartOrGet(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if err := a.installEnvironmentResult(ctx, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (a *Adapter) installEnvironmentResult(ctx context.Context, result Result) error {
	if a == nil || a.db == nil {
		return ErrClosed
	}
	if ctx == nil || result.Key.DomainID != a.domainID ||
		!identityPattern.MatchString(result.Key.CommandID) || !digestPattern.MatchString(result.InputDigest) ||
		!validTypedResult(result.Typed) {
		return ErrInvalidRequest
	}
	computed, err := hookResultDigest(result.Key, result.InputDigest, result.Typed)
	if err != nil || computed != result.ResultDigest {
		return ErrCorrupt
	}
	engagement, err := a.Lookup(ctx, result.Key.CommandID)
	if err != nil {
		return err
	}
	if engagement.InputDigest != result.InputDigest || engagement.ResultDigest != result.ResultDigest {
		return ErrConflict
	}
	resultBytes, err := wipdwire.EncodeCanonical(result.Typed)
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO environment_hook_results
		(domain_id,environment_id,command_id,input_digest,result_digest,result) VALUES(?,?,?,?,?,?)`,
		a.domainID, a.environmentID, result.Key.CommandID, result.InputDigest, result.ResultDigest, resultBytes); err != nil {
		return err
	}
	var inputDigest, resultDigest string
	var stored []byte
	if err = tx.QueryRowContext(ctx, `SELECT input_digest,result_digest,result FROM environment_hook_results
		WHERE domain_id=? AND environment_id=? AND command_id=?`, a.domainID, a.environmentID, result.Key.CommandID).
		Scan(&inputDigest, &resultDigest, &stored); err != nil {
		return err
	}
	if inputDigest != result.InputDigest || resultDigest != result.ResultDigest || !bytes.Equal(stored, resultBytes) {
		return ErrConflict
	}
	return tx.Commit()
}

// LookupInstalled returns only the exact result already installed by this
// Environment. Adapter-level retention alone does not imply local installation.
func (a *Adapter) LookupInstalled(ctx context.Context, commandID string) (Result, error) {
	if a == nil || a.db == nil {
		return Result{}, ErrClosed
	}
	if ctx == nil || !identityPattern.MatchString(commandID) {
		return Result{}, ErrInvalidRequest
	}
	key := Key{DomainID: a.domainID, CommandID: commandID}
	var inputDigest, resultDigest string
	var resultBytes []byte
	err := a.db.QueryRowContext(ctx, `SELECT input_digest,result_digest,result FROM environment_hook_results
		WHERE domain_id=? AND environment_id=? AND command_id=?`, a.domainID, a.environmentID, commandID).
		Scan(&inputDigest, &resultDigest, &resultBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	installed, err := decodeResult(key, inputDigest, resultDigest, resultBytes)
	if err != nil {
		return Result{}, err
	}
	engagement, err := a.Lookup(ctx, commandID)
	if err != nil {
		return Result{}, ErrCorrupt
	}
	if engagement.InputDigest != installed.InputDigest || engagement.ResultDigest != installed.ResultDigest {
		return Result{}, ErrCorrupt
	}
	engagementBytes, err := wipdwire.EncodeCanonical(engagement.Typed)
	if err != nil || !bytes.Equal(engagementBytes, resultBytes) {
		return Result{}, ErrCorrupt
	}
	return installed, nil
}

// Lookup returns only a committed result under this Adapter's authenticated
// domain. It never starts or retries an engagement.
func (a *Adapter) Lookup(ctx context.Context, commandID string) (Result, error) {
	if a == nil || a.db == nil {
		return Result{}, ErrClosed
	}
	if ctx == nil || !identityPattern.MatchString(commandID) {
		return Result{}, ErrInvalidRequest
	}
	key := Key{DomainID: a.domainID, CommandID: commandID}
	var state, inputDigest, resultDigest string
	var resultBytes []byte
	err := a.db.QueryRowContext(ctx, `SELECT state,input_digest,coalesce(result_digest,''),result FROM hook_engagements
		WHERE domain_id=? AND command_id=?`, key.DomainID, key.CommandID).
		Scan(&state, &inputDigest, &resultDigest, &resultBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if state != "completed" {
		return Result{}, ErrIndeterminate
	}
	return decodeResult(key, inputDigest, resultDigest, resultBytes)
}

// Evidence retrieves one retained result artifact by its verified digest.
func (a *Adapter) Evidence(ctx context.Context, digest string) ([]byte, error) {
	if a == nil || a.db == nil {
		return nil, ErrClosed
	}
	if ctx == nil || !digestPattern.MatchString(digest) {
		return nil, ErrInvalidEvidence
	}
	var size int
	var data []byte
	err := a.db.QueryRowContext(ctx, `SELECT byte_length,bytes FROM hook_evidence WHERE domain_id=? AND digest=?`,
		a.domainID, digest).Scan(&size, &data)
	if err != nil {
		return nil, ErrInvalidEvidence
	}
	if size != len(data) || blobDigest(data) != digest {
		return nil, ErrCorrupt
	}
	return bytes.Clone(data), nil
}

func hookInputDigest(request Request) (string, error) {
	refs := make([]map[string]any, 0, len(request.Inputs))
	for _, input := range request.Inputs {
		refs = append(refs, map[string]any{"name": input.Name, "digest": input.Digest, "size": input.Size})
	}
	claim := any(nil)
	if request.Claim != nil {
		claim = map[string]any{"id": request.Claim.ID, "epoch": request.Claim.Epoch}
	}
	value := map[string]any{
		"kind": request.Kind, "version": request.Version, "adapter_id": request.AdapterID,
		"adapter_version": request.AdapterVersion, "run_id": request.RunID, "matter_id": request.MatterID,
		"target_id": request.TargetID, "target_kind": request.TargetKind, "role": request.Role, "role_id": nullableID(request.RoleID),
		"owner_environment_id": request.EnvironmentID, "original_dispatch_id": nullableID(request.DispatchID),
		"original_claim": claim,
		"target_claims":  request.TargetClaims,
		"installed_prefix_anchor": map[string]any{
			"event_count":         request.Prefix.EventCount,
			"high_water_event_id": nullableIDPointer(request.Prefix.EventID), "prefix_digest": request.Prefix.Digest,
		},
		"revision": request.Revision, "reason": request.Reason,
		"policy": map[string]any{"contention": request.Contention, "blocked": request.Blocked, "failed": request.Failed},
		"inputs": refs,
	}
	canonical, err := wipdwire.EncodeCanonical(value)
	if err != nil {
		return "", err
	}
	return domainDigest("wipd/hook-input/v1", canonical), nil
}

// InputDigest exposes the deterministic digest used by the persisted adapter
// intent so the authority can independently bind the returned result to the
// submitted command's immutable context and blob references.
func InputDigest(request Request) (string, error) { return hookInputDigest(request) }

func hookResultDigest(key Key, inputDigest string, typed TypedResult) (string, error) {
	canonical, err := wipdwire.EncodeCanonical(map[string]any{
		"domain_id": key.DomainID, "command_id": key.CommandID,
		"input_digest": inputDigest, "typed_result": typed,
	})
	if err != nil {
		return "", err
	}
	return domainDigest("wipd/hook-result/v1", canonical), nil
}

// ResultDigest exposes the canonical adapter-result binding for the
// authenticated split-phase receiver.
func ResultDigest(key Key, inputDigest string, typed TypedResult) (string, error) {
	return hookResultDigest(key, inputDigest, typed)
}

// OperationOutput converts the adapter's single active typed-result variant
// into the closed operation result carried by the authenticated authority hop.
func OperationOutput(result Result) (operation.SchedulerHookOutput, error) {
	if !validTypedResult(result.Typed) || result.Key.CommandID == "" || result.Key.DomainID == "" ||
		!digestPattern.MatchString(result.InputDigest) || !digestPattern.MatchString(result.ResultDigest) {
		return operation.SchedulerHookOutput{}, ErrInvalidRequest
	}
	output := operation.SchedulerHookOutput{
		Kind: string(result.Typed.Kind), Version: uint64(result.Typed.Version),
		InputDigest: result.InputDigest, ResultDigest: result.ResultDigest,
	}
	switch result.Typed.Kind {
	case KindWork:
		output.Outcome = string(result.Typed.Work.Outcome)
		output.Revision = result.Typed.Work.Revision
		output.EvidenceRefs = slices.Clone(result.Typed.Work.EvidenceRefs)
		output.ProblemCode = result.Typed.Work.ProblemCode
	case KindPlan:
		output.Outcome = string(result.Typed.Plan.Outcome)
		output.ProblemCode = result.Typed.Plan.ProblemCode
		output.Steps = make([]operation.SchedulerHookStep, len(result.Typed.Plan.Steps))
		for index, step := range result.Typed.Plan.Steps {
			output.Steps[index] = operation.SchedulerHookStep{Title: step.Title}
		}
	case KindAsk:
		output.Handling = string(result.Typed.Ask.Handling)
	default:
		return operation.SchedulerHookOutput{}, ErrUnsupported
	}
	return output, nil
}

// TypedFromOperationOutput reconstructs the adapter result variant from the
// exact closed authority DTO and rejects mixed or kind-inappropriate fields.
func TypedFromOperationOutput(output operation.SchedulerHookOutput) (TypedResult, error) {
	typed := TypedResult{Kind: Kind(output.Kind), Version: uint16(output.Version)}
	switch typed.Kind {
	case KindWork:
		if output.Steps != nil || output.Handling != "" {
			return TypedResult{}, ErrInvalidRequest
		}
		typed.Work = &WorkResult{
			Outcome: WorkOutcome(output.Outcome), Revision: output.Revision,
			EvidenceRefs: slices.Clone(output.EvidenceRefs), ProblemCode: output.ProblemCode,
		}
	case KindPlan:
		if output.Revision != "" || output.EvidenceRefs != nil || output.Handling != "" {
			return TypedResult{}, ErrInvalidRequest
		}
		steps := make([]StepSpec, len(output.Steps))
		for index, step := range output.Steps {
			steps[index] = StepSpec{Title: step.Title}
		}
		typed.Plan = &PlanResult{Outcome: PlanOutcome(output.Outcome), Steps: steps, ProblemCode: output.ProblemCode}
	case KindAsk:
		if output.Outcome != "" || output.Revision != "" || output.EvidenceRefs != nil || output.ProblemCode != "" || output.Steps != nil {
			return TypedResult{}, ErrInvalidRequest
		}
		typed.Ask = &AskResult{Handling: Handling(output.Handling)}
	default:
		return TypedResult{}, ErrUnsupported
	}
	if !validTypedResult(typed) {
		return TypedResult{}, ErrInvalidRequest
	}
	return typed, nil
}

func domainDigest(label string, canonical []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(label))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func decodeResult(key Key, inputDigest, resultDigest string, raw []byte) (Result, error) {
	var typed TypedResult
	if err := wipdwire.DecodeCanonical(raw, &typed, "kind", "version", "work", "plan", "ask"); err != nil || !validTypedResult(typed) {
		return Result{}, ErrCorrupt
	}
	computed, err := hookResultDigest(key, inputDigest, typed)
	if err != nil || computed != resultDigest {
		return Result{}, ErrCorrupt
	}
	return Result{Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest}, nil
}

func validTypedResult(result TypedResult) bool {
	if result.Version != 1 {
		return false
	}
	switch result.Kind {
	case KindPlan:
		return result.Plan != nil && result.Work == nil && result.Ask == nil &&
			(result.Plan.Outcome == PlanPlanned || result.Plan.Outcome == PlanFailed) &&
			(result.Plan.Outcome != PlanPlanned || result.Plan.ProblemCode == "") &&
			(result.Plan.Outcome != PlanFailed || result.Plan.ProblemCode != "")
	case KindWork:
		return result.Work != nil && result.Plan == nil && result.Ask == nil &&
			(result.Work.Outcome == WorkCompleted || result.Work.Outcome == WorkFailed ||
				result.Work.Outcome == WorkVerified || result.Work.Outcome == WorkNotVerified) &&
			(result.Work.Outcome == WorkCompleted || result.Work.Outcome == WorkVerified || result.Work.ProblemCode != "") &&
			(result.Work.Outcome != WorkCompleted && result.Work.Outcome != WorkVerified || result.Work.ProblemCode == "")
	case KindAsk:
		return result.Ask != nil && result.Work == nil && result.Plan == nil &&
			(result.Ask.Handling == HandleSkip || result.Ask.Handling == HandleHalt)
	default:
		return false
	}
}

func validateRequest(request Request) error {
	if request.Kind == "" || request.Version != 1 || request.AdapterID != AdapterID || request.AdapterVersion != AdapterVersion {
		return ErrUnsupported
	}
	switch request.Kind {
	case KindPlan:
		if request.Role != "researcher" || request.RoleID == "" || request.DispatchID == "" || request.Claim == nil ||
			request.TargetID != request.MatterID || request.TargetKind != "matter" || request.Revision != "builtin.markdown-checklist-plan@v1" ||
			!validClaim(request.Claim) || !identityPattern.MatchString(request.RoleID) || !identityPattern.MatchString(request.DispatchID) ||
			!validTargetClaims(request, 1) ||
			!validInputs(request.Inputs, "workplan") {
			return ErrUnsupported
		}
	case KindWork:
		if request.RoleID == "" || request.DispatchID == "" || request.Claim == nil || !validClaim(request.Claim) ||
			!identityPattern.MatchString(request.RoleID) || !identityPattern.MatchString(request.DispatchID) || !validTargetClaims(request, 1) ||
			(request.TargetKind != "matter" && request.TargetKind != "stage" && request.TargetKind != "step") ||
			(request.TargetKind == "matter") != (request.TargetID == request.MatterID) {
			return ErrUnsupported
		}
		switch {
		case request.Role == "builder" && request.Revision == "builtin.markdown-outline@v1":
			if !validInputs(request.Inputs, "source") {
				return ErrInvalidEvidence
			}
		case request.Role == "verifier" && request.Revision == "builtin.markdown-outline-verifier@v1":
			if !validOutlineInputs(request.Inputs) {
				return ErrInvalidEvidence
			}
		case request.Role == "verifier" && request.Revision == operation.SchedulerStep10VerifierRevision:
			if !validStep10VerifierInputs(request.Inputs) {
				return ErrInvalidEvidence
			}
		default:
			return ErrUnsupported
		}
	case KindAsk:
		if request.Role != "" || request.RoleID != "" || request.DispatchID != "" || request.Claim != nil ||
			len(request.TargetClaims) != 0 || request.TargetID != request.MatterID || request.TargetKind != "matter" ||
			request.Revision != "builtin.run-policy@v1" || len(request.Inputs) != 0 {
			return ErrUnsupported
		}
		if request.Reason != "contended" && request.Reason != "blocked" && request.Reason != "failed" {
			return ErrInvalidRequest
		}
		for _, handling := range []Handling{request.Contention, request.Blocked, request.Failed} {
			if handling != HandleSkip && handling != HandleHalt {
				return ErrInvalidRequest
			}
		}
	default:
		return ErrUnsupported
	}
	return nil
}

func validTargetClaims(request Request, count int) bool {
	if len(request.TargetClaims) != count {
		return false
	}
	claim := request.TargetClaims[0]
	return claim.MatterID == request.MatterID && claim.ClaimID == request.Claim.ID && claim.Epoch == request.Claim.Epoch
}

func validInputs(inputs []PinnedBlob, name string) bool {
	if len(inputs) != 1 || inputs[0].Name != name || !digestPattern.MatchString(inputs[0].Digest) || inputs[0].Size < 0 ||
		inputs[0].Size > maxInputBytes || int64(len(inputs[0].Bytes)) != inputs[0].Size || blobDigest(inputs[0].Bytes) != inputs[0].Digest {
		return false
	}
	return utf8.Valid(inputs[0].Bytes) && !bytes.ContainsRune(inputs[0].Bytes, '\x00')
}

func validOutlineInputs(inputs []PinnedBlob) bool {
	return len(inputs) == 2 && validInputs(inputs[:1], "outline") && validInputs(inputs[1:], "source")
}

func compute(request Request) (TypedResult, map[string][]byte, error) {
	switch request.Kind {
	case KindPlan:
		steps, err := parseChecklist(request.Inputs[0].Bytes)
		if err != nil {
			return TypedResult{}, nil, err
		}
		return TypedResult{Kind: KindPlan, Version: 1, Plan: &PlanResult{Outcome: PlanPlanned, Steps: steps}}, nil, nil
	case KindWork:
		if request.Role == "builder" {
			return markdownOutline(request.Revision, request.Inputs[0])
		}
		return verifyMarkdownOutline(request.Revision, request.Inputs[1], request.Inputs[0])
	case KindAsk:
		var handling Handling
		switch request.Reason {
		case "contended":
			handling = request.Contention
		case "blocked":
			handling = request.Blocked
		case "failed":
			handling = request.Failed
		}
		return TypedResult{Kind: KindAsk, Version: 1, Ask: &AskResult{Handling: handling}}, nil, nil
	default:
		return TypedResult{}, nil, ErrUnsupported
	}
}

func parseChecklist(content []byte) ([]StepSpec, error) {
	steps := make([]StepSpec, 0)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 4096), int(maxInputBytes))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "- [ ] ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(line, "- [ ] "))
		if title == "" || len(title) > 240 || len(steps) == 128 {
			return nil, ErrInvalidEvidence
		}
		steps = append(steps, StepSpec{Title: title})
	}
	if err := scanner.Err(); err != nil {
		return nil, ErrInvalidEvidence
	}
	return steps, nil
}

type outlineDocument struct {
	Schema       string           `json:"schema"`
	SourceDigest string           `json:"source_digest"`
	Headings     []outlineHeading `json:"headings"`
}

type outlineHeading struct {
	Level int    `json:"level"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
}

func markdownOutline(revision string, source PinnedBlob) (TypedResult, map[string][]byte, error) {
	headings := make([]outlineHeading, 0)
	scanner := bufio.NewScanner(bytes.NewReader(source.Bytes))
	scanner.Buffer(make([]byte, 4096), int(maxInputBytes))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		level := 0
		for level < len(line) && level < 6 && line[level] == '#' {
			level++
		}
		if level == 0 || level == len(line) || line[level] != ' ' {
			continue
		}
		text := strings.TrimSpace(line[level+1:])
		if text != "" {
			headings = append(headings, outlineHeading{Level: level, Line: lineNumber, Text: text})
		}
	}
	if err := scanner.Err(); err != nil {
		return TypedResult{}, nil, ErrInvalidEvidence
	}
	if len(headings) == 0 {
		return TypedResult{Kind: KindWork, Version: 1, Work: &WorkResult{
			Outcome: WorkFailed, Revision: revision, EvidenceRefs: []string{}, ProblemCode: "adapter.markdown-no-headings",
		}}, nil, nil
	}
	encoded, err := json.Marshal(outlineDocument{Schema: "wipd.markdown-outline/1", SourceDigest: source.Digest, Headings: headings})
	if err != nil || len(encoded) > maxResultBytes {
		return TypedResult{}, nil, ErrInvalidEvidence
	}
	digest := blobDigest(encoded)
	return TypedResult{Kind: KindWork, Version: 1, Work: &WorkResult{
		Outcome: WorkCompleted, Revision: revision, EvidenceRefs: []string{digest},
	}}, map[string][]byte{digest: encoded}, nil
}

func verifyMarkdownOutline(revision string, source, outline PinnedBlob) (TypedResult, map[string][]byte, error) {
	invalid := func() (TypedResult, map[string][]byte, error) {
		return TypedResult{Kind: KindWork, Version: 1, Work: &WorkResult{
			Outcome: WorkNotVerified, Revision: revision, EvidenceRefs: []string{outline.Digest},
			ProblemCode: "verification.markdown-outline-invalid",
		}}, nil, nil
	}
	var document outlineDocument
	decoder := json.NewDecoder(bytes.NewReader(outline.Bytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Schema != "wipd.markdown-outline/1" ||
		document.SourceDigest != source.Digest || len(document.Headings) == 0 {
		return invalid()
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalid()
	}
	previous := 0
	for _, heading := range document.Headings {
		if heading.Level < 1 || heading.Level > 6 || heading.Line <= previous || strings.TrimSpace(heading.Text) == "" {
			return invalid()
		}
		previous = heading.Line
	}
	expected, _, err := markdownOutline("builtin.markdown-outline@v1", source)
	if err != nil || expected.Work == nil || expected.Work.Outcome != WorkCompleted ||
		len(expected.Work.EvidenceRefs) != 1 || expected.Work.EvidenceRefs[0] != outline.Digest {
		return invalid()
	}
	return TypedResult{Kind: KindWork, Version: 1, Work: &WorkResult{
		Outcome: WorkNotVerified, Revision: revision, EvidenceRefs: []string{outline.Digest},
		ProblemCode: "verification.complete-pass-unavailable",
	}}, nil, nil
}

func blobDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validClaim(claim *Claim) bool {
	return claim != nil && identityPattern.MatchString(claim.ID) && claim.Epoch > 0
}

func optionalIdentity(value string) *string {
	if value == "" {
		return nil
	}
	valueCopy := value
	return &valueCopy
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableIDPointer(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func cloneClaim(claim *Claim) *Claim {
	if claim == nil {
		return nil
	}
	claimCopy := *claim
	return &claimCopy
}

func clonePrefix(prefix PrefixAnchor) PrefixAnchor {
	if prefix.EventID != nil {
		id := *prefix.EventID
		prefix.EventID = &id
	}
	return prefix
}
