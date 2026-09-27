package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdseed"
)

const (
	clientDataRoot          = "/var/lib/online-authority-proof/client"
	clientWorkerTimeout     = 45 * time.Second
	serveWorkerTimeout      = 5 * time.Minute
	maxServeWorkerInputSize = 1 << 20
)

var (
	labULIDPattern  = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	stopFilePattern = regexp.MustCompile(`^/var/lib/online-authority-proof/\.m5-enroll-stop-[a-f0-9]{16}$`)
)

type clientPrepareRecord struct {
	CSRDER []byte `json:"csr_der"`
}

type enrollmentResult struct {
	Schema        string `json:"schema"`
	DomainID      string `json:"domain_id"`
	Epoch         uint64 `json:"authority_epoch"`
	RepoID        string `json:"repo_id"`
	EnvironmentID string `json:"environment_id"`
	OwnerKeyID    string `json:"owner_key_id"`
	SPKIDigest    string `json:"spki_digest"`
	PrefixDigest  string `json:"prefix_digest"`
	Manifest      string `json:"manifest_digest"`
}

type labAuthorityInput struct {
	Schema                      string `json:"schema"`
	Origin                      string `json:"origin"`
	DomainID                    string `json:"domain_id"`
	Epoch                       uint64 `json:"authority_epoch"`
	RepoID                      string `json:"repo_id"`
	OwnerRootPublicKey          []byte `json:"owner_root_public_key"`
	AuthoritySPKIPin            string `json:"authority_spki_pin"`
	EnrollmentGrant             []byte `json:"enrollment_grant"`
	ExpectedCSRDER              []byte `json:"expected_csr_der"`
	EnvironmentCACertificateDER []byte `json:"environment_ca_certificate_der"`
	EnvironmentCAPrivateKeyDER  []byte `json:"environment_ca_private_key_pkcs8"`
	EnvironmentCADelegation     []byte `json:"environment_ca_delegation"`
	AuthorityCertificateDER     []byte `json:"authority_certificate_der"`
	AuthorityPrivateKeyDER      []byte `json:"authority_private_key_pkcs8"`
	StopFile                    string `json:"stop_file"`
}

type labClientInput struct {
	Schema                  string `json:"schema"`
	Origin                  string `json:"origin"`
	DomainID                string `json:"domain_id"`
	Epoch                   uint64 `json:"authority_epoch"`
	RepoID                  string `json:"repo_id"`
	OwnerRootPublicKey      []byte `json:"owner_root_public_key"`
	OwnerRootSPKI           string `json:"owner_root_spki"`
	AuthoritySPKIPin        string `json:"authority_spki_pin"`
	AuthorityCertificateDER []byte `json:"authority_certificate_der"`
	EnvironmentCADelegation []byte `json:"environment_ca_delegation"`
	EnrollmentGrant         []byte `json:"enrollment_grant"`
}

func runStep4Command(args []string) error {
	if len(args) == 0 {
		return errors.New("missing Step 4 worker command")
	}
	switch args[0] {
	case "prepare-client-worker":
		return runPrepareClientWorker(args[1:])
	case "client-csr-worker":
		return runClientCSRWorker(args[1:])
	case "client-enroll-worker":
		return runClientEnrollWorker(args[1:])
	case "authority-serve-worker":
		return runAuthorityServeWorker(args[1:])
	case "prepare-client":
		return runHostPrepareClient(args[1:])
	case "enroll":
		return runHostEnroll(args[1:])
	default:
		return fmt.Errorf("unknown Step 4 command %q", args[0])
	}
}

func runHostPrepareClient(args []string) error {
	flags := flag.NewFlagSet("prepare-client", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	containerID := flags.String("container-id", "", "running client-env container ID")
	project := flags.String("project", "", "validated Compose project name")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *containerID == "" || !labProjectPattern.MatchString(*project) {
		return errors.New("usage: prepare-client --container-id <id> --project wip-authority-proof-<id>")
	}
	if err := verifyOwnedContainer(*containerID, *project, "client-env"); err != nil {
		return err
	}
	goarch, err := containerGOARCH(*containerID)
	if err != nil {
		return err
	}
	worker, err := buildLabWorker(goarch)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(worker)) }()
	workerPath, err := copyWorker(*containerID, worker)
	if err != nil {
		return err
	}
	defer removeWorker(*containerID, workerPath)
	output, err := runContainerWorker(clientWorkerTimeout, *containerID, workerPath, "prepare-client-worker", nil)
	if err != nil {
		return fmt.Errorf("prepare client Environment failed: %w", err)
	}
	var record clientPrepareRecord
	if err := decodeStrictJSON(output, &record); err != nil || len(record.CSRDER) == 0 {
		return errors.New("client worker returned an invalid CSR record")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"csr_der_base64": base64.StdEncoding.EncodeToString(record.CSRDER)})
}

