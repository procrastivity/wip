package wipdseed

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/wipdauthority"
)

var (
	// ErrD115LocalRefused reports a local authorization refusal before accepted success.
	ErrD115LocalRefused = errors.New("wipdseed: D115 create-local refused")
	// ErrD115OutcomeUnknown means the exact command may have committed remotely.
	ErrD115OutcomeUnknown = errors.New("wipdseed: D115 create-local outcome is unknown")
	// ErrD115DefinitelyUnsent means the exchange is proven not to have been sent.
	ErrD115DefinitelyUnsent = errors.New("wipdseed: D115 create-local was definitely unsent")
	// ErrD115ReceiptDurable means a validated receipt is durable but local
	// installation did not complete; status/recovery can finish installation.
	ErrD115ReceiptDurable = errors.New("wipdseed: D115 receipt is durable")
)

var d115AttemptLocks sync.Map

func lockD115Attempt(profileRoot, commandID string) func() {
	key := profileRoot + "\x00" + commandID
	lockValue, _ := d115AttemptLocks.LoadOrStore(key, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// D115OwnerRootSigner is the existing offline owner-root signing boundary.
// Implementations must verify the signed grant's realm/scope/owner binding
// before signing the exact request-bound proof preimage. The private key is
// never part of daemon configuration or client state.
type D115OwnerRootSigner interface {
	OwnerRootKeyID() string
	SignD115OwnerProof(context.Context, []byte, d115.Claims, []byte) ([]byte, error)
}

// RecoverD115Attempts retries exact uncertain requests and installs matching
// stored receipts for one authenticated checkout binding.
func RecoverD115Attempts(ctx context.Context, profileRoot, checkoutBinding string) ([]d115.Receipt, error) {
	if !validD115CheckoutBinding(checkoutBinding) {
		return nil, ErrD115LocalRefused
	}
	return recoverD115Attempts(ctx, profileRoot, &checkoutBinding)
}

// RecoverD115StartupAttempts retries only uncertain attempts recorded in the
// owner-private profile ledger. It never prepares or signs and is called once
// during daemon startup before local IPC is served.
func RecoverD115StartupAttempts(ctx context.Context, profileRoot string) ([]d115.Receipt, error) {
	return recoverD115Attempts(ctx, profileRoot, nil)
}

func recoverD115Attempts(ctx context.Context, profileRoot string, checkoutBinding *string) ([]d115.Receipt, error) {
	if ctx == nil {
		return nil, ErrD115LocalRefused
	}
	config, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		return nil, err
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return nil, err
	}
	attempts, err := ledger.loadAll()
	if err != nil {
		return nil, err
	}
	var receipts []d115.Receipt
	var recoveryErrors []error
	for _, attempt := range attempts {
		if attempt.RealmID != config.ProvisioningRealmID || (checkoutBinding != nil && attempt.CheckoutBinding != *checkoutBinding) ||
			(attempt.Disposition != D115OutcomeUnknown && attempt.Disposition != D115PossiblySubmitted && attempt.Disposition != D115ReceiptDurable) {
			continue
		}
		unlock := lockD115Attempt(profileRoot, attempt.CommandID)
		var receipt d115.Receipt
		if attempt.Disposition == D115ReceiptDurable {
			receipt, err = installD115Receipt(ledger, profileRoot, attempt)
		} else {
			receipt, err = sendD115Attempt(ctx, ledger, profileRoot, config, attempt)
		}
		unlock()
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("recover D115 command %s: %w", attempt.CommandID, err))
			continue
		}
		receipts = append(receipts, receipt)
	}
	return receipts, errors.Join(recoveryErrors...)
}

// RecoverD115Attempt retries one exact uncertain command for the checkout
// authenticated on the local IPC request. It never prepares or signs.
func RecoverD115Attempt(ctx context.Context, profileRoot, checkoutBinding, commandID string) (d115.Receipt, error) {
	if ctx == nil || !validD115CheckoutBinding(checkoutBinding) {
		return d115.Receipt{}, ErrD115LocalRefused
	}
	config, err := LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		return d115.Receipt{}, err
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		return d115.Receipt{}, err
	}
	unlock := lockD115Attempt(profileRoot, commandID)
	defer unlock()
	attempt, err := ledger.findByCommandID(commandID)
	if err != nil || attempt.RealmID != config.ProvisioningRealmID || attempt.CheckoutBinding != checkoutBinding {
		return d115.Receipt{}, ErrD115LocalRefused
	}
	switch attempt.Disposition {
	case D115ReceiptDurable:
		return installD115Receipt(ledger, profileRoot, attempt)
	case D115OutcomeUnknown, D115PossiblySubmitted:
		return sendD115Attempt(ctx, ledger, profileRoot, config, attempt)
	default:
		return d115.Receipt{}, ErrD115LocalRefused
	}
}

