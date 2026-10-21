package wipdseed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/reporoute"
	"golang.org/x/sys/unix"
)

const environmentAttachLedgerFile = "environment-attach-ledger.json"

// EnvironmentAttachPrepared and the following values are the closed durable
// dispositions for one owner-local attach attempt.
const (
	EnvironmentAttachPrepared            = "prepared"
	EnvironmentAttachUnavailable         = "unavailable"
	EnvironmentAttachSubmitted           = "submitted"
	EnvironmentAttachOutcomeUnknown      = "outcome-unknown"
	EnvironmentAttachTerminal            = "terminal"
	EnvironmentAttachRefused             = "refused"
	EnvironmentAttachLocalInstallPending = "local-install-pending"
	EnvironmentAttachInstalled           = "installed"
)

// EnvironmentAttachAttempt is the exact, pinned input and cumulative local state for an attach.
type EnvironmentAttachAttempt struct {
	EnvironmentID     string                    `json:"environment_id"`
	GrantDigest       string                    `json:"grant_digest"`
	GrantBytes        []byte                    `json:"grant_bytes"`
	D115ReceiptBytes  []byte                    `json:"d115_receipt_bytes"`
	D115ReceiptDigest string                    `json:"d115_receipt_digest"`
	D115AttemptID     string                    `json:"d115_attempt_id"`
	CommandID         string                    `json:"command_id"`
	RequestBytes      []byte                    `json:"request_bytes"`
	RequestHash       string                    `json:"request_hash"`
	AuthorityDomainID string                    `json:"authority_domain_id"`
	AuthorityEpoch    uint64                    `json:"authority_epoch"`
	EnvironmentSPKI   string                    `json:"environment_spki"`
	Route             string                    `json:"route"`
	CheckoutProof     []byte                    `json:"checkout_proof"`
	Disposition       string                    `json:"disposition"`
	Markers           []EnvironmentAttachMarker `json:"markers"`
	ResultBytes       []byte                    `json:"result_bytes,omitempty"`
}

// EnvironmentAttachMarker is an append-only exchange/recovery observation.
type EnvironmentAttachMarker struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
}
type environmentAttachLedgerState struct {
	Schema   string                     `json:"schema"`
	Attempts []EnvironmentAttachAttempt `json:"attempts"`
}

// EnvironmentAttachLedger serializes exact grant intents and recovery markers
// across concurrent goroutines and daemon restarts.
type EnvironmentAttachLedger struct {
	dir      string
	mu       sync.Mutex
	intentMu sync.Mutex
}

// OpenEnvironmentAttachLedger creates or opens a separate owner-private ledger below profileRoot.
func OpenEnvironmentAttachLedger(profileRoot string) (*EnvironmentAttachLedger, error) {
	info, err := os.Lstat(profileRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return nil, ErrInvalidClientState
	}
	dir := filepath.Join(profileRoot, "environment-attach-attempts")
	if err = os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err = syncProfileDirectory(profileRoot); err != nil {
		return nil, err
	}
	info, err = os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return nil, ErrInvalidClientState
	}
	l := &EnvironmentAttachLedger{dir: dir}
	if err = l.withState(func(*environmentAttachLedgerState) error { return nil }); err != nil {
		return nil, err
	}
	return l, nil
}

// GetOrCreate atomically returns an identical keyed attempt or refuses a different pinned intent.
func (l *EnvironmentAttachLedger) GetOrCreate(a EnvironmentAttachAttempt) (EnvironmentAttachAttempt, error) {
	if a.EnvironmentID == "" || a.D115ReceiptBytes == nil || a.D115AttemptID == "" || a.CommandID == "" || a.CheckoutProof == nil || a.AuthorityDomainID == "" || a.AuthorityEpoch == 0 ||
		(a.RequestBytes == nil) != (a.RequestHash == "") {
		return EnvironmentAttachAttempt{}, ErrInvalidClientState
	}
	g, r := sha256.Sum256(a.GrantBytes), sha256.Sum256(a.D115ReceiptBytes)
	if a.GrantBytes == nil || a.GrantDigest != "sha256:"+hex.EncodeToString(g[:]) || a.D115ReceiptDigest != "sha256:"+hex.EncodeToString(r[:]) {
		return EnvironmentAttachAttempt{}, ErrInvalidClientState
	}
	var result EnvironmentAttachAttempt
	err := l.withState(func(s *environmentAttachLedgerState) error {
		for _, old := range s.Attempts {
			if old.EnvironmentID == a.EnvironmentID && old.GrantDigest == a.GrantDigest {
				if !sameAttachIntent(old, a) {
					return ErrStateExists
				}
				result = cloneAttachAttempt(old)
				return nil
			}
		}
		for _, old := range s.Attempts {
			if old.EnvironmentID == a.EnvironmentID && old.CommandID == a.CommandID {
				return ErrInvalidClientState
			}
		}
		if a.Disposition == "" {
			a.Disposition = EnvironmentAttachPrepared
		}
		a.Markers = []EnvironmentAttachMarker{}
		result = cloneAttachAttempt(a)
		s.Attempts = append(s.Attempts, result)
		return nil
	})
	return result, err
}