func runHostEnroll(args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	project := flags.String("project", "", "validated Compose project name")
	authorityContainer := flags.String("authority-container-id", "", "running authority-env container ID")
	clientContainer := flags.String("client-container-id", "", "running client-env container ID")
	domainID := flags.String("domain-id", "", "domain ID from the bootstrap record")
	repoID := flags.String("repo-id", "", "Repo ID from the bootstrap record")
	epoch := flags.Uint64("epoch", 0, "authority epoch from the bootstrap record")
	ownerRootValue := flags.String("owner-root-public-key", "", "retained offline owner-root Ed25519 public key in base64")
	caCertificatePath := flags.String("environment-ca-certificate", "", "offline-delegated Environment CA certificate DER or PEM")
	caPrivateKeyPath := flags.String("environment-ca-private-key", "", "restricted issuer Ed25519 PKCS#8 private key DER or PEM")
	caDelegationPath := flags.String("environment-ca-delegation", "", "offline owner-signed CA delegation artifact")
	enrollmentGrantPath := flags.String("enrollment-grant", "", "offline owner-signed one-use enrollment grant")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !labProjectPattern.MatchString(*project) || *authorityContainer == "" || *clientContainer == "" ||
		!labULIDPattern.MatchString(*domainID) || !labULIDPattern.MatchString(*repoID) || *epoch == 0 || *ownerRootValue == "" || *caCertificatePath == "" ||
		*caPrivateKeyPath == "" || *caDelegationPath == "" || *enrollmentGrantPath == "" {
		return errors.New("usage: enroll --project <project> --authority-container-id <id> --client-container-id <id> --domain-id <ulid> --repo-id <ulid> --epoch <n> --owner-root-public-key <base64> --environment-ca-certificate <path> --environment-ca-private-key <path> --environment-ca-delegation <path> --enrollment-grant <path>")
	}
	if err := verifyOwnedContainer(*authorityContainer, *project, "authority-env"); err != nil {
		return err
	}
	if err := verifyOwnedContainer(*clientContainer, *project, "client-env"); err != nil {
		return err
	}
	ownerPublic, err := decodePublicKey(*ownerRootValue)
	if err != nil {
		return errors.New("invalid offline owner-root public key")
	}
	domain, err := makeDomain(*domainID, ownerPublic)
	if err != nil {
		return err
	}
	caDER, err := readCertificateFile(*caCertificatePath)
	if err != nil {
		return fmt.Errorf("read Environment CA certificate: %w", err)
	}
	caKeyDER, err := readPrivateKeyFile(*caPrivateKeyPath)
	if err != nil {
		return fmt.Errorf("read restricted Environment issuer key: %w", err)
	}
	defer clear(caKeyDER)
	caPrivate, err := parseEd25519PrivateKey(caKeyDER)
	if err != nil {
		return errors.New("environment CA issuer key must be PKCS#8 Ed25519")
	}
	defer clear(caPrivate)
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		return errors.New("invalid Environment CA certificate")
	}
	caPublicDER, err := x509.MarshalPKIXPublicKey(caPrivate.Public())
	if err != nil || !bytes.Equal(caPublicDER, caCertificate.RawSubjectPublicKeyInfo) {
		return errors.New("environment CA private key does not match the delegated certificate")
	}
	delegation, err := readLimitedFile(*caDelegationPath, 1<<20)
	if err != nil {
		return fmt.Errorf("read owner-signed Environment CA delegation: %w", err)
	}
	grant, err := readLimitedFile(*enrollmentGrantPath, 4096)
	if err != nil {
		return fmt.Errorf("read owner-signed enrollment grant: %w", err)
	}
	ownerSPKI := domain.OwnerKeyID
	clientCSR, err := fetchClientCSR(*clientContainer, *project)
	if err != nil {
		return err
	}
	serverCertDER, serverPrivate, err := makeAuthorityServerCertificate(*domainID, *epoch, ownerSPKI, time.Now().UTC())
	if err != nil {
		return err
	}
	defer clear(serverPrivate)
	serverPrivateDER, err := x509.MarshalPKCS8PrivateKey(serverPrivate)
	if err != nil {
		return err
	}
	defer clear(serverPrivateDER)
	serverSPKI, err := x509.MarshalPKIXPublicKey(serverPrivate.Public())
	if err != nil {
		return err
	}
	serverPinBytes := sha256.Sum256(serverSPKI)
	serverPin := "sha256:" + hex.EncodeToString(serverPinBytes[:])
	stopSuffix := make([]byte, 8)
	if _, err = rand.Read(stopSuffix); err != nil {
		return err
	}
	stopFile := authorityStateRoot + "/.m5-enroll-stop-" + hex.EncodeToString(stopSuffix)
	serveConfig := labAuthorityInput{
		Schema: "wipd.m5-lab-authority-worker/1", Origin: "https://authority-env:8443",
		DomainID: *domainID, Epoch: *epoch, RepoID: *repoID, OwnerRootPublicKey: append([]byte(nil), ownerPublic...),
		AuthoritySPKIPin: serverPin, EnrollmentGrant: grant, ExpectedCSRDER: clientCSR,
		EnvironmentCACertificateDER: caDER, EnvironmentCAPrivateKeyDER: caKeyDER, EnvironmentCADelegation: delegation,
		AuthorityCertificateDER: serverCertDER, AuthorityPrivateKeyDER: serverPrivateDER, StopFile: stopFile,
	}
	defer clear(serveConfig.EnvironmentCAPrivateKeyDER)
	defer clear(serveConfig.AuthorityPrivateKeyDER)
	defer clear(serveConfig.EnrollmentGrant)
	clientConfig := labClientInput{
		Schema: "wipd.m5-lab-client-worker/1", Origin: "https://authority-env:8443", DomainID: *domainID,
		Epoch: *epoch, RepoID: *repoID, OwnerRootPublicKey: append([]byte(nil), ownerPublic...),
		OwnerRootSPKI: ownerSPKI, AuthoritySPKIPin: serverPin, AuthorityCertificateDER: serverCertDER,
		EnvironmentCADelegation: delegation, EnrollmentGrant: grant,
	}
	defer clear(clientConfig.EnrollmentGrant)
	defer clear(clientConfig.OwnerRootPublicKey)
	serveWorker, err := buildWorkerForContainer(*authorityContainer)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(serveWorker)) }()
	servePath, err := copyWorker(*authorityContainer, serveWorker)
	if err != nil {
		return err
	}
	defer removeWorker(*authorityContainer, servePath)
	clientWorker, err := buildWorkerForContainer(*clientContainer)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(clientWorker)) }()
	clientPath, err := copyWorker(*clientContainer, clientWorker)
	if err != nil {
		return err
	}
	defer removeWorker(*clientContainer, clientPath)
	serverProcess, err := startAuthorityWorker(*authorityContainer, servePath, serveConfig)
	if err != nil {
		return err
	}
	clientOutput, clientErr := runJSONContainerWorker(*clientContainer, clientPath, "client-enroll-worker", clientConfig)
	stopErr := runDocker(workerTimeout, "exec", *authorityContainer, "touch", stopFile)
	serveErr := waitAuthorityWorker(serverProcess)
	if clientErr != nil {
		return fmt.Errorf("client enrollment outcome-unknown domain_id=%s repo_id=%s: %w", *domainID, *repoID, clientErr)
	}
	if stopErr != nil || serveErr != nil {
		return fmt.Errorf("client enrollment installed but authority-worker shutdown was not confirmed domain_id=%s repo_id=%s: %v %v", *domainID, *repoID, stopErr, serveErr)
	}
	var result enrollmentResult
	if err := decodeStrictJSON(clientOutput, &result); err != nil || result.Schema != "wipd.m5-client-state/1" || result.DomainID != *domainID || result.Epoch != *epoch || result.RepoID != *repoID || result.PrefixDigest != emptyPrefixDigest() || result.Manifest != emptyManifestDigest() {
		return fmt.Errorf("client enrollment outcome-unknown domain_id=%s repo_id=%s: invalid result record", *domainID, *repoID)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func runPrepareClientWorker(args []string) error {
	defer removeSelf()
	if len(args) != 0 {
		return errors.New("prepare-client-worker does not accept arguments")
	}
	if _, err := os.Stat(filepath.Join(clientDataRoot, "client-state.json")); err == nil {
		return wipdseed.ErrStateExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	identity, err := wipdseed.PrepareIdentity()
	if err != nil {
		return err
	}
	defer clear(identity.PrivateKeyPKCS8)
	if err = wipdseed.SavePending(clientDataRoot, identity); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(clientPrepareRecord{CSRDER: identity.CSRDER})
}

func runClientCSRWorker(args []string) error {
	defer removeSelf()
	if len(args) != 0 {
		return errors.New("client-csr-worker does not accept arguments")
	}
	identity, err := wipdseed.LoadPending(clientDataRoot)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(clientPrepareRecord{CSRDER: identity.CSRDER})
}

func runClientEnrollWorker(args []string) error {
	defer removeSelf()
	if len(args) != 0 {
		return errors.New("client-enroll-worker does not accept arguments")
	}
	var input labClientInput
	if err := decodeLimitedStdin(maxServeWorkerInputSize, &input); err != nil {
		return err
	}
	defer clear(input.EnrollmentGrant)
	defer clear(input.EnvironmentCADelegation)
	defer clear(input.OwnerRootPublicKey)
	if input.Schema != "wipd.m5-lab-client-worker/1" || !labULIDPattern.MatchString(input.DomainID) || !labULIDPattern.MatchString(input.RepoID) || input.Epoch == 0 {
		return errors.New("invalid client enrollment worker input")
	}
	certificate, err := x509.ParseCertificate(input.AuthorityCertificateDER)
	if err != nil {
		return errors.New("invalid pinned authority certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	profile, err := wipdauthority.NewProfile(input.Origin, input.DomainID, input.Epoch, input.AuthoritySPKIPin, input.OwnerRootSPKI)
	if err != nil {
		return err
	}
	identity, err := wipdseed.LoadPending(clientDataRoot)
	if err != nil {
		return err
	}
	defer clear(identity.PrivateKeyPKCS8)
	state, err := wipdseed.EnrollAndSeed(context.Background(), profile, roots, ed25519.PublicKey(input.OwnerRootPublicKey), input.EnvironmentCADelegation, input.RepoID, identity, input.EnrollmentGrant, clientDataRoot)
	if err != nil {
		return err
	}
	defer clear(state.PrivateKeyPKCS8)
	return json.NewEncoder(os.Stdout).Encode(enrollmentResult{
		Schema: state.Schema, DomainID: state.DomainID, Epoch: state.Epoch, RepoID: state.RepoID,
		EnvironmentID: state.EnvironmentID, OwnerKeyID: state.OwnerKeyID, SPKIDigest: state.SPKIDigest,
		PrefixDigest: state.Prefix.Digest, Manifest: state.ManifestDigest,
	})
}

func runAuthorityServeWorker(args []string) error {
	defer removeSelf()
	if len(args) != 0 {
		return errors.New("authority-serve-worker does not accept arguments")
	}
	var input labAuthorityInput
	if err := decodeLimitedStdin(maxServeWorkerInputSize, &input); err != nil {
		return err
	}
	defer clear(input.EnvironmentCAPrivateKeyDER)
	defer clear(input.AuthorityPrivateKeyDER)
	defer clear(input.EnrollmentGrant)
	if input.Schema != "wipd.m5-lab-authority-worker/1" || !labULIDPattern.MatchString(input.DomainID) || !labULIDPattern.MatchString(input.RepoID) || input.Epoch == 0 || !stopFilePattern.MatchString(input.StopFile) {
		return errors.New("invalid authority enrollment worker input")
	}
	caPrivate, err := parseEd25519PrivateKey(input.EnvironmentCAPrivateKeyDER)
	if err != nil {
		return errors.New("invalid Environment CA issuer key")
	}
	defer clear(caPrivate)
	authorityPrivate, err := parseEd25519PrivateKey(input.AuthorityPrivateKeyDER)
	if err != nil {
		return errors.New("invalid authority TLS key")
	}
	defer clear(authorityPrivate)
	caCertificate, err := x509.ParseCertificate(input.EnvironmentCACertificateDER)
	if err != nil {
		return errors.New("invalid Environment CA certificate")
	}
	caSPKI, err := x509.MarshalPKIXPublicKey(caPrivate.Public())
	if err != nil || !bytes.Equal(caSPKI, caCertificate.RawSubjectPublicKeyInfo) {
		return errors.New("environment CA issuer key does not match its certificate")
	}
	authorityCertificate, err := x509.ParseCertificate(input.AuthorityCertificateDER)
	if err != nil {
		return errors.New("invalid authority TLS certificate")
	}
	authoritySPKI, err := x509.MarshalPKIXPublicKey(authorityPrivate.Public())
	if err != nil || !bytes.Equal(authoritySPKI, authorityCertificate.RawSubjectPublicKeyInfo) {
		return errors.New("authority TLS key does not match its certificate")
	}
	ownerSPKI, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(input.OwnerRootPublicKey))
	if err != nil {
		return err
	}
	ownerDigest := sha256.Sum256(ownerSPKI)
	ownerKeyID := "sha256:" + hex.EncodeToString(ownerDigest[:])
	profile, err := wipdauthority.NewProfile(input.Origin, input.DomainID, input.Epoch, input.AuthoritySPKIPin, ownerKeyID)
	if err != nil {
		return err
	}
	store, err := authoritystore.OpenExisting(authorityDataRoot)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	server, err := wipdauthority.NewM5LabServer(profile, tlsCertificateDER(input.AuthorityCertificateDER, authorityPrivate), wipdauthority.M5LabConfig{
		Store: store, RepoID: input.RepoID, EnrollmentGrant: input.EnrollmentGrant,
		ExpectedCSRDER: input.ExpectedCSRDER, EnvironmentCACertificateDER: input.EnvironmentCACertificateDER,
		SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			return signEnvironmentLeaf(caPrivate, caCertificate, domain, epoch, environment, ownerKeyID, csrDER, at)
		},
	})
	if err != nil {
		return err
	}
	if err = store.InstallEnvironmentCA(context.Background(), input.DomainID, input.EnvironmentCADelegation, time.Now().UTC()); err != nil {
		return fmt.Errorf("install owner-signed Environment CA delegation: %w", err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:8443")
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), serveWorkerTimeout)
	defer cancel()
	go watchStopFile(ctx, cancel, input.StopFile)
	if _, err = fmt.Fprintln(os.Stdout, "ready"); err != nil {
		return err
	}
	return server.Serve(ctx, listener)
}

func watchStopFile(ctx context.Context, cancel context.CancelFunc, path string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Lstat(path); err == nil {
				_ = os.Remove(path)
				cancel()
				return
			}
		}
	}
}

