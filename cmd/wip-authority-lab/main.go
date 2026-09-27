// Command wip-authority-lab contains the explicit host harness and
// authority-env bootstrap worker for the disposable M5 Compose lab.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
)

const (
	authorityDataRoot  = "/var/lib/online-authority-proof/authority"
	authorityStateRoot = "/var/lib/online-authority-proof"
	grantInputLimit    = 4096
	workerTimeout      = 30 * time.Second
	workerBuildTimeout = 2 * time.Minute
	ulidAlphabet       = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var labProjectPattern = regexp.MustCompile(`^wip-authority-proof-[a-z0-9][a-z0-9_-]{0,31}$`)

type highWaterRecord struct {
	EventCount   uint64  `json:"event_count"`
	EventID      *string `json:"high_water_event_id"`
	PrefixDigest string  `json:"prefix_digest"`
}

type bootstrapRecord struct {
	DomainID  string          `json:"domain_id"`
	Epoch     uint64          `json:"epoch"`
	RepoID    string          `json:"repo_id"`
	HighWater highWaterRecord `json:"high_water"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "wip-authority-lab: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected bootstrap or bootstrap-worker")
	}
	switch args[0] {
	case "bootstrap":
		return runHostBootstrap(args[1:])
	case "bootstrap-worker":
		return runBootstrapWorker(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runHostBootstrap(args []string) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	containerID := flags.String("container-id", "", "running authority-env container ID")
	project := flags.String("project", "", "validated Compose project name")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *containerID == "" || !labProjectPattern.MatchString(*project) {
		return errors.New("usage: bootstrap --container-id <id> --project wip-authority-proof-<id>")
	}
	if err := verifyAuthorityContainer(*containerID, *project); err != nil {
		return err
	}
	goarch, err := authorityContainerGOARCH(*containerID)
	if err != nil {
		return err
	}
	workerExecutable, err := buildBootstrapWorker(goarch)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(workerExecutable)) }()
	workerNameBytes := make([]byte, 8)
	if _, err := rand.Read(workerNameBytes); err != nil {
		return err
	}
	workerPath := authorityStateRoot + "/.m5-bootstrap-" + hex.EncodeToString(workerNameBytes)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "exec", *containerID, "rm", "-f", workerPath).Run()
	}()
	if err := runDocker(workerTimeout, "cp", workerExecutable, *containerID+":"+workerPath); err != nil {
		return fmt.Errorf("copy bootstrap worker into authority-env: %w", err)
	}

	domainID, err := newULID(time.Now())
	if err != nil {
		return err
	}
	repoID, err := newULID(time.Now())
	if err != nil {
		return err
	}
	for repoID == domainID {
		repoID, err = newULID(time.Now())
		if err != nil {
			return err
		}
	}
	ownerPublicKey, ownerPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	defer clear(ownerPrivateKey)
	setupPublicKey, setupPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	defer clear(setupPrivateKey)
	// The authority receives this out-of-band public-key pin separately from
	// the signed grant bytes; it is fixed before the worker reads that input.
	pinnedSetupSigner := append(ed25519.PublicKey(nil), setupPublicKey...)
	domain, err := makeDomain(domainID, ownerPublicKey)
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	issuedAt := time.Now().UTC()
	grant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivateKey, domain, repoID, nonce, issuedAt, issuedAt.Add(authoritystore.M5LabGenesisGrantMaxLifetime))
	clear(nonce)
	if err != nil {
		return err
	}
	defer clear(grant)

	args = []string{
		"exec", "-i", *containerID, workerPath, "bootstrap-worker",
		"--domain-id", domain.ID,
		"--repo-id", repoID,
		"--owner-root-public-key", base64.StdEncoding.EncodeToString(domain.OwnerPublicKey),
		"--setup-signer-pin", base64.StdEncoding.EncodeToString(pinnedSetupSigner),
	}
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin = bytes.NewReader(grant)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err = command.Run(); err != nil {
		return fmt.Errorf("authority-env bootstrap refused: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var record bootstrapRecord
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record); err != nil {
		return fmt.Errorf("authority-env returned invalid bootstrap record: %w", err)
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("authority-env returned trailing bootstrap data")
	}
	if record.DomainID != domain.ID || record.Epoch != 1 || record.RepoID != repoID ||
		record.HighWater.EventCount != 0 || record.HighWater.EventID != nil || record.HighWater.PrefixDigest != emptyPrefixDigest() {
		return errors.New("authority-env bootstrap identity or initial high-water differs from the authorized values")
	}
	return json.NewEncoder(os.Stdout).Encode(record)
}

func verifyAuthorityContainer(containerID, project string) error {
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	defer cancel()
	format := `{{.State.Running}}|{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.procrastivity.wip.authority-proof"}}`
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", format, containerID).Output()
	if err != nil {
		return fmt.Errorf("inspect authority-env container: %w", err)
	}
	want := "true|authority-env|" + project + "|lab-v1"
	if strings.TrimSpace(string(output)) != want {
		return errors.New("refusing bootstrap outside the running, owned authority-env service")
	}
	return nil
}

func runDocker(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func authorityContainerGOARCH(containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	defer cancel()
	image, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Image}}", containerID).Output()
	if err != nil {
		return "", fmt.Errorf("inspect authority-env image: %w", err)
	}
	platform, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Os}}|{{.Architecture}}", strings.TrimSpace(string(image))).Output()
	if err != nil {
		return "", fmt.Errorf("inspect authority-env platform: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(platform)), "|")
	if len(parts) != 2 {
		return "", errors.New("authority-env bootstrap worker requires a Linux container")
	}
	return linuxGOARCH(parts[0], parts[1])
}

func linuxGOARCH(goos, goarch string) (string, error) {
	if goos != "linux" {
		return "", errors.New("authority-env bootstrap worker requires a Linux container")
	}
	switch goarch {
	case "386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x":
		return goarch, nil
	default:
		return "", fmt.Errorf("unsupported authority-env architecture %q", goarch)
	}
}

func buildBootstrapWorker(goarch string) (string, error) {
	directory, err := os.MkdirTemp("", "wip-authority-lab-worker-")
	if err != nil {
		return "", err
	}
	executable := filepath.Join(directory, "bootstrap-worker")
	ctx, cancel := context.WithTimeout(context.Background(), workerBuildTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", executable, "./cmd/wip-authority-lab")
	command.Env = withEnvironment(os.Environ(), map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      goarch,
		"GOOS":        "linux",
	})
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("build static Linux authority-env worker: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return executable, nil
}

func withEnvironment(environment []string, overrides map[string]string) []string {
	result := make([]string, 0, len(environment)+len(overrides))
	for _, value := range environment {
		key, _, found := strings.Cut(value, "=")
		if _, replace := overrides[key]; !found || !replace {
			result = append(result, value)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func runBootstrapWorker(args []string) error {
	defer func() { _ = os.Remove(os.Args[0]) }()
	flags := flag.NewFlagSet("bootstrap-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	domainID := flags.String("domain-id", "", "authorized fresh domain ID")
	repoID := flags.String("repo-id", "", "authorized initial Repo ID")
	ownerRootPublicKey := flags.String("owner-root-public-key", "", "owner-root public key")
	setupSignerPin := flags.String("setup-signer-pin", "", "out-of-band pinned setup signer public key")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *domainID == "" || *repoID == "" || *ownerRootPublicKey == "" || *setupSignerPin == "" {
		return errors.New("incomplete authority-env bootstrap input")
	}
	ownerPublicKey, err := decodePublicKey(*ownerRootPublicKey)
	if err != nil {
		return errors.New("invalid owner-root public key")
	}
	pinnedSetupSigner, err := decodePublicKey(*setupSignerPin)
	if err != nil {
		return errors.New("invalid setup signer pin")
	}
	domain, err := makeDomain(*domainID, ownerPublicKey)
	if err != nil {
		return errors.New("invalid authorized domain identity")
	}
	// Pin parsing and domain binding inputs are established before the
	// worker accepts or verifies any grant bytes from stdin.
	grant, err := io.ReadAll(io.LimitReader(os.Stdin, grantInputLimit+1))
	if err != nil || len(grant) == 0 || len(grant) > grantInputLimit {
		return errors.New("invalid bounded grant input")
	}
	defer clear(grant)

	record, err := bootstrapFreshDomain(context.Background(), authorityDataRoot, domain, *repoID, pinnedSetupSigner, grant, time.Now().UTC())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(record)
}

func bootstrapFreshDomain(ctx context.Context, root string, domain authoritystore.Domain, repoID string, pinnedSetupSigner ed25519.PublicKey, grant []byte, now time.Time) (bootstrapRecord, error) {
	if err := authoritystore.VerifyM5LabGenesisGrant(grant, pinnedSetupSigner, domain, repoID, now); err != nil {
		return bootstrapRecord{}, err
	}
	store, err := authoritystore.CreateEmpty(root)
	if err != nil {
		return bootstrapRecord{}, err
	}
	defer func() { _ = store.Close() }()
	if err = store.BootstrapDomainWithM5LabGrant(ctx, domain, repoID, pinnedSetupSigner, grant, now); err != nil {
		return bootstrapRecord{}, err
	}
	storedDomain, err := store.LookupDomain(ctx, domain.ID)
	if err != nil {
		return bootstrapRecord{}, err
	}
	if storedDomain.ID != domain.ID || storedDomain.ActiveEpoch != 1 || storedDomain.OwnerKeyID != domain.OwnerKeyID {
		return bootstrapRecord{}, errors.New("persisted domain identity differs from the authorized values")
	}
	storedRepoDomain, err := store.RepoDomain(ctx, repoID)
	if err != nil || storedRepoDomain != domain.ID {
		return bootstrapRecord{}, errors.New("persisted initial Repo does not resolve to the authorized domain")
	}
	anchor, err := store.CurrentPrefixAnchor(ctx, domain.ID)
	if err != nil {
		return bootstrapRecord{}, err
	}
	if anchor.EventCount != 0 || anchor.EventID != "" || anchor.Digest != emptyPrefixDigest() {
		return bootstrapRecord{}, errors.New("fresh authority domain does not have the normative empty high-water")
	}
	return bootstrapRecord{
		DomainID: storedDomain.ID,
		Epoch:    storedDomain.ActiveEpoch,
		RepoID:   repoID,
		HighWater: highWaterRecord{
			EventCount:   anchor.EventCount,
			PrefixDigest: anchor.Digest,
		},
	}, nil
}

func makeDomain(domainID string, ownerPublicKey ed25519.PublicKey) (authoritystore.Domain, error) {
	if len(ownerPublicKey) != ed25519.PublicKeySize {
		return authoritystore.Domain{}, errors.New("invalid Ed25519 owner public key")
	}
	der, err := x509.MarshalPKIXPublicKey(ownerPublicKey)
	if err != nil {
		return authoritystore.Domain{}, err
	}
	digest := sha256.Sum256(der)
	return authoritystore.Domain{
		ID:             domainID,
		OwnerPublicKey: append(ed25519.PublicKey(nil), ownerPublicKey...),
		OwnerKeyID:     "sha256:" + hex.EncodeToString(digest[:]),
		ActiveEpoch:    1,
	}, nil
}

func decodePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid canonical base64 Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func emptyPrefixDigest() string {
	digest := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return fmt.Sprintf("sha256:%x", digest)
}

func newULID(now time.Time) (string, error) {
	milliseconds := now.UTC().UnixMilli()
	if milliseconds < 0 || uint64(milliseconds) >= 1<<48 {
		return "", errors.New("current time cannot be represented in a ULID")
	}
	var raw [16]byte
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(milliseconds))
	copy(raw[:6], timestamp[2:])
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	return encodeULID(raw), nil
}

func encodeULID(raw [16]byte) string {
	out := make([]byte, 26)
	out[0] = ulidAlphabet[(raw[0]&0xe0)>>5]
	bitPosition := uint(3)
	for i := 1; i < len(out); i++ {
		var value byte
		for range 5 {
			byteIndex := bitPosition >> 3
			bitIndex := 7 - (bitPosition & 7)
			value = value<<1 | (raw[byteIndex]>>bitIndex)&1
			bitPosition++
		}
		out[i] = ulidAlphabet[value]
	}
	return string(out)
}
