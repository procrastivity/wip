package wipdremote

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/procrastivity/wip/internal/reporoute"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdprofile"
	"github.com/procrastivity/wip/internal/wipdseed"
)

const routeConfigName = "route-resolver.json"

// PrebindingProfile is an explicitly provisioned D54 connection. The
// credential directory names the canonical profile-local Environment
// credential record; it contains no Repo identity or journal.
type PrebindingProfile struct {
	Name                    string `json:"name"`
	Origin                  string `json:"origin"`
	DomainID                string `json:"domain_id"`
	Epoch                   uint64 `json:"authority_epoch"`
	RepoID                  string `json:"repo_id"`
	AuthoritySPKIPin        string `json:"authority_spki_pin"`
	OwnerRootSPKI           string `json:"owner_root_spki"`
	AuthorityCertificateDER []byte `json:"authority_certificate_der"`
	ArtifactKeyCertificate  []byte `json:"artifact_key_certificate"`
	OwnerRootPublicKey      []byte `json:"owner_root_public_key"`
	EnvironmentCADElegation []byte `json:"environment_ca_delegation"`
	CredentialDirectory     string `json:"credential_directory"`
}

// PrebindingConfig is trusted local provisioning for read-only route.resolve.
// It is protected by the private daemon profile directory and is never
// accepted from local IPC callers.
type PrebindingConfig struct {
	Schema   string              `json:"schema"`
	Entries  []reporoute.Entry   `json:"entries"`
	Profiles []PrebindingProfile `json:"profiles"`
}