type authorityWorkerProcess struct {
	command    *exec.Cmd
	stderr     *bytes.Buffer
	stdoutDone chan struct{}
	cancel     context.CancelFunc
}

func startAuthorityWorker(containerID, workerPath string, input labAuthorityInput) (*authorityWorkerProcess, error) {
	ctx, cancel := context.WithTimeout(context.Background(), serveWorkerTimeout)
	command := exec.CommandContext(ctx, "docker", "exec", "-i", containerID, workerPath, "authority-serve-worker")
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = command.Start(); err != nil {
		cancel()
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err == nil {
		_, err = io.Copy(stdin, bytes.NewReader(encoded))
	}
	clear(encoded)
	if closeErr := stdin.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cancel()
		_ = command.Wait()
		return nil, err
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		cancel()
		_ = command.Wait()
		return nil, fmt.Errorf("authority enrollment worker failed before readiness: %s %s", strings.TrimSpace(line), stderr.String())
	}
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		_, _ = io.Copy(io.Discard, reader)
	}()
	return &authorityWorkerProcess{command: command, stderr: stderr, stdoutDone: stdoutDone, cancel: cancel}, nil
}

func waitAuthorityWorker(process *authorityWorkerProcess) error {
	if process == nil || process.command == nil {
		return errors.New("authority worker was not started")
	}
	defer process.cancel()
	<-process.stdoutDone
	if err := process.command.Wait(); err != nil {
		return fmt.Errorf("authority enrollment worker: %w: %s", err, strings.TrimSpace(process.stderr.String()))
	}
	return nil
}