// StorePreparedCommand completes a reservation after the Environment journal
// has durably prepared the exact command. It makes crashes on either side of
// that journal commit recoverable using the reserved command ID.
func (l *EnvironmentAttachLedger) StorePreparedCommand(env, digest string, request []byte, requestHash string) (EnvironmentAttachAttempt, error) {
	command, err := operation.DecodeCanonicalCommand(request)
	if err != nil || command.ID == "" || operation.VerifyRequestHash(command, requestHash) != nil {
		return EnvironmentAttachAttempt{}, ErrInvalidClientState
	}
	var out EnvironmentAttachAttempt
	err = l.withState(func(s *environmentAttachLedgerState) error {
		for i := range s.Attempts {
			a := &s.Attempts[i]
			if a.EnvironmentID != env || a.GrantDigest != digest {
				continue
			}
			if command.ID != a.CommandID || command.EnvironmentID != a.EnvironmentID ||
				command.AuthorityDomainID != a.AuthorityDomainID || command.ExpectedAuthorityEpoch != a.AuthorityEpoch ||
				command.Request.Context.Repo == "" {
				return ErrInvalidClientState
			}
			if a.RequestBytes != nil {
				if !bytes.Equal(a.RequestBytes, request) || a.RequestHash != requestHash {
					return ErrStateExists
				}
			} else {
				a.RequestBytes = bytes.Clone(request)
				a.RequestHash = requestHash
			}
			out = cloneAttachAttempt(*a)
			return nil
		}
		return os.ErrNotExist
	})
	return out, err
}

// LockIntent serializes one Environment/grant lifecycle across concurrent
// authenticated IPC calls using the server-owned ledger handle.
func (l *EnvironmentAttachLedger) LockIntent() func() {
	l.intentMu.Lock()
	return l.intentMu.Unlock
}

// AppendMarker appends one marker and updates cumulative disposition; markers cannot be rewritten.
func (l *EnvironmentAttachLedger) AppendMarker(env, digest string, marker EnvironmentAttachMarker, disposition string) (EnvironmentAttachAttempt, error) {
	var out EnvironmentAttachAttempt
	err := l.withState(func(s *environmentAttachLedgerState) error {
		for i := range s.Attempts {
			a := &s.Attempts[i]
			if a.EnvironmentID == env && a.GrantDigest == digest {
				if marker.Kind == "" || !validEnvironmentAttachDisposition(disposition) ||
					marker.Sequence != uint64(len(a.Markers)+1) || !validEnvironmentAttachTransition(a.Disposition, disposition) {
					return ErrInvalidClientState
				}
				a.Markers = append(a.Markers, marker)
				a.Disposition = disposition
				out = cloneAttachAttempt(*a)
				return nil
			}
		}
		return os.ErrNotExist
	})
	return out, err
}

// PersistResult pins exact terminal result bytes; repeating the same bytes is idempotent.
func (l *EnvironmentAttachLedger) PersistResult(env, digest string, result []byte, disposition string) (EnvironmentAttachAttempt, error) {
	var out EnvironmentAttachAttempt
	err := l.withState(func(s *environmentAttachLedgerState) error {
		for i := range s.Attempts {
			a := &s.Attempts[i]
			if a.EnvironmentID == env && a.GrantDigest == digest {
				if len(result) == 0 || !validEnvironmentAttachDisposition(disposition) || !validEnvironmentAttachTransition(a.Disposition, disposition) {
					return ErrInvalidClientState
				}
				if len(a.ResultBytes) > 0 && !bytes.Equal(a.ResultBytes, result) {
					return ErrStateExists
				}
				a.ResultBytes = bytes.Clone(result)
				a.Disposition = disposition
				out = cloneAttachAttempt(*a)
				return nil
			}
		}
		return os.ErrNotExist
	})
	return out, err
}

