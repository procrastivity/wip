package wipdseed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdprofile"
	"github.com/procrastivity/wip/internal/wipdwire"
	"golang.org/x/sys/unix"
)

const environmentCredentialName = "environment-credential.json"

// EnvironmentCredential is the canonical profile-local Environment identity.
// It deliberately contains no Repo ID, enrollment grant, journal, or route
// result; those belong to independent authority and runtime state.
type EnvironmentCredential struct {
	Schema          string   `json:"schema"`
	DomainID        string   `json:"domain_id"`
	Epoch           uint64   `json:"authority_epoch"`
	EnvironmentID   string   `json:"environment_id"`
	OwnerKeyID      string   `json:"owner_key_id"`
	SPKIDigest      string   `json:"spki_digest"`
	PrivateKeyPKCS8 []byte   `json:"private_key_pkcs8"`
	CertificateDER  [][]byte `json:"certificate_chain_der"`
}

// EnrollEnvironment enrolls a durable pending identity without creating any
// Repo-bound client state. The D117 grant is sent only in the enrollment
// request and is never written to profile state. If the response is lost, the
// same pending CSR and caller-supplied grant can be replayed to recover the
// authority's exact previously issued certificate.
func EnrollEnvironment(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, ownerRoot ed25519.PublicKey, caDelegation []byte, identity PreparedIdentity, grant []byte, profileRoot string) (EnvironmentCredential, error) {
	var empty EnvironmentCredential
	if ctx == nil || len(grant) == 0 || len(grant) > 4096 {
		return empty, ErrInvalidClientState
	}
	resolved, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return empty, err
	}
	lock, err := lockInstalledState(resolved.Root)
	if err != nil {
		return empty, err
	}
	defer unlockInstalledState(lock)
	if _, loadErr := loadEnvironmentCredential(resolved.Root); loadErr == nil {
		credential, _, recoverErr := LoadEnvironmentCredential(resolved.Root, profile, ownerRoot, caDelegation, time.Now().UTC())
		if recoverErr != nil {
			return empty, recoverErr
		}
		if recoverErr = reconcilePendingCredential(resolved.Root, credential); recoverErr != nil {
			clear(credential.PrivateKeyPKCS8)
			return empty, recoverErr
		}
		return credential, nil
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return empty, loadErr
	}
	if err = requirePendingIdentity(resolved.Root, identity); err != nil {
		return empty, err
	}
	trustedCA, err := wipdauthority.VerifyEnvironmentCADelegation(profile, ownerRoot, caDelegation, time.Now().UTC())
	if err != nil {
		return empty, ErrInvalidClientState
	}
	privateKey, csr, spkiDigest, err := validatePrepared(identity)
	if err != nil {
		return empty, err
	}
	defer clear(privateKey)
	enrollmentClient, err := profile.HTTPClient(roots)
	if err != nil {
		return empty, err
	}
	defer enrollmentClient.CloseIdleConnections()
	request := wipdwire.EnrollmentRequest{Schema: "wipd.enrollment-request/1", Credential: bytes.Clone(grant), CSRDER: bytes.Clone(identity.CSRDER)}
	defer clear(request.Credential)
	requestFrame, requestID, err := encodeRequestFrame("enrollment.request", request)
	if err != nil {
		return empty, err
	}
	defer clear(requestFrame)
	responseFrame, err := postSingleFrame(ctx, enrollmentClient, profile.Origin()+"/wipd/v1/enroll", requestFrame, requestID)
	if err != nil {
		return empty, err
	}
	if responseFrame.Kind == "problem" {
		return empty, decodeProblem(responseFrame.Payload)
	}
	if responseFrame.Kind != "enrollment.issued" || responseFrame.Sequence != 0 {
		return empty, ErrInvalidClientState
	}
	var issued wipdwire.EnrollmentIssued
	if err = wipdwire.DecodeCanonical(responseFrame.Payload, &issued, "schema", "domain_id", "authority_epoch", "environment_id", "owner_key_id", "spki_digest", "certificate_chain_der"); err != nil {
		return empty, err
	}
	if issued.Schema != "wipd.enrollment-issued/1" || issued.DomainID != profile.DomainID() || issued.Epoch != profile.Epoch() ||
		!clientULIDPattern.MatchString(issued.EnvironmentID) || issued.OwnerKeyID != profile.OwnerRootSPKI() ||
		issued.SPKIDigest != spkiDigest || len(issued.CertificateChain) != 2 {
		return empty, ErrInvalidClientState
	}
	if _, err = verifyIssuedCertificate(issued, trustedCA, csr, privateKey, time.Now()); err != nil {
		return empty, err
	}
	credential := EnvironmentCredential{
		Schema: "wipd.environment-credential/1", DomainID: issued.DomainID, Epoch: issued.Epoch,
		EnvironmentID: issued.EnvironmentID, OwnerKeyID: issued.OwnerKeyID, SPKIDigest: issued.SPKIDigest,
		PrivateKeyPKCS8: bytes.Clone(identity.PrivateKeyPKCS8), CertificateDER: clone2D(issued.CertificateChain),
	}
	if err = installEnvironmentCredential(resolved.Root, credential); err != nil {
		clear(credential.PrivateKeyPKCS8)
		return empty, err
	}
	if err = removePendingIdentity(resolved.Root, identity); err != nil {
		clear(credential.PrivateKeyPKCS8)
		return empty, err
	}
	return credential, nil
}