func runJSONContainerWorker(containerID, workerPath, commandName string, input any) ([]byte, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	return runContainerWorker(clientWorkerTimeout, containerID, workerPath, commandName, encoded)
}

func runContainerWorker(timeout time.Duration, containerID, workerPath, commandName string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "exec", "-i", containerID, workerPath, commandName)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("docker worker failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return bytes.Clone(stdout.Bytes()), nil
}

func fetchClientCSR(clientContainer, project string) ([]byte, error) {
	if err := verifyOwnedContainer(clientContainer, project, "client-env"); err != nil {
		return nil, err
	}
	goarch, err := containerGOARCH(clientContainer)
	if err != nil {
		return nil, err
	}
	worker, err := buildLabWorker(goarch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(worker)) }()
	workerPath, err := copyWorker(clientContainer, worker)
	if err != nil {
		return nil, err
	}
	defer removeWorker(clientContainer, workerPath)
	output, err := runContainerWorker(clientWorkerTimeout, clientContainer, workerPath, "client-csr-worker", nil)
	if err != nil {
		return nil, fmt.Errorf("client Environment has no valid prepared CSR: %w", err)
	}
	var record clientPrepareRecord
	if err = decodeStrictJSON(output, &record); err != nil || len(record.CSRDER) == 0 {
		return nil, errors.New("client worker returned an invalid CSR")
	}
	return record.CSRDER, nil
}