func validEnvironmentAttachDisposition(value string) bool {
	switch value {
	case EnvironmentAttachPrepared, EnvironmentAttachUnavailable, EnvironmentAttachSubmitted,
		EnvironmentAttachOutcomeUnknown, EnvironmentAttachTerminal, EnvironmentAttachRefused, EnvironmentAttachLocalInstallPending,
		EnvironmentAttachInstalled:
		return true
	default:
		return false
	}
}

// Unknown is cumulative: a later connect failure cannot turn it into
// unavailable. Only authenticated M2 evidence may advance it to submitted or
// terminal; terminal installation is a separate local recovery phase.
func validEnvironmentAttachTransition(from, to string) bool {
	if from == "" {
		from = EnvironmentAttachPrepared
	}
	if from == to {
		return true
	}
	switch from {
	case EnvironmentAttachPrepared:
		return to == EnvironmentAttachUnavailable || to == EnvironmentAttachSubmitted ||
			to == EnvironmentAttachOutcomeUnknown || to == EnvironmentAttachTerminal || to == EnvironmentAttachRefused
	case EnvironmentAttachUnavailable:
		return to == EnvironmentAttachSubmitted || to == EnvironmentAttachOutcomeUnknown || to == EnvironmentAttachTerminal || to == EnvironmentAttachRefused
	case EnvironmentAttachSubmitted:
		return to == EnvironmentAttachOutcomeUnknown || to == EnvironmentAttachTerminal || to == EnvironmentAttachRefused
	case EnvironmentAttachOutcomeUnknown:
		return to == EnvironmentAttachSubmitted || to == EnvironmentAttachTerminal || to == EnvironmentAttachRefused
	case EnvironmentAttachTerminal:
		return to == EnvironmentAttachLocalInstallPending || to == EnvironmentAttachInstalled
	case EnvironmentAttachLocalInstallPending:
		return to == EnvironmentAttachInstalled
	default:
		return false
	}
}

// Load returns a copy of one exact Environment/grant attempt.
func (l *EnvironmentAttachLedger) Load(env, digest string) (EnvironmentAttachAttempt, error) {
	var out EnvironmentAttachAttempt
	err := l.withState(func(s *environmentAttachLedgerState) error {
		for _, a := range s.Attempts {
			if a.EnvironmentID == env && a.GrantDigest == digest {
				out = cloneAttachAttempt(a)
				return nil
			}
		}
		return os.ErrNotExist
	})
	return out, err
}

// LoadByCommandID finds one immutable attempt for authenticated status lookup.
func (l *EnvironmentAttachLedger) LoadByCommandID(commandID string) (EnvironmentAttachAttempt, error) {
	var out EnvironmentAttachAttempt
	err := l.withState(func(s *environmentAttachLedgerState) error {
		for _, a := range s.Attempts {
			if a.CommandID == commandID {
				out = cloneAttachAttempt(a)
				return nil
			}
		}
		return os.ErrNotExist
	})
	return out, err
}

// LoadByGrantDigest finds one immutable intent independently of its currently
// active disposition, allowing exact recovery classification before new-use
// grant validity and route checks.
func (l *EnvironmentAttachLedger) LoadByGrantDigest(digest string) (EnvironmentAttachAttempt, error) {
	var out EnvironmentAttachAttempt
	err := l.withState(func(s *environmentAttachLedgerState) error {
		for _, a := range s.Attempts {
			if a.GrantDigest == digest {
				if out.CommandID != "" {
					return ErrInvalidClientState
				}
				out = cloneAttachAttempt(a)
			}
		}
		if out.CommandID == "" {
			return os.ErrNotExist
		}
		return nil
	})
	return out, err
}

