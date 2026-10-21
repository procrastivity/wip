package wipdseed

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	d115LedgerName = "attempt-ledger.json"
	d115LockName   = "attempt-ledger.lock"
)

// D115Disposition is the durable cumulative state of one exact local attempt.
type D115Disposition string

// D115 dispositions distinguish no-send, uncertain, durable-receipt, and terminal states.
const (
	D115Preparing         D115Disposition = "preparing"
	D115PendingSignature  D115Disposition = "pending-signature"
	D115Prepared          D115Disposition = "prepared"
	D115DefinitelyUnsent  D115Disposition = "definitely-unsent"
	D115PossiblySubmitted D115Disposition = "possibly-submitted"
	D115OutcomeUnknown    D115Disposition = "outcome-unknown"
	D115ReceiptDurable    D115Disposition = "receipt-durable"
	D115Installed         D115Disposition = "installed"
	D115Cancelled         D115Disposition = "cancelled"
	D115Refused           D115Disposition = "refused"
)

// D115Exchange records one independently classified network attempt.
type D115Exchange struct {
	Number      uint64 `json:"number"`
	Disposition string `json:"disposition"`
	ResultCode  string `json:"result_code"`
	OccurredAt  string `json:"occurred_at"`
}

// D115Attempt retains the exact request/recovery identity and cumulative history.
type D115Attempt struct {
	RealmID         string          `json:"provisioning_realm_id"`
	GrantDigest     string          `json:"grant_digest"`
	CheckoutBinding string          `json:"checkout_binding"`
	CommandID       string          `json:"command_id"`
	RequestHash     string          `json:"request_hash"`
	OwnerKeyID      string          `json:"owner_root_key_id"`
	GrantID         string          `json:"grant_id"`
	RequestBytes    []byte          `json:"request_bytes,omitempty"`
	SigningRequest  []byte          `json:"signing_request,omitempty"`
	ProofArtifact   []byte          `json:"proof_artifact,omitempty"`
	ReceiptBytes    []byte          `json:"receipt_bytes,omitempty"`
	Disposition     D115Disposition `json:"command_disposition"`
	Exchanges       []D115Exchange  `json:"exchanges"`
}

type d115LedgerFile struct {
	Schema   string        `json:"schema"`
	Attempts []D115Attempt `json:"attempts"`
}

type d115Ledger struct {
	directory string
	mu        sync.Mutex
}

func openD115Ledger(profileRoot string) (*d115Ledger, error) {
	return openD115LedgerWithDirectorySync(profileRoot, syncProfileDirectory)
}