func buildWorkerForContainer(containerID string) (string, error) {
	goarch, err := containerGOARCH(containerID)
	if err != nil {
		return "", err
	}
	return buildLabWorker(goarch)
}

func buildLabWorker(goarch string) (string, error) {
	directory, err := os.MkdirTemp("", "wip-authority-lab-worker-")
	if err != nil {
		return "", err
	}
	executable := filepath.Join(directory, "wip-authority-lab-worker")
	ctx, cancel := context.WithTimeout(context.Background(), workerBuildTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", executable, "./cmd/wip-authority-lab")
	command.Env = withEnvironment(os.Environ(), map[string]string{"CGO_ENABLED": "0", "GOARCH": goarch, "GOOS": "linux"})
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("build static Linux lab worker: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return executable, nil
}

func containerGOARCH(containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	defer cancel()
	image, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Image}}", containerID).Output()
	if err != nil {
		return "", fmt.Errorf("inspect lab container image: %w", err)
	}
	platform, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Os}}|{{.Architecture}}", strings.TrimSpace(string(image))).Output()
	if err != nil {
		return "", fmt.Errorf("inspect lab container platform: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(platform)), "|")
	if len(parts) != 2 {
		return "", errors.New("lab worker requires Linux containers")
	}
	return linuxGOARCH(parts[0], parts[1])
}

func copyWorker(containerID, source string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	path := authorityStateRoot + "/.m5-step4-worker-" + hex.EncodeToString(suffix)
	if err := runDocker(workerTimeout, "cp", source, containerID+":"+path); err != nil {
		return "", fmt.Errorf("copy temporary worker into lab state volume: %w", err)
	}
	return path, nil
}

func removeWorker(containerID, workerPath string) {
	_ = runDocker(workerTimeout, "exec", containerID, "rm", "-f", workerPath)
}

func verifyOwnedContainer(containerID, project, service string) error {
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	defer cancel()
	format := `{{.State.Running}}|{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.procrastivity.wip.authority-proof"}}`
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", format, containerID).Output()
	if err != nil {
		return fmt.Errorf("inspect %s container: %w", service, err)
	}
	want := "true|" + service + "|" + project + "|lab-v1"
	if strings.TrimSpace(string(output)) != want {
		return fmt.Errorf("refusing operation outside the running, owned %s service", service)
	}
	return nil
}

func decodeLimitedStdin(limit int64, target any) error {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
	if err != nil || int64(len(data)) > limit {
		clear(data)
		return errors.New("invalid bounded worker input")
	}
	defer clear(data)
	return decodeStrictJSON(data, target)
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("JSON input contains trailing data")
	}
	return nil
}