func sendD115Attempt(ctx context.Context, ledger *d115Ledger, profileRoot string, config D115ProvisioningConfig, attempt D115Attempt) (d115.Receipt, error) {
	if err := validateD115ProvisioningConfigAt(config, time.Now()); err != nil {
		return d115.Receipt{}, recordD115Unsent(ledger, attempt, "profile.invalid")
	}
	profile, err := wipdauthority.NewProvisioningProfile(config.Origin, config.ProvisioningRealmID, config.AuthoritySPKIPin)
	if err != nil {
		return d115.Receipt{}, recordD115Unsent(ledger, attempt, "profile.invalid")
	}
	certificate, err := x509.ParseCertificate(config.AuthorityCertificateDER)
	if err != nil {
		return d115.Receipt{}, recordD115Unsent(ledger, attempt, "profile.invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	client, err := profile.HTTPClient(roots)
	if err != nil {
		return d115.Receipt{}, recordD115Unsent(ledger, attempt, "profile.invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Origin+wipdauthority.D115CreateDomainPath, bytes.NewReader(attempt.RequestBytes))
	if err != nil {
		return d115.Receipt{}, recordD115Unsent(ledger, attempt, "request.invalid")
	}
	if err = ctx.Err(); err != nil {
		return d115.Receipt{}, recordD115Unsent(ledger, attempt, "request.cancelled-before-send")
	}
	request.Header.Set("Content-Type", "application/cbor")
	request.Header.Set("Accept", "application/cbor")
	attempt, err = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		if len(current.RequestBytes) == 0 || current.CommandID != attempt.CommandID || current.RequestHash != attempt.RequestHash ||
			(current.Disposition != D115Prepared && current.Disposition != D115DefinitelyUnsent &&
				current.Disposition != D115OutcomeUnknown && current.Disposition != D115PossiblySubmitted) ||
			!bytes.Equal(current.RequestBytes, attempt.RequestBytes) {
			return ErrInvalidClientState
		}
		previousUnknown := current.Disposition == D115OutcomeUnknown || hasPossiblySubmitted(current.Exchanges)
		appendD115Exchange(current, D115PossiblySubmitted, "send-marker")
		if previousUnknown {
			current.Disposition = D115OutcomeUnknown
		} else {
			current.Disposition = D115PossiblySubmitted
		}
		return nil
	})
	if err != nil {
		return d115.Receipt{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return d115.Receipt{}, recordD115Unknown(ledger, attempt, "transport.outcome-unknown")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 8193))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 8192 {
		clear(body)
		return d115.Receipt{}, recordD115Unknown(ledger, attempt, "protocol.invalid-response")
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/cbor" {
		code := strings.TrimSpace(string(body))
		clear(body)
		if len(code) > 128 || strings.ContainsAny(code, "\r\n") {
			code = "protocol.invalid-response"
		}
		if response.StatusCode >= 500 || code == "authority.unavailable" {
			return d115.Receipt{}, recordD115Unknown(ledger, attempt, code)
		}
		return d115.Receipt{}, recordD115Response(ledger, attempt, code)
	}
	receipt, err := d115.DecodeReceipt(body)
	if err != nil {
		clear(body)
		return d115.Receipt{}, recordD115Unknown(ledger, attempt, "protocol.invalid-receipt")
	}
	submit, submitErr := d115.DecodeSubmit(attempt.RequestBytes)
	_, claims, grantErr := d115.DecodeGrant(submit.Grant)
	expectedHash, hashErr := d115.RequestHash(submit.RealmID, submit.CommandID, d115.GrantDigest(submit.Grant))
	if submitErr != nil || grantErr != nil || hashErr != nil || submit.RealmID != attempt.RealmID || submit.CommandID != attempt.CommandID ||
		submit.RequestHash != attempt.RequestHash || expectedHash != attempt.RequestHash || d115.GrantDigest(submit.Grant) != attempt.GrantDigest ||
		claims.Audience != attempt.RealmID || claims.GrantID != attempt.GrantID || claims.OwnerKeyID != attempt.OwnerKeyID ||
		receipt.RealmID != attempt.RealmID || receipt.CommandID != attempt.CommandID || receipt.RequestHash != attempt.RequestHash ||
		receipt.Operation != (d115.Operation{Name: "repo.create-local", Version: 1}) || receipt.OwnerKeyID != attempt.OwnerKeyID || receipt.GrantID != attempt.GrantID {
		clear(body)
		return d115.Receipt{}, recordD115Unknown(ledger, attempt, "protocol.receipt-mismatch")
	}
	attempt, err = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		if len(current.ReceiptBytes) != 0 && !bytes.Equal(current.ReceiptBytes, body) {
			return ErrInvalidClientState
		}
		current.ReceiptBytes = bytes.Clone(body)
		current.Disposition = D115ReceiptDurable
		appendD115Exchange(current, D115ReceiptDurable, "result.succeeded")
		return nil
	})
	clear(body)
	if err != nil {
		return d115.Receipt{}, err
	}
	receipt, err = installD115Receipt(ledger, profileRoot, attempt)
	if err != nil {
		return d115.Receipt{}, fmt.Errorf("%w: %v", ErrD115ReceiptDurable, err)
	}
	return receipt, nil
}