func openD115LedgerWithDirectorySync(profileRoot string, syncDirectory func(string) error) (*d115Ledger, error) {
	info, err := os.Lstat(profileRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return nil, ErrInvalidClientState
	}
	directory := filepath.Join(profileRoot, "d115-attempts")
	if err = os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	// The ledger directory is itself a durable namespace entry. Sync its
	// owner-private parent before relying on files inside it.
	if err = syncDirectory(profileRoot); err != nil {
		return nil, err
	}
	info, err = os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return nil, ErrInvalidClientState
	}
	ledger := &d115Ledger{directory: directory}
	if err = ledger.withFile(func(file *d115LedgerFile) error {
		for index := range file.Attempts {
			if file.Attempts[index].Disposition == D115PossiblySubmitted {
				file.Attempts[index].Disposition = D115OutcomeUnknown
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (ledger *d115Ledger) getOrCreate(attempt D115Attempt) (D115Attempt, error) {
	var result D115Attempt
	err := ledger.withFile(func(file *d115LedgerFile) error {
		for _, existing := range file.Attempts {
			if existing.RealmID == attempt.RealmID && existing.GrantDigest == attempt.GrantDigest {
				if existing.CheckoutBinding != attempt.CheckoutBinding {
					return ErrStateExists
				}
				result = cloneD115Attempt(existing)
				return nil
			}
		}
		for _, existing := range file.Attempts {
			if existing.RealmID == attempt.RealmID && existing.CommandID == attempt.CommandID {
				return ErrInvalidClientState
			}
		}
		result = cloneD115Attempt(attempt)
		file.Attempts = append(file.Attempts, result)
		return nil
	})
	return result, err
}

func (ledger *d115Ledger) update(realmID, grantDigest string, update func(*D115Attempt) error) (D115Attempt, error) {
	var result D115Attempt
	err := ledger.withFile(func(file *d115LedgerFile) error {
		for index := range file.Attempts {
			attempt := &file.Attempts[index]
			if attempt.RealmID == realmID && attempt.GrantDigest == grantDigest {
				if err := update(attempt); err != nil {
					return err
				}
				result = cloneD115Attempt(*attempt)
				return nil
			}
		}
		return os.ErrNotExist
	})
	return result, err
}

func (ledger *d115Ledger) loadAll() ([]D115Attempt, error) {
	var result []D115Attempt
	err := ledger.withFile(func(file *d115LedgerFile) error {
		result = make([]D115Attempt, len(file.Attempts))
		for index := range file.Attempts {
			result[index] = cloneD115Attempt(file.Attempts[index])
		}
		return nil
	})
	return result, err
}

func (ledger *d115Ledger) findByCommandID(commandID string) (D115Attempt, error) {
	var result D115Attempt
	err := ledger.withFile(func(file *d115LedgerFile) error {
		for _, attempt := range file.Attempts {
			if attempt.CommandID == commandID {
				result = cloneD115Attempt(attempt)
				return nil
			}
		}
		return os.ErrNotExist
	})
	return result, err
}

func (ledger *d115Ledger) withFile(operation func(*d115LedgerFile) error) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	lockPath := filepath.Join(ledger.directory, d115LockName)
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 || !ownedByEffectiveUID(lockInfo) {
		return ErrInvalidClientState
	}
	if err = removeStaleD115LedgerTemp(ledger.directory); err != nil {
		return err
	}
	path := filepath.Join(ledger.directory, d115LedgerName)
	var state d115LedgerFile
	data, readErr := readPrivateRegular(path, 8<<20, 0o600)
	if errors.Is(readErr, os.ErrNotExist) {
		state = d115LedgerFile{Schema: "wipd.create-domain-attempt-ledger/1", Attempts: []D115Attempt{}}
	} else if readErr != nil {
		return ErrInvalidClientState
	} else {
		defer clear(data)
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decodeErr := decoder.Decode(&state); decodeErr != nil || decoder.Decode(new(any)) != io.EOF || state.Schema != "wipd.create-domain-attempt-ledger/1" {
			return ErrInvalidClientState
		}
	}
	if err = operation(&state); err != nil {
		return err
	}
	sort.Slice(state.Attempts, func(i, j int) bool {
		if state.Attempts[i].RealmID != state.Attempts[j].RealmID {
			return state.Attempts[i].RealmID < state.Attempts[j].RealmID
		}
		return state.Attempts[i].GrantDigest < state.Attempts[j].GrantDigest
	})
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > 8<<20 {
		return ErrInvalidClientState
	}
	temp, err := os.OpenFile(filepath.Join(ledger.directory, ".attempt-ledger.tmp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err = temp.Write(encoded); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tempPath, path); err != nil {
		return err
	}
	dir, err := os.Open(ledger.directory)
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}

func removeStaleD115LedgerTemp(directory string) error {
	path := filepath.Join(directory, ".attempt-ledger.tmp")
	data, err := readPrivateRegular(path, 8<<20, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrInvalidClientState
	}
	clear(data)
	if err = os.Remove(path); err != nil {
		return err
	}
	return syncProfileDirectory(directory)
}

func cloneD115Attempt(attempt D115Attempt) D115Attempt {
	attempt.RequestBytes = bytes.Clone(attempt.RequestBytes)
	attempt.SigningRequest = bytes.Clone(attempt.SigningRequest)
	attempt.ProofArtifact = bytes.Clone(attempt.ProofArtifact)
	attempt.ReceiptBytes = bytes.Clone(attempt.ReceiptBytes)
	attempt.Exchanges = append([]D115Exchange(nil), attempt.Exchanges...)
	return attempt
}