// SavePrebindingConfig installs one immutable, owner-only D54 resolver
// configuration. Conflicting replacement requires explicit reprovisioning.
func SavePrebindingConfig(profileRoot string, config PrebindingConfig) error {
	if err := validatePrebindingConfig(config); err != nil {
		return err
	}
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return err
	}
	info, err := os.Lstat(profile.Root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("wipdremote: route profile root must be a private directory")
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > configSize {
		return errors.New("wipdremote: route profile is invalid or too large")
	}
	defer clear(data)
	path := filepath.Join(profile.Root, routeConfigName)
	if existing, readErr := readRouteConfig(path); readErr == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return errors.New("wipdremote: conflicting route profile already exists")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	temp, err := os.CreateTemp(profile.Root, ".route-resolver-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
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
	if err = os.Link(tempName, path); err != nil {
		if existing, readErr := readRouteConfig(path); readErr == nil && bytes.Equal(existing, data) {
			return syncProfileDirectory(profile.Root)
		}
		return err
	}
	return syncProfileDirectory(profile.Root)
}

func hasPrebindingRouteProfile(profileRoot string) (bool, error) {
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return false, err
	}
	_, err = readRouteConfig(filepath.Join(profile.Root, routeConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func configurePrebindingRoutes(server *wipd.Server, profileRoot string) error {
	resolver, err := loadPrebindingResolver(profileRoot)
	if err != nil {
		return err
	}
	if resolver == nil {
		return nil
	}
	return server.ConfigureRouteResolve(resolver)
}

func loadPrebindingResolver(profileRoot string) (*wipdseed.LocalRouteResolver, error) {
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return nil, err
	}
	data, err := readRouteConfig(filepath.Join(profile.Root, routeConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config PrebindingConfig
	if err = decoder.Decode(&config); err != nil || decoder.Decode(new(any)) != io.EOF || validatePrebindingConfig(config) != nil {
		return nil, errors.New("wipdremote: invalid route resolver profile")
	}
	connections := make(map[string]wipdseed.PrebindingConnection, len(config.Profiles))
	for _, item := range config.Profiles {
		connection, buildErr := buildPrebindingConnection(profile.Root, item)
		if buildErr != nil {
			return nil, buildErr
		}
		connections[item.Name] = connection
	}
	ledger, err := wipdseed.OpenEnvironmentAttachLedger(profile.Root)
	if err != nil {
		return nil, err
	}
	return &wipdseed.LocalRouteResolver{
		Entries: append([]reporoute.Entry(nil), config.Entries...), Connections: connections,
		RememberedRoute: ledger.RememberedRouteBinding,
	}, nil
}

func buildPrebindingConnection(profileRoot string, item PrebindingProfile) (wipdseed.PrebindingConnection, error) {
	var empty wipdseed.PrebindingConnection
	profile, err := wipdauthority.NewProfile(item.Origin, item.DomainID, item.Epoch, item.AuthoritySPKIPin, item.OwnerRootSPKI)
	if err != nil {
		return empty, err
	}
	profile, err = profile.WithM5LabRepoID(item.RepoID)
	if err != nil {
		return empty, err
	}
	owner := ed25519.PublicKey(bytes.Clone(item.OwnerRootPublicKey))
	ownerDER, err := x509.MarshalPKIXPublicKey(owner)
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(ownerDER)
	if "sha256:"+hex.EncodeToString(digest[:]) != item.OwnerRootSPKI {
		return empty, errors.New("wipdremote: route profile owner key does not match its pin")
	}
	authorityCertificate, err := x509.ParseCertificate(item.AuthorityCertificateDER)
	if err != nil || !bytes.Equal(authorityCertificate.Raw, item.AuthorityCertificateDER) {
		return empty, errors.New("wipdremote: invalid route profile authority certificate")
	}
	authorityDigest := sha256.Sum256(authorityCertificate.RawSubjectPublicKeyInfo)
	if "sha256:"+hex.EncodeToString(authorityDigest[:]) != item.AuthoritySPKIPin {
		return empty, errors.New("wipdremote: route profile authority certificate does not match its pin")
	}
	roots := x509.NewCertPool()
	roots.AddCert(authorityCertificate)
	credentialRoot := filepath.Join(profileRoot, item.CredentialDirectory)
	return wipdseed.PrebindingConnection{
		AuthorityProfile: profile, AuthorityRoots: roots, OwnerRoot: owner,
		CADElegation: bytes.Clone(item.EnvironmentCADElegation), ProfileRoot: credentialRoot,
		ArtifactKeyCertificate: bytes.Clone(item.ArtifactKeyCertificate),
	}, nil
}

func validatePrebindingConfig(config PrebindingConfig) error {
	if config.Schema != "wipd.route-resolver-profile/1" || len(config.Entries) == 0 || len(config.Profiles) == 0 ||
		len(config.Entries) > 256 || len(config.Profiles) > 64 {
		return errors.New("wipdremote: invalid route resolver profile")
	}
	profiles := make(map[string]PrebindingProfile, len(config.Profiles))
	for _, profile := range config.Profiles {
		if profile.Name == "" || strings.TrimSpace(profile.Name) != profile.Name || strings.ContainsAny(profile.Name, "/\\") ||
			profile.DomainID == "" || profile.Epoch == 0 || profile.RepoID == "" || len(profile.OwnerRootPublicKey) != ed25519.PublicKeySize ||
			len(profile.AuthorityCertificateDER) == 0 || len(profile.EnvironmentCADElegation) == 0 ||
			!safeRelativeProfileDirectory(profile.CredentialDirectory) {
			return errors.New("wipdremote: invalid route connection profile")
		}
		if _, exists := profiles[profile.Name]; exists {
			return errors.New("wipdremote: duplicate route connection profile")
		}
		if _, err := buildPrebindingConnection("/profile", profile); err != nil {
			return fmt.Errorf("wipdremote: invalid route connection profile: %w", err)
		}
		profiles[profile.Name] = profile
	}
	for _, entry := range config.Entries {
		if _, err := reporoute.Select([]reporoute.Entry{entry}, routeValidationKey(entry.Key)); err != nil {
			return fmt.Errorf("wipdremote: invalid D54 route: %w", err)
		}
		profile, ok := profiles[entry.ConnectionProfile]
		if !ok || profile.DomainID != entry.DomainID {
			return errors.New("wipdremote: D54 route has no matching domain connection profile")
		}
	}
	return nil
}

func routeValidationKey(rule string) string {
	if strings.HasSuffix(rule, "/**") {
		return strings.TrimSuffix(rule, "**") + "validation"
	}
	if rule == "**" {
		return "validation/repository"
	}
	return rule
}

func safeRelativeProfileDirectory(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func readRouteConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > configSize {
		return nil, errors.New("wipdremote: route profile must be a private regular file")
	}
	return os.ReadFile(path)
}