func parseEd25519PrivateKey(der []byte) (ed25519.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("expected Ed25519 PKCS#8 private key")
	}
	return privateKey, nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		clear(data)
		return nil, errors.New("file exceeds the permitted size")
	}
	return data, nil
}

func readCertificateFile(path string) ([]byte, error) {
	data, err := readLimitedFile(path, 64<<10)
	if err != nil {
		return nil, err
	}
	if block, rest := pem.Decode(data); block != nil && len(bytes.TrimSpace(rest)) == 0 && block.Type == "CERTIFICATE" {
		der := bytes.Clone(block.Bytes)
		clear(data)
		return der, nil
	}
	if _, err = x509.ParseCertificate(data); err != nil {
		clear(data)
		return nil, errors.New("file is neither one DER nor one PEM certificate")
	}
	return data, nil
}

func readPrivateKeyFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("environment CA private key file must be regular and mode 0600 or stricter")
	}
	data, err := readLimitedFile(path, 64<<10)
	if err != nil {
		return nil, err
	}
	if block, rest := pem.Decode(data); block != nil && len(bytes.TrimSpace(rest)) == 0 && block.Type == "PRIVATE KEY" {
		der := bytes.Clone(block.Bytes)
		clear(data)
		return der, nil
	}
	if _, err = x509.ParsePKCS8PrivateKey(data); err != nil {
		clear(data)
		return nil, errors.New("file is neither PKCS#8 DER nor PEM")
	}
	return data, nil
}

