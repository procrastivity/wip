// Package wipdremote composes the connected M5 birth-command runtime without
// resolving or switching the legacy WIP store.
package wipdremote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdprofile"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	configName = "connected-authority.json"
	configSize = 1 << 20
)

// ErrNotConfigured means the daemon profile has no connected-authority config.
var ErrNotConfigured = errors.New("wipdremote: connected authority profile is not configured")

// Config is the private local pin set installed by the M5 lab client-enroll
// worker. It is not an M2 wire or normative profile schema.
type Config struct {
	Schema                  string `json:"schema"`
	Origin                  string `json:"origin"`
	DomainID                string `json:"domain_id"`
	Epoch                   uint64 `json:"authority_epoch"`
	RepoID                  string `json:"repo_id"`
	OwnerRootSPKI           string `json:"owner_root_spki"`
	AuthoritySPKIPin        string `json:"authority_spki_pin"`
	AuthorityCertificateDER []byte `json:"authority_certificate_der"`
	OwnerRootPublicKey      []byte `json:"owner_root_public_key"`
	ArtifactKeyCertificate  []byte `json:"artifact_key_certificate"`
	ClientStateDirectory    string `json:"client_state_directory"`
}

// SaveConfig durably creates an owner-only connected-authority profile. An
// exact retry is idempotent; conflicting bytes never replace existing pins.
func SaveConfig(profileRoot string, config Config) error {
	if err := validateConfig(config); err != nil {
		return err
	}
	if err := os.MkdirAll(profileRoot, 0o700); err != nil {
		return err
	}
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return err
	}
	info, err := os.Lstat(profile.Root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("wipdremote: connected profile root must be a private directory")
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > configSize {
		return errors.New("wipdremote: connected profile is invalid or too large")
	}
	defer clear(data)
	path := filepath.Join(profile.Root, configName)
	if existing, readErr := readConfig(path); readErr == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return errors.New("wipdremote: conflicting connected profile already exists")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	temp, err := os.CreateTemp(profile.Root, ".connected-authority-*")
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
		if existing, readErr := readConfig(path); readErr == nil && bytes.Equal(existing, data) {
			return syncProfileDirectory(profile.Root)
		}
		return err
	}
	return syncProfileDirectory(profile.Root)
}

func syncProfileDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

