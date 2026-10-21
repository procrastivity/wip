package wipdseed

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdprofile"
)

const d115ProfileName = "d115-provisioning-profile.json"

// D115ProvisioningConfig is the domainless TLS pin and realm binding for D115.
type D115ProvisioningConfig struct {
	Schema                  string `json:"schema"`
	Name                    string `json:"name"`
	Origin                  string `json:"origin"`
	ProvisioningRealmID     string `json:"provisioning_realm_id"`
	AuthoritySPKIPin        string `json:"authority_spki_pin"`
	AuthorityCertificateDER []byte `json:"authority_certificate_der"`
}

// SaveD115ProvisioningConfig installs the immutable owner-only provisioning profile.
func SaveD115ProvisioningConfig(profileRoot string, config D115ProvisioningConfig) error {
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return err
	}
	if err = validateD115ProvisioningConfig(config); err != nil {
		return err
	}
	info, err := os.Lstat(profile.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return ErrInvalidClientState
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > 1<<20 {
		return ErrInvalidClientState
	}
	path := filepath.Join(profile.Root, d115ProfileName)
	if existing, readErr := readD115ProvisioningConfig(path); readErr == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return errors.New("wipdseed: conflicting D115 provisioning profile already exists")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	temp, err := os.CreateTemp(profile.Root, ".d115-provisioning-*")
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
	if err = os.Link(tempPath, path); err != nil {
		if existing, readErr := readD115ProvisioningConfig(path); readErr == nil && bytes.Equal(existing, data) {
			return syncProfileDirectory(profile.Root)
		}
		return err
	}
	return syncProfileDirectory(profile.Root)
}

// LoadD115ProvisioningConfig reads and validates the explicit private profile.
func LoadD115ProvisioningConfig(profileRoot string) (D115ProvisioningConfig, error) {
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return D115ProvisioningConfig{}, err
	}
	data, err := readD115ProvisioningConfig(filepath.Join(profile.Root, d115ProfileName))
	if err != nil {
		return D115ProvisioningConfig{}, err
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config D115ProvisioningConfig
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || validateD115ProvisioningConfigAt(config, time.Time{}) != nil {
		return D115ProvisioningConfig{}, ErrInvalidClientState
	}
	return config, nil
}

func readD115ProvisioningConfig(path string) ([]byte, error) {
	return readPrivateRegular(path, 1<<20, 0o600)
}

func validateD115ProvisioningConfig(config D115ProvisioningConfig) error {
	return validateD115ProvisioningConfigAt(config, time.Now())
}

func validateD115ProvisioningConfigAt(config D115ProvisioningConfig, now time.Time) error {
	if config.Schema != "wipd.d115-provisioning-profile/1" || config.Name == "" || strings.TrimSpace(config.Name) != config.Name ||
		strings.ContainsAny(config.Name, "/\\") || len(config.AuthorityCertificateDER) == 0 || len(config.AuthorityCertificateDER) > 1<<20 {
		return ErrInvalidClientState
	}
	if _, err := wipdauthority.NewProvisioningProfile(config.Origin, config.ProvisioningRealmID, config.AuthoritySPKIPin); err != nil {
		return ErrInvalidClientState
	}
	certificate, err := x509.ParseCertificate(config.AuthorityCertificateDER)
	if err != nil || !bytes.Equal(certificate.Raw, config.AuthorityCertificateDER) || certificate.VerifyHostname(profileHost(config.Origin)) != nil ||
		!now.IsZero() && (now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter)) || !hasD115ServerEKU(certificate) || !hasCriticalD115SAN(certificate) ||
		len(certificate.URIs) != 1 || certificate.URIs[0] == nil || certificate.URIs[0].String() != "wipd://provisioning-realm/"+config.ProvisioningRealmID {
		return ErrInvalidClientState
	}
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	encoded, _ := hex.DecodeString(strings.TrimPrefix(config.AuthoritySPKIPin, "sha256:"))
	if len(encoded) != sha256.Size || subtle.ConstantTimeCompare(digest[:], encoded) != 1 {
		return ErrInvalidClientState
	}
	return nil
}

func profileHost(origin string) string {
	parsed, _ := url.Parse(origin)
	if parsed == nil {
		return ""
	}
	return parsed.Hostname()
}

func hasD115ServerEKU(certificate *x509.Certificate) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return false
}

func hasCriticalD115SAN(certificate *x509.Certificate) bool {
	count, critical := 0, false
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal([]int{2, 5, 29, 17}) {
			count++
			critical = extension.Critical
		}
	}
	return count == 1 && critical
}

func syncProfileDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