func makeAuthorityServerCertificate(domain string, epoch uint64, owner string, now time.Time) ([]byte, ed25519.PrivateKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	ownerHex := strings.TrimPrefix(owner, "sha256:")
	uri := fmt.Sprintf("wipd://authority/%s?epoch=%d&owner=%s", domain, epoch, ownerHex)
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte("authority-env")},
		{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)},
	})
	if err != nil {
		clear(private)
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: new(big.Int).SetInt64(now.UnixNano()), Subject: pkix.Name{CommonName: "M5 disposable authority"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(serveWorkerTimeout + 5*time.Minute),
		BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0, MaxPathLenZero: true,
		KeyUsage:        x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:        []string{"authority-env"},
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Critical: true, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		clear(private)
		return nil, nil, err
	}
	return der, private, nil
}

func signEnvironmentLeaf(caPrivate ed25519.PrivateKey, ca *x509.Certificate, domain string, epoch uint64, environment, owner string, csrDER []byte, now time.Time) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(csr.Raw, csrDER) {
		return nil, errors.New("invalid canonical CSR")
	}
	publicKey, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok || bytes.Equal(publicKey, caPrivate.Public().(ed25519.PublicKey)) {
		return nil, errors.New("CSR key is not an independent Ed25519 Environment key")
	}
	uri := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", environment, domain, epoch, strings.TrimPrefix(owner, "sha256:"))
	san, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)}})
	if err != nil {
		return nil, err
	}
	serialBytes := make([]byte, 16)
	if _, err = rand.Read(serialBytes); err != nil {
		return nil, err
	}
	serial := new(big.Int).SetBytes(serialBytes)
	clear(serialBytes)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	start := now.UTC().Add(-time.Minute)
	if start.Before(ca.NotBefore) {
		start = ca.NotBefore
	}
	end := now.UTC().Add(time.Hour)
	if end.After(ca.NotAfter) {
		end = ca.NotAfter
	}
	if !start.Before(end) {
		return nil, errors.New("environment CA is outside its delegated validity window")
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "wipd Environment"},
		NotBefore: start, NotAfter: end, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 15}, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Critical: true, Value: san},
		},
	}
	return x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, caPrivate)
}

func emptyManifestDigest() string {
	digest := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	return fmt.Sprintf("sha256:%x", digest)
}

func tlsCertificateDER(certificateDER []byte, private ed25519.PrivateKey) tls.Certificate {
	return tls.Certificate{Certificate: [][]byte{bytes.Clone(certificateDER)}, PrivateKey: private}
}

func removeSelf() {
	_ = os.Remove(os.Args[0])
}