// LoadConfig reads and strictly validates the connected profile without
// following symlinks or accepting group/world-readable configuration.
func LoadConfig(profileRoot string) (Config, error) {
	var empty Config
	profile, err := wipdprofile.Resolve(profileRoot)
	if err != nil {
		return empty, err
	}
	data, err := readConfig(filepath.Join(profile.Root, configName))
	if errors.Is(err, os.ErrNotExist) {
		return empty, ErrNotConfigured
	}
	if err != nil {
		return empty, err
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config Config
	if err = decoder.Decode(&config); err != nil || decoder.Decode(new(any)) != io.EOF || validateConfig(config) != nil {
		return empty, errors.New("wipdremote: invalid connected authority profile")
	}
	return config, nil
}

// Runtime owns the remote mTLS client and Environment journal writer lease.
type Runtime struct {
	config  Config
	profile wipdauthority.Profile
	client  *wipdseed.CommandExchangeClient
	state   wipdseed.ClientState
	journal *wipdjournal.Journal
}

// NewServer creates a normal empty server when no connected profile is
// installed, and otherwise composes the authenticated command runtime before
// returning a daemon server ready to Serve.
func NewServer(profileRoot string) (*wipd.Server, *Runtime, error) {
	config, err := LoadConfig(profileRoot)
	if errors.Is(err, ErrNotConfigured) {
		return wipd.NewServer(), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	runtime, err := OpenRuntime(profileRoot, config)
	if err != nil {
		return nil, nil, err
	}
	registry, err := wipdauthority.NewM5BirthRegistry()
	if err != nil {
		_ = runtime.Close()
		return nil, nil, err
	}
	server := wipd.NewServerWithRegistry(registry)
	environment, err := wipd.NewJournalCommandStartEnvironment(runtime.journal)
	if err == nil {
		err = server.ConfigureConnectedCommands(config.DomainID, runtime.journal, runtime, environment)
	}
	if err != nil {
		_ = runtime.Close()
		return nil, nil, err
	}
	return server, runtime, nil
}

// OpenRuntime opens the validated authority client and the identity-bound
// Environment journal, recovering the verified enrollment prefix if needed.
func OpenRuntime(profileRoot string, config Config) (*Runtime, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	profile, roots, err := authorityProfile(config)
	if err != nil {
		return nil, err
	}
	registry, err := wipdauthority.NewM5BirthRegistry()
	if err != nil {
		return nil, err
	}
	definitions := registry.Definitions()
	operations := make([]operation.ID, 0, len(definitions))
	for _, definition := range definitions {
		operations = append(operations, definition.Metadata().Operation)
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := wipdseed.OpenCommandExchangeClient(startupCtx, profile, roots, config.ClientStateDirectory, operations)
	if err != nil {
		return nil, err
	}
	state := client.State()
	if state.DomainID != config.DomainID || state.Epoch != config.Epoch || state.RepoID != config.RepoID {
		_ = client.Close()
		return nil, wipdseed.ErrInvalidClientState
	}
	identity := wipdjournal.Identity{
		RepoID: state.RepoID, DomainID: state.DomainID, AuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, OwnerRootSPKI: state.OwnerKeyID,
	}
	journal, err := wipdjournal.Open(filepath.Join(profileRoot, "environment-journal"), identity)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	runtime := &Runtime{config: config, profile: profile, client: client, state: state, journal: journal}
	if err = runtime.reconcileSeed(context.Background()); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	return runtime, nil
}

// Return submits or queries the exact journal identity, then obtains the
// verified authority tail used by the coordinator's atomic fold install.
func (runtime *Runtime) Return(ctx context.Context, entry wipdjournal.Entry, installed wipdwire.PrefixAnchor) (wipd.CommandFold, error) {
	var empty wipd.CommandFold
	canonical := bytes.Clone(entry.CanonicalBytes)
	if len(canonical) == 0 {
		return empty, wipd.ErrCommandStartIdentity
	}
	submit := wipdwire.CommandSubmit{Schema: "wipd.command-submit/1", CanonicalCommand: canonical, RequestHash: entry.RequestHash}
	frames, err := runtime.client.Exchange(ctx, "command.submit", submit)
	if err != nil {
		return empty, err
	}
	receipt, err := terminalFromSubmit(ctx, runtime.client, runtime.state.DomainID, entry, frames)
	if err != nil {
		return empty, err
	}
	resultCode, err := terminalResultCode(receipt)
	if err != nil {
		return empty, err
	}
	pull, err := runtime.pull(ctx, installed)
	if err != nil {
		return empty, err
	}
	return wipd.CommandFold{
		DomainID: runtime.state.DomainID, Epoch: runtime.state.Epoch,
		CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
		EnvironmentID: entry.Command.EnvironmentID, EnvironmentSeq: entry.EnvironmentSeq,
		JournalPosition: entry.JournalPosition, Start: installed, End: pull.End,
		ResultCode: resultCode, CanonicalReceipt: receipt, Manifest: pull.Manifest,
		VerifiedTransfer: pull.VerifiedTransfer,
	}, nil
}

// Pull fetches and verifies the complete authority tail from the exact local
// anchor before it can be installed atomically with the overlay rebuild.
func (runtime *Runtime) Pull(ctx context.Context, installed wipdwire.PrefixAnchor) (wipd.CommandPull, error) {
	return runtime.pull(ctx, installed)
}

// AcknowledgeBirthJournalEntry publishes only the exact terminal receipt
// already committed to the Environment's installed prefix.
func (runtime *Runtime) AcknowledgeBirthJournalEntry(ctx context.Context, ack wipdwire.BirthJournalAck) error {
	if runtime == nil || runtime.client == nil || ack.DomainID != runtime.state.DomainID || ack.Epoch != runtime.state.Epoch ||
		ack.Schema != "wipd.birth-journal-ack/1" {
		return wipdseed.ErrInvalidClientState
	}
	frames, err := runtime.client.Exchange(ctx, "birth-journal.ack", ack)
	if err != nil {
		return err
	}
	if len(frames) != 1 || frames[0].Kind != "birth-journal.acknowledged" {
		return wipdseed.ErrInvalidClientState
	}
	var acknowledged wipdwire.BirthJournalAcked
	if wipdwire.DecodeCanonical(frames[0].Payload, &acknowledged,
		"schema", "domain_id", "matter_id", "command_id", "request_hash") != nil ||
		acknowledged.Schema != "wipd.birth-journal-acked/1" || acknowledged.DomainID != ack.DomainID ||
		acknowledged.MatterID != ack.MatterID || acknowledged.CommandID != ack.CommandID || acknowledged.RequestHash != ack.RequestHash {
		return wipdseed.ErrInvalidClientState
	}
	return nil
}

// SubmitBirthClaimRelease sends the Environment's exact durable lifecycle
// identity and returns its terminal receipt with a complete verified tail.
func (runtime *Runtime) SubmitBirthClaimRelease(ctx context.Context, attempt wipdjournal.BirthReleaseCommand, installed wipdwire.PrefixAnchor) ([]byte, operation.ResultCode, wipd.CommandPull, error) {
	var empty wipd.CommandPull
	if runtime == nil || runtime.client == nil || ctx == nil || attempt.ID == "" || attempt.Barrier.Journal != attempt.Barrier.Claim.ID {
		return nil, "", empty, wipdseed.ErrInvalidClientState
	}
	payload := wipdwire.ClaimRelease{
		Schema: "wipd.claim-release/1", CanonicalCommand: bytes.Clone(attempt.CanonicalBytes),
		RequestHash: attempt.RequestHash, Barrier: attempt.Barrier,
	}
	frames, err := runtime.client.Exchange(ctx, "claim.release", payload)
	if err != nil {
		return nil, "", empty, err
	}
	receipt, err := terminalFromBirthRelease(ctx, runtime.client, runtime.state.DomainID, runtime.state.Epoch,
		runtime.state.EnvironmentID, attempt, payload, frames)
	if err != nil {
		return nil, "", empty, err
	}
	code, err := validateBirthReleaseTerminal(receipt, runtime.state.DomainID, runtime.state.Epoch, runtime.state.EnvironmentID, attempt)
	if err != nil {
		return nil, "", empty, err
	}
	tail, err := runtime.pull(ctx, installed)
	if err != nil {
		return nil, "", empty, err
	}
	return receipt, code, tail, nil
}

func (runtime *Runtime) pull(ctx context.Context, installed wipdwire.PrefixAnchor) (wipd.CommandPull, error) {
	var empty wipd.CommandPull
	payload := wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: runtime.state.DomainID,
		Epoch: runtime.state.Epoch, Installed: installed,
	}
	frames, err := runtime.client.Exchange(ctx, "pull.request", payload)
	if err != nil {
		return empty, err
	}
	prior := runtime.state
	prior.Prefix = installed
	prior.EventRecords, err = runtime.journal.EventRecords(ctx)
	if err != nil || len(prior.EventRecords) != int(installed.EventCount) {
		return empty, wipd.ErrCommandStartIdentity
	}
	transfer, manifest, err := wipdseed.VerifyPullTransfer(runtime.profile, prior, installed, frames)
	if err != nil {
		return empty, err
	}
	return wipd.CommandPull{
		DomainID: runtime.state.DomainID, Epoch: runtime.state.Epoch,
		Start: installed, End: transfer.End(), Manifest: manifest, VerifiedTransfer: transfer,
	}, nil
}

func (runtime *Runtime) reconcileSeed(ctx context.Context) error {
	installed, err := runtime.journal.InstallSnapshot(ctx)
	if err != nil {
		return err
	}
	records, err := runtime.journal.EventRecords(ctx)
	if err != nil {
		return err
	}
	if len(records) == 0 && len(runtime.state.EventRecords) > 0 {
		manifest := wipdwire.BlobManifest{
			Schema: "wipd.blob-manifest/1", DomainID: runtime.state.DomainID,
			Epoch: runtime.state.Epoch, AsOf: runtime.state.Prefix,
			Entries: append([]wipdwire.BlobManifestEntry(nil), runtime.state.ManifestEntries...), Digest: runtime.state.ManifestDigest,
		}
		transfer, verifyErr := wipdjournal.VerifyTransfer(runtime.state.DomainID, runtime.state.Epoch,
			emptyPrefixAnchor(), runtime.state.Prefix, runtime.state.EventRecords, manifest)
		if verifyErr != nil {
			return verifyErr
		}
		installed, err = runtime.journal.InstallPull(ctx, installed.Expectation(), transfer)
		if err != nil {
			return err
		}
		records = runtime.state.EventRecords
	}
	if len(records) < len(runtime.state.EventRecords) {
		return wipdseed.ErrInvalidClientState
	}
	for index, seed := range runtime.state.EventRecords {
		if records[index].EventID != seed.EventID || !bytes.Equal(records[index].Record, seed.Record) {
			return wipdseed.ErrInvalidClientState
		}
	}
	if installed.Anchor.EventCount != uint64(len(records)) {
		return wipdseed.ErrInvalidClientState
	}
	return nil
}

// Close releases the authenticated client and Environment journal lease.
func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	var clientErr, journalErr error
	if runtime.client != nil {
		clientErr = runtime.client.Close()
	}
	if runtime.journal != nil {
		journalErr = runtime.journal.Close()
	}
	return errors.Join(clientErr, journalErr)
}

func validateConfig(config Config) error {
	if config.Schema != "wipd.connected-authority-profile/2" || config.RepoID == "" || config.ClientStateDirectory == "" ||
		!filepath.IsAbs(config.ClientStateDirectory) || len(config.OwnerRootPublicKey) != ed25519.PublicKeySize ||
		len(config.ArtifactKeyCertificate) == 0 || len(config.ArtifactKeyCertificate) > 1<<20 {
		return errors.New("wipdremote: invalid connected authority profile")
	}
	ownerDER, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(config.OwnerRootPublicKey))
	ownerDigest := sha256.Sum256(ownerDER)
	if err != nil || "sha256:"+hex.EncodeToString(ownerDigest[:]) != config.OwnerRootSPKI {
		return errors.New("wipdremote: connected authority owner key does not match its pin")
	}
	_, _, err = authorityProfile(config)
	return err
}