func installD115Receipt(ledger *d115Ledger, profileRoot string, attempt D115Attempt) (d115.Receipt, error) {
	receipt, err := d115.DecodeReceipt(attempt.ReceiptBytes)
	if err != nil || receipt.RealmID != attempt.RealmID || receipt.CommandID != attempt.CommandID || receipt.RequestHash != attempt.RequestHash || receipt.OwnerKeyID != attempt.OwnerKeyID || receipt.GrantID != attempt.GrantID {
		return d115.Receipt{}, ErrInvalidClientState
	}
	receiptDirectory, err := ensureD115ReceiptDirectory(profileRoot)
	if err != nil {
		return d115.Receipt{}, err
	}
	path := filepath.Join(receiptDirectory, attempt.CommandID+".cbor")
	existing, readErr := readPrivateRegular(path, 8192, 0o600)
	if readErr == nil {
		exact := bytes.Equal(existing, attempt.ReceiptBytes)
		clear(existing)
		if !exact {
			return d115.Receipt{}, ErrInvalidClientState
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		if err = WriteD115OwnerFile(path, attempt.ReceiptBytes); err != nil {
			return d115.Receipt{}, err
		}
	} else {
		return d115.Receipt{}, readErr
	}
	_, err = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		current.Disposition = D115Installed
		current.RequestBytes = nil
		return nil
	})
	return receipt, err
}

func ensureD115ReceiptDirectory(profileRoot string) (string, error) {
	ledgerDirectory := filepath.Join(profileRoot, "d115-attempts")
	info, err := os.Lstat(ledgerDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return "", ErrInvalidClientState
	}
	receiptDirectory := filepath.Join(ledgerDirectory, "receipts")
	if err = os.Mkdir(receiptDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err = syncProfileDirectory(ledgerDirectory); err != nil {
		return "", err
	}
	info, err = os.Lstat(receiptDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return "", ErrInvalidClientState
	}
	return receiptDirectory, nil
}

func recordD115Unsent(ledger *d115Ledger, attempt D115Attempt, code string) error {
	updated, err := ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		appendD115Exchange(current, D115DefinitelyUnsent, code)
		if hasPossiblySubmitted(current.Exchanges) {
			current.Disposition = D115OutcomeUnknown
		} else {
			current.Disposition = D115DefinitelyUnsent
		}
		return nil
	})
	if err != nil {
		return err
	}
	if updated.Disposition == D115OutcomeUnknown {
		return ErrD115OutcomeUnknown
	}
	return ErrD115DefinitelyUnsent
}

func recordD115Unknown(ledger *d115Ledger, attempt D115Attempt, code string) error {
	_, err := ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		current.Disposition = D115OutcomeUnknown
		appendD115Exchange(current, D115OutcomeUnknown, code)
		return nil
	})
	if err != nil {
		return err
	}
	return ErrD115OutcomeUnknown
}

func recordD115Response(ledger *d115Ledger, attempt D115Attempt, code string) error {
	updated, err := ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		appendD115Exchange(current, "response", code)
		if hasPossiblySubmitted(current.Exchanges) {
			current.Disposition = D115OutcomeUnknown
		} else {
			current.Disposition = D115Refused
		}
		return nil
	})
	if err != nil {
		return err
	}
	if updated.Disposition == D115OutcomeUnknown {
		return ErrD115OutcomeUnknown
	}
	return fmt.Errorf("%w: %s", ErrD115LocalRefused, code)
}

func appendD115Exchange(attempt *D115Attempt, disposition D115Disposition, code string) {
	number := uint64(1)
	for _, event := range attempt.Exchanges {
		if event.Number >= number {
			number = event.Number + 1
		}
	}
	attempt.Exchanges = append(attempt.Exchanges, D115Exchange{Number: number, Disposition: string(disposition), ResultCode: code, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)})
}

func hasPossiblySubmitted(events []D115Exchange) bool {
	for _, event := range events {
		if event.Disposition == string(D115PossiblySubmitted) || event.Disposition == string(D115OutcomeUnknown) || event.Disposition == string(D115ReceiptDurable) {
			return true
		}
	}
	return false
}

func validD115CheckoutBinding(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == 32 && value == "sha256:"+strings.ToLower(hex.EncodeToString(decoded))
}