// ReconcileEnvironmentCredential loads the installed identity and removes an
// exact matching pending file left by a crash after durable installation.
// A conflicting pending identity is never reassigned or silently discarded.
func ReconcileEnvironmentCredential(profileRoot string, profile wipdauthority.Profile, ownerRoot ed25519.PublicKey, caDelegation []byte, now time.Time) (EnvironmentCredential, tls.Certificate, error) {
	credential, certificate, err := LoadEnvironmentCredential(profileRoot, profile, ownerRoot, caDelegation, now)
	if err != nil {
		return EnvironmentCredential{}, tls.Certificate{}, err
	}
	resolved, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		clear(credential.PrivateKeyPKCS8)
		clear(certificate.PrivateKey.(ed25519.PrivateKey))
		return EnvironmentCredential{}, tls.Certificate{}, err
	}
	lock, err := lockInstalledState(resolved.Root)
	if err != nil {
		clear(credential.PrivateKeyPKCS8)
		clear(certificate.PrivateKey.(ed25519.PrivateKey))
		return EnvironmentCredential{}, tls.Certificate{}, err
	}
	defer unlockInstalledState(lock)
	if err = reconcilePendingCredential(resolved.Root, credential); err != nil {
		clear(credential.PrivateKeyPKCS8)
		clear(certificate.PrivateKey.(ed25519.PrivateKey))
		return EnvironmentCredential{}, tls.Certificate{}, err
	}
	return credential, certificate, nil
}

// LoadEnvironmentCredential validates the canonical private credential against
// the explicitly pinned authority profile and owner-delegated Environment CA.
func LoadEnvironmentCredential(profileRoot string, profile wipdauthority.Profile, ownerRoot ed25519.PublicKey, caDelegation []byte, now time.Time) (EnvironmentCredential, tls.Certificate, error) {
	var empty EnvironmentCredential
	resolved, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return empty, tls.Certificate{}, err
	}
	credential, err := loadEnvironmentCredential(resolved.Root)
	if err != nil {
		return empty, tls.Certificate{}, err
	}
	if credential.Schema != "wipd.environment-credential/1" || credential.DomainID != profile.DomainID() ||
		credential.Epoch != profile.Epoch() || credential.OwnerKeyID != profile.OwnerRootSPKI() ||
		!clientULIDPattern.MatchString(credential.EnvironmentID) {
		clear(credential.PrivateKeyPKCS8)
		return empty, tls.Certificate{}, ErrInvalidClientState
	}
	trustedCA, err := wipdauthority.VerifyEnvironmentCADelegation(profile, ownerRoot, caDelegation, now.UTC())
	if err != nil {
		clear(credential.PrivateKeyPKCS8)
		return empty, tls.Certificate{}, ErrInvalidClientState
	}
	privateKey, leaf, digest, err := validateCredentialCertificate(credential, trustedCA, now)
	if err != nil {
		clear(credential.PrivateKeyPKCS8)
		return empty, tls.Certificate{}, err
	}
	if digest != credential.SPKIDigest {
		clear(privateKey)
		clear(credential.PrivateKeyPKCS8)
		return empty, tls.Certificate{}, ErrInvalidClientState
	}
	chain := clone2D(credential.CertificateDER)
	return credential, tls.Certificate{Certificate: chain, PrivateKey: privateKey, Leaf: leaf}, nil
}