func sameAttachIntent(a, b EnvironmentAttachAttempt) bool {
	a.Disposition = ""
	b.Disposition = ""
	a.Markers = nil
	b.Markers = nil
	a.ResultBytes = nil
	b.ResultBytes = nil
	return bytes.Equal(mustJSON(a), mustJSON(b))
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func cloneAttachAttempt(a EnvironmentAttachAttempt) EnvironmentAttachAttempt {
	a.GrantBytes = bytes.Clone(a.GrantBytes)
	a.D115ReceiptBytes = bytes.Clone(a.D115ReceiptBytes)
	a.RequestBytes = bytes.Clone(a.RequestBytes)
	a.CheckoutProof = bytes.Clone(a.CheckoutProof)
	a.ResultBytes = bytes.Clone(a.ResultBytes)
	a.Markers = append([]EnvironmentAttachMarker(nil), a.Markers...)
	return a
}

// RememberedRouteBinding returns the single installed attach identity for a
// normalized key, if one exists. Conflicting retained identities fail closed.
func (l *EnvironmentAttachLedger) RememberedRouteBinding(key string) (*reporoute.Binding, error) {
	var binding *reporoute.Binding
	err := l.readState(func(state *environmentAttachLedgerState) error {
		for _, attempt := range state.Attempts {
			if attempt.Route != key || attempt.Disposition != EnvironmentAttachInstalled {
				continue
			}
			current := reporoute.Binding{RepoID: "", DomainID: attempt.AuthorityDomainID, Epoch: attempt.AuthorityEpoch}
			command, err := operation.DecodeCanonicalCommand(attempt.RequestBytes)
			if err != nil || command.Request.Context.Repo == "" {
				return ErrInvalidClientState
			}
			current.RepoID = command.Request.Context.Repo
			if binding != nil && *binding != current {
				return ErrStateExists
			}
			bindingCopy := current
			binding = &bindingCopy
		}
		return nil
	})
	return binding, err
}

func (l *EnvironmentAttachLedger) withState(fn func(*environmentAttachLedgerState) error) (resultErr error) {
	return l.withStateMode(true, fn)
}

func (l *EnvironmentAttachLedger) readState(fn func(*environmentAttachLedgerState) error) error {
	return l.withStateMode(false, fn)
}

func (l *EnvironmentAttachLedger) withStateMode(persist bool, fn func(*environmentAttachLedgerState) error) (resultErr error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lockPath := filepath.Join(l.dir, "ledger.lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	flock := unix.LOCK_SH
	if persist {
		flock = unix.LOCK_EX
	}
	if err = unix.Flock(fd, flock); err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, unix.Flock(fd, unix.LOCK_UN)) }()
	li, err := lock.Stat()
	if err != nil || !li.Mode().IsRegular() || li.Mode().Perm() != 0o600 || !ownedByEffectiveUID(li) {
		return ErrInvalidClientState
	}
	if persist {
		err = removeStaleEnvironmentAttachTemp(l.dir)
	}
	if err != nil {
		return err
	}
	path := filepath.Join(l.dir, environmentAttachLedgerFile)
	s := environmentAttachLedgerState{Schema: "wipd.environment-attach-attempt-ledger/1", Attempts: []EnvironmentAttachAttempt{}}
	data, re := readPrivateRegular(path, 16<<20, 0o600)
	if re != nil && !errors.Is(re, os.ErrNotExist) {
		return ErrInvalidClientState
	}
	if re == nil {
		defer clear(data)
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Schema != "wipd.environment-attach-attempt-ledger/1" {
			return ErrInvalidClientState
		}
	}
	if err = fn(&s); err != nil {
		return err
	}
	if !persist {
		return nil
	}
	sort.Slice(s.Attempts, func(i, j int) bool {
		if s.Attempts[i].EnvironmentID != s.Attempts[j].EnvironmentID {
			return s.Attempts[i].EnvironmentID < s.Attempts[j].EnvironmentID
		}
		return s.Attempts[i].GrantDigest < s.Attempts[j].GrantDigest
	})
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > 16<<20 {
		return ErrInvalidClientState
	}
	tmp := filepath.Join(l.dir, ".environment-attach.tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(encoded); err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncProfileDirectory(l.dir)
}

func removeStaleEnvironmentAttachTemp(dir string) error {
	p := filepath.Join(dir, ".environment-attach.tmp")
	b, e := readPrivateRegular(p, 16<<20, 0o600)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return ErrInvalidClientState
	}
	clear(b)
	if e = os.Remove(p); e != nil {
		return e
	}
	return syncProfileDirectory(dir)
}