func authorityProfile(config Config) (wipdauthority.Profile, *x509.CertPool, error) {
	var empty wipdauthority.Profile
	certificate, err := x509.ParseCertificate(config.AuthorityCertificateDER)
	if err != nil || !bytes.Equal(certificate.Raw, config.AuthorityCertificateDER) {
		return empty, nil, errors.New("wipdremote: invalid pinned authority certificate")
	}
	parsedOrigin, err := url.Parse(config.Origin)
	if err != nil || parsedOrigin.Scheme != "https" || parsedOrigin.Host == "" || parsedOrigin.Path != "" {
		return empty, nil, errors.New("wipdremote: invalid authority origin")
	}
	profile, err := wipdauthority.NewProfile(config.Origin, config.DomainID, config.Epoch, config.AuthoritySPKIPin, config.OwnerRootSPKI)
	if err != nil {
		return empty, nil, err
	}
	profile, err = profile.WithM5LabRepoID(config.RepoID)
	if err != nil {
		return empty, nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return profile, roots, nil
}

func readConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > configSize {
		return nil, errors.New("wipdremote: connected profile must be a private regular file")
	}
	return os.ReadFile(path)
}

func emptyPrefixAnchor() wipdwire.PrefixAnchor {
	digest := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return wipdwire.PrefixAnchor{Digest: "sha256:" + hex.EncodeToString(digest[:])}
}