func validateCredentialCertificate(credential EnvironmentCredential, trustedCA []byte, now time.Time) (ed25519.PrivateKey, *x509.Certificate, string, error) {
	var emptyKey ed25519.PrivateKey
	if len(credential.CertificateDER) != 2 || !bytes.Equal(credential.CertificateDER[1], trustedCA) {
		return emptyKey, nil, "", ErrInvalidClientState
	}
	leaf, err := x509.ParseCertificate(credential.CertificateDER[0])
	if err != nil {
		return emptyKey, nil, "", ErrInvalidClientState
	}
	ca, err := x509.ParseCertificate(credential.CertificateDER[1])
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 || ca.KeyUsage != x509.KeyUsageCertSign || ca.CheckSignatureFrom(ca) != nil {
		return emptyKey, nil, "", ErrInvalidClientState
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(credential.PrivateKeyPKCS8)
	if err != nil {
		return emptyKey, nil, "", ErrInvalidClientState
	}
	key, ok := keyValue.(ed25519.PrivateKey)
	if !ok {
		return emptyKey, nil, "", ErrInvalidClientState
	}
	publicDER, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil || !bytes.Equal(publicDER, leaf.RawSubjectPublicKeyInfo) {
		clear(key)
		return emptyKey, nil, "", ErrInvalidClientState
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	wantURI := "wipd://environment/" + credential.EnvironmentID + "?domain=" + credential.DomainID + "&epoch=" + strconv.FormatUint(credential.Epoch, 10) + "&owner=" + strings.TrimPrefix(credential.OwnerKeyID, "sha256:")
	if leaf.IsCA || !leaf.BasicConstraintsValid || !hasCriticalCertificateExtension(leaf, []int{2, 5, 29, 19}) ||
		leaf.KeyUsage != x509.KeyUsageDigitalSignature || !hasCriticalCertificateExtension(leaf, []int{2, 5, 29, 15}) ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(leaf.UnknownExtKeyUsage) != 0 ||
		len(leaf.DNSNames) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.IPAddresses) != 0 || len(leaf.URIs) != 1 || leaf.URIs[0] == nil ||
		leaf.URIs[0].String() != wantURI || leaf.NotBefore.Before(ca.NotBefore) || leaf.NotAfter.After(ca.NotAfter) ||
		!leaf.NotBefore.Before(leaf.NotAfter) || leaf.NotAfter.Sub(leaf.NotBefore) > maxEnvironmentCertificateLifetime ||
		now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || leaf.CheckSignatureFrom(ca) != nil || !hasCriticalSAN(leaf) {
		clear(key)
		return emptyKey, nil, "", ErrInvalidClientState
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		clear(key)
		return emptyKey, nil, "", ErrInvalidClientState
	}
	return key, leaf, digest, nil
}

func unlockInstalledState(lock *os.File) {
	if lock == nil {
		return
	}
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	_ = lock.Close()
}

func requirePendingIdentity(directory string, identity PreparedIdentity) error {
	pending, err := LoadPending(directory)
	if err != nil {
		return err
	}
	defer clear(pending.PrivateKeyPKCS8)
	if !bytes.Equal(pending.PrivateKeyPKCS8, identity.PrivateKeyPKCS8) || !bytes.Equal(pending.CSRDER, identity.CSRDER) {
		return ErrStateExists
	}
	return nil
}

func reconcilePendingCredential(directory string, credential EnvironmentCredential) error {
	pending, err := LoadPending(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer clear(pending.PrivateKeyPKCS8)
	_, _, digest, err := validatePrepared(pending)
	if err != nil || digest != credential.SPKIDigest || !bytes.Equal(pending.PrivateKeyPKCS8, credential.PrivateKeyPKCS8) {
		return ErrStateExists
	}
	if err = os.Remove(filepath.Join(directory, pendingName)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func loadEnvironmentCredential(directory string) (EnvironmentCredential, error) {
	var credential EnvironmentCredential
	path := filepath.Join(directory, environmentCredentialName)
	info, err := os.Lstat(path)
	if err != nil {
		return credential, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1<<20 {
		return credential, ErrInvalidClientState
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credential, err
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&credential); err != nil || decoder.Decode(new(any)) != io.EOF || len(credential.PrivateKeyPKCS8) == 0 || len(credential.CertificateDER) != 2 {
		clear(credential.PrivateKeyPKCS8)
		return EnvironmentCredential{}, ErrInvalidClientState
	}
	return credential, nil
}

func validateStateCredential(directory string, state ClientState) error {
	return validateStateCredentialMode(directory, state, false)
}

func validateStateCredentialRequired(directory string, state ClientState) error {
	return validateStateCredentialMode(directory, state, true)
}

func validateStateCredentialMode(directory string, state ClientState, required bool) error {
	if _, err := os.Lstat(filepath.Join(directory, environmentCredentialName)); err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return ErrInvalidClientState
		}
		return err
	}
	credential, err := loadEnvironmentCredential(directory)
	if err != nil {
		return err
	}
	defer clear(credential.PrivateKeyPKCS8)
	if credential.DomainID != state.DomainID || credential.Epoch != state.Epoch || credential.EnvironmentID != state.EnvironmentID ||
		credential.OwnerKeyID != state.OwnerKeyID {
		return ErrInvalidClientState
	}
	return nil
}

func equalDERChain(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !bytes.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}

func installEnvironmentCredential(directory string, credential EnvironmentCredential) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	defer clear(data)
	target := filepath.Join(directory, environmentCredentialName)
	if existing, readErr := loadEnvironmentCredential(directory); readErr == nil {
		existingData, _ := json.Marshal(existing)
		clear(existing.PrivateKeyPKCS8)
		if bytes.Equal(existingData, data) {
			clear(existingData)
			return nil
		}
		clear(existingData)
		return ErrStateExists
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	temp, err := os.CreateTemp(directory, ".environment-credential-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(tempPath, target); err != nil {
		if existing, readErr := os.ReadFile(target); readErr == nil && bytes.Equal(existing, data) {
			clear(existing)
			return syncDirectory(directory)
		}
		return ErrStateExists
	}
	return syncDirectory(directory)
}

func removePendingIdentity(directory string, identity PreparedIdentity) error {
	if err := requirePendingIdentity(directory, identity); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, pendingName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(directory)
}
