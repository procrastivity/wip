package schedulerhook

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	verifierEvidenceSchema = "wipd.step10-verification-evidence/5"
	verifierTailBytes      = 16 << 10
	verifierMaxTreeBytes   = 512 << 20
	verifierScratchDir     = "/tmp"
	verifierScratchPrefix  = "wipd-step10-verifier-"
	verifierHostsFile      = "127.0.0.1 localhost\n::1 localhost ip6-localhost\n"
)

type verifierEvidenceRef struct {
	Schema                  string                 `json:"schema"`
	CheckSet                string                 `json:"check_set"`
	DomainID                string                 `json:"domain_id"`
	EnvironmentID           string                 `json:"environment_id"`
	RunID                   string                 `json:"run_id"`
	MatterID                string                 `json:"matter_id"`
	TargetID                string                 `json:"target_id"`
	VerifierRoleID          string                 `json:"verifier_role_id"`
	ClaimID                 string                 `json:"claim_id"`
	ClaimEpoch              uint64                 `json:"claim_epoch"`
	DispatchID              string                 `json:"dispatch_id"`
	OriginalCommandID       string                 `json:"original_command_id"`
	RequestHash             string                 `json:"request_hash"`
	InputDigest             string                 `json:"input_digest"`
	CandidateRevision       string                 `json:"candidate_revision"`
	CandidateManifestSHA256 string                 `json:"candidate_manifest_sha256"`
	SourceTreeSHA256        string                 `json:"source_tree_sha256"`
	SnapshotSHA256          string                 `json:"snapshot_sha256"`
	SourceRevision          SelectedSourceRevision `json:"source_revision"`
	ModelSHA256             string                 `json:"model_sha256"`
	ReviewSHA256            string                 `json:"review_sha256"`
	WorkplanSHA256          string                 `json:"workplan_sha256"`
	Review                  verifierReview         `json:"review"`
	NetworkMode             string                 `json:"network_mode"`
	Toolchain               verifierToolchain      `json:"toolchain"`
	Checks                  []verifierCheck        `json:"checks"`
	Outcome                 string                 `json:"outcome"`
}

type verifierCheckSpec struct {
	id         string
	argv       []string
	required   bool
	diagnostic bool
	loopback   bool
}

func verifierCheckSpecs() []verifierCheckSpec {
	return []verifierCheckSpec{
		{id: "focused-step10", required: true, loopback: true, argv: []string{"nix", "develop", "--command", "go", "test", "-count=1", "-run", "^TestStep10.*", "./internal/wipdauthority"}},
		{id: "lifecycle-regression", required: true, loopback: true, argv: []string{"nix", "develop", "--command", "go", "test", "-count=1", "-run", "^TestM6LifecycleCommandsThroughWipdProcess$", "./internal/wipdauthority"}},
		{id: "adapter-operation-remote-race", required: true, loopback: true, argv: []string{"nix", "develop", "--command", "go", "test", "-race", "-count=1", "./internal/schedulerhook", "./internal/operation", "./internal/wipdremote"}},
		{id: "changed-package-regressions", required: true, loopback: true, argv: []string{"nix", "develop", "--command", "go", "test", "-count=1", "./internal/scheduler", "./internal/runlock", "./internal/writesurface", "./internal/operation", "./internal/schedulerhook", "./internal/wipd", "./internal/wipdremote", "./internal/wipdauthority", "./internal/wipdjournal", "./internal/authoritystore"}},
		{id: "repository-check", required: true, loopback: true, argv: []string{"nix", "develop", "--command", "make", "check"}},
		{id: "candidate-build", required: true, argv: []string{"nix", "build", "--no-link", "path:/snapshot"}},
		{id: "default-flake-build", diagnostic: true, argv: []string{"nix", "build", "--no-link"}},
		{id: "cross-compile", required: true, argv: []string{"nix", "develop", "--command", "make", "cross-compile"}},
		{id: "snapshot-integrity", required: true, argv: []string{"internal", "verify-pinned-snapshot"}},
		{id: "gofumpt", required: true, argv: []string{"nix", "develop", "--command", "contrib/check-gofumpt"}},
		{id: "static-whitespace-yaml-conflicts", required: true, argv: []string{"internal", "read-only-static-checks"}},
		{id: "shellcheck", required: true, argv: []string{"nix", "develop", "--command", "shellcheck", ".envrc", "contrib/check-commit-msg", "contrib/check-gofumpt", "contrib/release", ".agents/setup", ".agents/resume", "contrib/orb-smoke/entrypoint", "contrib/orb-smoke/run", "contrib/orb-smoke/test-policy", "contrib/online-authority-proof/run", "contrib/online-authority-proof/runtime-test", "contrib/online-authority-proof/test-policy", "scripts/install.sh"}},
		{id: "golangci-lint", required: true, argv: []string{"nix", "develop", "--command", "golangci-lint", "run"}},
	}
}

type verifierSandbox struct {
	nixPath           string
	bwrapPath         string
	goModulesPath     string
	closure           []string
	closureProjection string
	workspace         string
	storeScratch      string
	nixDatabase       string
	goCache           string
	hostsPath         string
	profile           string
	toolchain         verifierToolchain
}

type boundedOutput struct {
	hash  hash.Hash
	size  int64
	tail  []byte
	limit int
}

func newBoundedOutput() *boundedOutput {
	return &boundedOutput{hash: sha256.New(), limit: verifierTailBytes}
}

func (w *boundedOutput) Write(data []byte) (int, error) {
	_, _ = w.hash.Write(data)
	w.size += int64(len(data))
	if len(data) >= w.limit {
		w.tail = bytes.Clone(data[len(data)-w.limit:])
	} else if len(w.tail)+len(data) > w.limit {
		w.tail = append(bytes.Clone(w.tail[len(w.tail)+len(data)-w.limit:]), data...)
	} else {
		w.tail = append(w.tail, data...)
	}
	return len(data), nil
}

func (w *boundedOutput) digest() string { return hex.EncodeToString(w.hash.Sum(nil)) }

func (a *Adapter) startStep10Verifier(ctx context.Context, request Request, key Key, intent []byte, inputDigest string) (Result, error) {
	if ctx == nil || validateRequest(request) != nil {
		return Result{}, ErrInvalidRequest
	}
	review, candidate, tree, err := validateStep10Review(request, key.DomainID)
	if err != nil {
		return Result{}, err
	}
	lock, err := a.lockEngagement(ctx, key)
	if err != nil {
		return Result{}, err
	}
	defer releaseEngagementLock(lock)

	stored, found, err := a.loadStep10Engagement(ctx, key, intent, inputDigest)
	if err != nil {
		return Result{}, err
	}
	if found != nil {
		if found.Typed.Work == nil || found.Typed.Work.Revision != request.Revision ||
			len(found.Typed.Work.EvidenceRefs) != 1 {
			return Result{}, ErrCorrupt
		}
		artifact, artifactErr := a.Evidence(ctx, found.Typed.Work.EvidenceRefs[0])
		if artifactErr != nil || validateStoredVerifierEvidence(artifact, request, key.DomainID, inputDigest, review, candidate, tree) != nil {
			return Result{}, ErrCorrupt
		}
		return *found, nil
	}
	if err = a.ensureStep10Intent(ctx, key, intent, inputDigest); err != nil {
		return Result{}, err
	}

	scratch, err := os.MkdirTemp(verifierScratchDir, verifierScratchPrefix)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if cleanupErr := removeVerifierScratchTree(scratch); cleanupErr != nil {
			fmt.Printf("step10 verifier scratch cleanup failed: %v\n", cleanupErr)
		}
	}()
	if err = os.Chmod(scratch, 0o700); err != nil {
		return Result{}, err
	}
	snapshot := inputBlob(request.Inputs, "snapshot").Bytes
	materialized := filepath.Join(scratch, "snapshot")
	if err = os.Mkdir(materialized, 0o700); err != nil {
		return Result{}, err
	}
	if err = extractPinnedSnapshot(snapshot, tree, materialized); err != nil {
		return Result{}, err
	}
	if err = makeSnapshotReadOnly(materialized); err != nil {
		return Result{}, err
	}

	sandbox, sandboxErr := resolveVerifierSandbox(ctx, scratch, materialized)
	checks := make([]verifierCheck, 0, len(verifierCheckSpecs()))
	checks = append(checks, stored...)
	known := make(map[string]bool, len(checks))
	for _, check := range checks {
		known[check.ID] = true
	}
	for _, spec := range verifierCheckSpecs() {
		if known[spec.id] {
			continue
		}
		check := verifierCheck{
			ID: spec.id, CheckSet: step10VerifierCheckSet, Required: spec.required, Diagnostic: spec.diagnostic,
			Argv: append([]string(nil), spec.argv...), Environment: []string{
				"HOME=/home/verifier", "TMPDIR=/tmp", "GOCACHE=/tmp/go-cache", "GOMODCACHE=/tmp/go-mod-cache",
				"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", verifierNetworkLabel(spec),
				"WIPD_STEP10_VERIFIER_CHECK=" + spec.id, "NIX_REMOTE=local",
				"NIX_CONFIG=" + verifierCheckNixConfig(spec),
			},
			CleanStateID: randomVerifierID(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ExitCode: -1,
		}
		if sandbox != nil {
			check.Environment = append(check.Environment, "PATH="+filepath.Dir(sandbox.nixPath))
		}
		if spec.id == "default-flake-build" {
			check.Environment = append(check.Environment, "source_selection=immutable-path-snapshot-without-git-metadata")
		}
		if check.CleanStateID == "unavailable" {
			return Result{}, errors.New("secure randomness unavailable for clean-state identity")
		}
		empty := sha256.Sum256(nil)
		check.StdoutSHA256, check.StderrSHA256 = hex.EncodeToString(empty[:]), hex.EncodeToString(empty[:])
		if sandboxErr == nil {
			check.ToolchainSHA256 = verifierCheckToolchainDigest(sandbox.toolchain, check.Environment)
		}
		started := time.Now()
		if spec.id == "snapshot-integrity" {
			check.ExitCode, check.Problem, check.StdoutBytes, check.StdoutSHA256, check.StdoutTail = runSnapshotIntegrityCheck(materialized, tree, snapshot)
		} else if spec.id == "static-whitespace-yaml-conflicts" {
			check.ExitCode, check.Problem = runSnapshotStaticCheck(materialized, tree)
			if check.Problem != "" {
				check.StderrBytes = int64(len(check.Problem) + 1)
				check.StderrSHA256 = verifierRawSHA256([]byte(check.Problem + "\n"))
				check.StderrTail = check.Problem + "\n"
			}
		} else if sandboxErr != nil {
			check.Skipped = true
			check.Problem = "isolated toolchain unavailable: " + sandboxErr.Error()
		} else {
			check.ExitCode, check.Problem, check.StdoutBytes, check.StderrBytes,
				check.StdoutSHA256, check.StderrSHA256, check.StdoutTail, check.StderrTail = runVerifierCommand(ctx, sandbox, spec)
		}
		check.DurationNanos = time.Since(started).Nanoseconds()
		if check.ExitCode != 0 && !check.Skipped && check.Problem == "" {
			check.Problem = "command exited nonzero"
		}
		if err = a.storeStep10Check(ctx, key, inputDigest, check); err != nil {
			return Result{}, err
		}
		checks = append(checks, check)
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	if a.afterVerifierChecks != nil {
		a.afterVerifierChecks()
	}
	passed := true
	problemCode := ""
	for _, check := range checks {
		if !check.Required {
			continue
		}
		if check.Skipped || check.ExitCode != 0 {
			passed = false
			if check.Skipped {
				problemCode = "verification.required-check-unavailable"
			} else {
				problemCode = "verification.required-check-failed"
			}
			break
		}
		if sandboxErr != nil || check.ToolchainSHA256 != verifierCheckToolchainDigest(sandbox.toolchain, check.Environment) {
			passed = false
			problemCode = "verification.toolchain-identity-changed"
			break
		}
	}
	if sandboxErr != nil || !validSHA256(sandbox.toolchain.GoSHA256) {
		passed = false
		if problemCode == "" {
			problemCode = "verification.toolchain-identity-incomplete"
		}
	}
	artifact := verifierEvidenceRef{
		Schema: verifierEvidenceSchema, CheckSet: step10VerifierCheckSet,
		DomainID: key.DomainID, EnvironmentID: request.EnvironmentID, RunID: request.RunID,
		MatterID: request.MatterID, TargetID: request.TargetID, VerifierRoleID: request.RoleID,
		ClaimID: request.Claim.ID, ClaimEpoch: request.Claim.Epoch, DispatchID: request.DispatchID,
		OriginalCommandID: request.CommandID, RequestHash: request.RequestHash, InputDigest: inputDigest,
		CandidateRevision:       "candidate-v1:" + strings.TrimPrefix(tree.CandidateManifestSHA256, "sha256:"),
		CandidateManifestSHA256: tree.CandidateManifestSHA256,
		SourceTreeSHA256:        inputBlob(request.Inputs, "tree-manifest").Digest, SnapshotSHA256: tree.SnapshotSHA256,
		SourceRevision: selectedStep10SourceRevision(request.Revision, candidate, tree, inputBlob(request.Inputs, "tree-manifest").Digest),
		ModelSHA256:    inputBlob(request.Inputs, "model").Digest, ReviewSHA256: inputBlob(request.Inputs, "review").Digest,
		WorkplanSHA256: inputBlob(request.Inputs, "workplan").Digest, Review: review,
		NetworkMode: step10VerifierNetworkMode,
		Checks:      checks, Outcome: "not-verified",
	}
	if sandboxErr == nil {
		artifact.Toolchain = sandbox.toolchain
	}
	if passed {
		artifact.Outcome = "verified"
	} else if problemCode == "" {
		problemCode = "verification.required-check-failed"
	}
	artifactBytes, err := json.Marshal(artifact)
	if err != nil || int64(len(artifactBytes)) > maxResultBytes {
		fmt.Printf("step10 verifier evidence size invalid: bytes=%d limit=%d checks=%d closure=%d marshal_error=%v\n",
			len(artifactBytes), maxResultBytes, len(checks), len(artifact.Toolchain.Closure), err)
		return Result{}, ErrInvalidEvidence
	}
	artifactBytes = append(artifactBytes, '\n')
	artifactDigest := blobDigest(artifactBytes)
	if err = validateStoredVerifierEvidence(artifactBytes, request, key.DomainID, inputDigest, review, candidate, tree); err != nil {
		fmt.Printf("step10 verifier stored evidence validation failed: %v outcome=%q checks=%d toolchain=%q closure=%d\n",
			err, artifact.Outcome, len(artifact.Checks), artifact.Toolchain.GoVersion, len(artifact.Toolchain.Closure))
		return Result{}, err
	}
	outcome := WorkNotVerified
	if passed {
		outcome = WorkVerified
	}
	typed := TypedResult{Kind: KindWork, Version: 1, Work: &WorkResult{
		Outcome: outcome, Revision: request.Revision, EvidenceRefs: []string{artifactDigest}, ProblemCode: problemCode,
	}}
	resultDigest, err := hookResultDigest(key, inputDigest, typed)
	if err != nil {
		return Result{}, err
	}
	result := Result{Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest}
	if err = a.commitStep10Result(ctx, result, artifactBytes); err != nil {
		return Result{}, err
	}
	return result, nil
}

func inputBlob(inputs []PinnedBlob, name string) PinnedBlob {
	for _, input := range inputs {
		if input.Name == name {
			return input
		}
	}
	return PinnedBlob{}
}

func validateStoredVerifierEvidence(raw []byte, request Request, domain, inputDigest string, review verifierReview,
	candidate candidateManifest, tree sourceTreeManifest,
) error {
	var artifact verifierEvidenceRef
	if decodeStrictJSON(raw, &artifact) != nil || artifact.Schema != verifierEvidenceSchema ||
		artifact.CheckSet != step10VerifierCheckSet || artifact.DomainID != domain ||
		artifact.EnvironmentID != request.EnvironmentID || artifact.RunID != request.RunID ||
		artifact.MatterID != request.MatterID || artifact.TargetID != request.TargetID ||
		artifact.VerifierRoleID != request.RoleID || request.Claim == nil ||
		artifact.ClaimID != request.Claim.ID || artifact.ClaimEpoch != request.Claim.Epoch ||
		artifact.DispatchID != request.DispatchID || artifact.OriginalCommandID != request.CommandID ||
		artifact.RequestHash != request.RequestHash || artifact.InputDigest != inputDigest ||
		artifact.CandidateRevision != "candidate-v1:"+strings.TrimPrefix(tree.CandidateManifestSHA256, "sha256:") ||
		artifact.CandidateManifestSHA256 != tree.CandidateManifestSHA256 ||
		artifact.SourceTreeSHA256 != inputBlob(request.Inputs, "tree-manifest").Digest ||
		artifact.SnapshotSHA256 != tree.SnapshotSHA256 || artifact.ModelSHA256 != inputBlob(request.Inputs, "model").Digest ||
		artifact.SourceRevision != selectedStep10SourceRevision(request.Revision, candidate, tree, inputBlob(request.Inputs, "tree-manifest").Digest) ||
		artifact.ReviewSHA256 != inputBlob(request.Inputs, "review").Digest ||
		artifact.WorkplanSHA256 != inputBlob(request.Inputs, "workplan").Digest ||
		!sameVerifierReview(artifact.Review, review) || artifact.NetworkMode != step10VerifierNetworkMode ||
		!sameVerifierCheckIDs(artifact.Checks) || artifact.Outcome != "verified" && artifact.Outcome != "not-verified" {
		fmt.Printf("step10 evidence identity/check binding invalid: schema=%q checkset=%q domain=%t environment=%t run=%t matter=%t target=%t role=%t claim=%t dispatch=%t command=%t request=%t input=%t candidate=%t review=%t network=%q checks=%t outcome=%q\n",
			artifact.Schema, artifact.CheckSet, artifact.DomainID == domain, artifact.EnvironmentID == request.EnvironmentID,
			artifact.RunID == request.RunID, artifact.MatterID == request.MatterID, artifact.TargetID == request.TargetID,
			artifact.VerifierRoleID == request.RoleID, request.Claim != nil && artifact.ClaimID == request.Claim.ID && artifact.ClaimEpoch == request.Claim.Epoch,
			artifact.DispatchID == request.DispatchID, artifact.OriginalCommandID == request.CommandID, artifact.RequestHash == request.RequestHash,
			artifact.InputDigest == inputDigest, artifact.CandidateManifestSHA256 == tree.CandidateManifestSHA256,
			sameVerifierReview(artifact.Review, review), artifact.NetworkMode, sameVerifierCheckIDs(artifact.Checks), artifact.Outcome)
		return ErrInvalidEvidence
	}
	if artifact.CandidateManifestSHA256 != blobDigest([]byte(tree.CandidateManifest)) || candidate.Base != step10VerifierBase {
		fmt.Printf("step10 evidence candidate binding invalid: manifest=%t base=%q\n",
			artifact.CandidateManifestSHA256 == blobDigest([]byte(tree.CandidateManifest)), candidate.Base)
		return ErrInvalidEvidence
	}
	passed := true
	for _, check := range artifact.Checks {
		if check.Required && (check.Skipped || check.ExitCode != 0) {
			passed = false
		}
	}
	if !validSHA256(artifact.Toolchain.GoSHA256) {
		passed = false
	}
	if (artifact.Outcome == "verified") != passed {
		fmt.Printf("step10 evidence outcome mismatch: outcome=%q checks_pass=%t go_digest=%t\n",
			artifact.Outcome, passed, validSHA256(artifact.Toolchain.GoSHA256))
		return ErrInvalidEvidence
	}
	if artifact.Outcome == "verified" {
		if !strings.HasPrefix(artifact.Toolchain.NixPath, "/nix/store/") || !validSHA256(artifact.Toolchain.NixSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.BubblewrapPath, "/nix/store/") || !validSHA256(artifact.Toolchain.BubblewrapSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.EnvPath, "/nix/store/") || !validSHA256(artifact.Toolchain.EnvSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.GitPath, "/nix/store/") || !validSHA256(artifact.Toolchain.GitSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.BashPath, "/nix/store/") || !validSHA256(artifact.Toolchain.BashSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.ShasumPath, "/nix/store/") || !validSHA256(artifact.Toolchain.ShasumSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.IPPath, "/nix/store/") || !validSHA256(artifact.Toolchain.IPSHA256) ||
			!strings.HasPrefix(artifact.Toolchain.CapshPath, "/nix/store/") || !validSHA256(artifact.Toolchain.CapshSHA256) ||
			artifact.Toolchain.HostsSHA256 != verifierRawSHA256([]byte(verifierHostsFile)) ||
			!strings.HasPrefix(artifact.Toolchain.GoDerivationPath, "/nix/store/") ||
			!strings.HasPrefix(artifact.Toolchain.GoModulesPath, "/nix/store/") ||
			!strings.HasPrefix(artifact.Toolchain.GoPath, "/nix/store/") || !validSHA256(artifact.Toolchain.GoSHA256) ||
			!validSHA256(artifact.Toolchain.ClosureProjectionSHA256) ||
			!validVerifierNetworkProof(artifact.Toolchain.BootstrapNetworkState, "down") ||
			artifact.Toolchain.GoVersion == "" || len(artifact.Toolchain.Closure) == 0 ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.NixPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.BubblewrapPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.EnvPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.GitPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.BashPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.ShasumPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.IPPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.CapshPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.GoModulesPath) ||
			!containsStoreObject(artifact.Toolchain.Closure, artifact.Toolchain.GoPath) ||
			!sortedUniqueStorePaths(artifact.Toolchain.Closure) {
			fmt.Printf("step10 positive toolchain identity invalid: nix=%q bwrap=%q go=%q modules=%q closure=%d sorted=%t\n",
				artifact.Toolchain.NixPath, artifact.Toolchain.BubblewrapPath, artifact.Toolchain.GoPath,
				artifact.Toolchain.GoModulesPath, len(artifact.Toolchain.Closure), sortedUniqueStorePaths(artifact.Toolchain.Closure))
			return ErrInvalidEvidence
		}
		for _, check := range artifact.Checks {
			if check.Required && (check.Skipped || check.ExitCode != 0 ||
				check.ToolchainSHA256 != verifierCheckToolchainDigest(artifact.Toolchain, check.Environment)) {
				fmt.Printf("step10 required check toolchain binding invalid: id=%q skipped=%t exit=%d check=%s fingerprint=%s\n",
					check.ID, check.Skipped, check.ExitCode, check.ToolchainSHA256,
					verifierCheckToolchainDigest(artifact.Toolchain, check.Environment))
				return ErrInvalidEvidence
			}
		}
	}
	return nil
}

// ValidateStep10VerificationEvidence is the authority-side D106 acceptance
// boundary. The caller supplies the authenticated command's exact pinned bytes
// and verifies the returned evidence artifact against those immutable inputs.
func ValidateStep10VerificationEvidence(raw []byte, request Request, domain string) error {
	review, candidate, tree, err := validateStep10Review(request, domain)
	if err != nil {
		return err
	}
	inputDigest, err := InputDigest(request)
	if err != nil {
		return ErrInvalidEvidence
	}
	if err = validateStoredVerifierEvidence(raw, request, domain, inputDigest, review, candidate, tree); err != nil {
		return err
	}
	var evidence verifierEvidenceRef
	if decodeStrictJSON(raw, &evidence) != nil || evidence.Outcome != "verified" {
		return ErrInvalidEvidence
	}
	return nil
}

func sameVerifierReview(left, right verifierReview) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func sameVerifierCheckIDs(checks []verifierCheck) bool {
	specs := verifierCheckSpecs()
	if len(checks) != len(specs) {
		return false
	}
	byID := make(map[string]verifierCheck, len(checks))
	cleanStates := make(map[string]bool, len(checks))
	previousID := ""
	for _, check := range checks {
		if previousID != "" && previousID >= check.ID || cleanStates[check.CleanStateID] {
			fmt.Printf("invalid durable verifier check ordering or clean state: previous=%q check=%q clean-state=%q\n",
				previousID, check.ID, check.CleanStateID)
			return false
		}
		previousID = check.ID
		cleanStates[check.CleanStateID] = true
		byID[check.ID] = check
	}
	for _, spec := range specs {
		check, ok := byID[spec.id]
		if !ok {
			fmt.Printf("missing durable verifier check: id=%q\n", spec.id)
			return false
		}
		if !validVerifierCheckRecord(check, spec) {
			fmt.Printf("invalid durable verifier check record: id=%q required=%t diagnostic=%t skipped=%t exit=%d problem=%q environment=%q started=%q duration=%d stdout=%d/%s stderr=%d/%s\n",
				check.ID, check.Required, check.Diagnostic, check.Skipped, check.ExitCode, check.Problem,
				check.Environment, check.StartedAt, check.DurationNanos, check.StdoutBytes, check.StdoutSHA256,
				check.StderrBytes, check.StderrSHA256)
			return false
		}
	}
	return true
}

func validVerifierCheckRecord(check verifierCheck, spec verifierCheckSpec) bool {
	started, err := time.Parse(time.RFC3339Nano, check.StartedAt)
	if check.ID != spec.id || check.CheckSet != step10VerifierCheckSet || check.Required != spec.required ||
		check.Diagnostic != spec.diagnostic || !sameStrings(check.Argv, spec.argv) ||
		!validVerifierCleanStateID(check.CleanStateID) || err != nil || started.IsZero() || check.DurationNanos < 0 ||
		check.ExitCode < -1 || check.StdoutBytes < 0 || check.StderrBytes < 0 ||
		len(check.StdoutTail) > verifierTailBytes || len(check.StderrTail) > verifierTailBytes ||
		!validSHA256(check.StdoutSHA256) || !validSHA256(check.StderrSHA256) ||
		check.Required && !check.Skipped && spec.id != "snapshot-integrity" &&
			spec.id != "static-whitespace-yaml-conflicts" && !validSHA256(check.ToolchainSHA256) ||
		check.Skipped && (check.ExitCode != -1 || check.Problem == "") ||
		check.ExitCode == 0 && check.Problem != "" || check.ExitCode != 0 && check.Problem == "" {
		return false
	}
	expectedEnvironment := []string{
		"HOME=/home/verifier", "TMPDIR=/tmp", "GOCACHE=/tmp/go-cache", "GOMODCACHE=/tmp/go-mod-cache",
		"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", verifierNetworkLabel(spec),
		"WIPD_STEP10_VERIFIER_CHECK=" + spec.id, "NIX_REMOTE=local",
		"NIX_CONFIG=" + verifierCheckNixConfig(spec),
	}
	if len(check.Environment) > len(expectedEnvironment) && strings.HasPrefix(check.Environment[len(expectedEnvironment)], "PATH=") {
		path := check.Environment[len(expectedEnvironment)]
		if !strings.HasPrefix(path, "PATH=/nix/store/") || strings.Contains(strings.TrimPrefix(path, "PATH="), ":") {
			return false
		}
		expectedEnvironment = append(expectedEnvironment, path)
	}
	if spec.id == "default-flake-build" {
		expectedEnvironment = append(expectedEnvironment, "source_selection=immutable-path-snapshot-without-git-metadata")
	}
	if !sameStrings(check.Environment, expectedEnvironment) {
		return false
	}
	if check.ExitCode == 0 && spec.id != "snapshot-integrity" && spec.id != "static-whitespace-yaml-conflicts" {
		loopbackState := "down"
		if spec.loopback {
			loopbackState = "up"
		}
		if !validVerifierNetworkProof(verifierNetworkProof([]byte(check.StderrTail)), loopbackState) {
			return false
		}
	}
	return true
}

func verifierNetworkLabel(spec verifierCheckSpec) string {
	if spec.loopback {
		return "network=namespace-loopback-only;external-egress-denied"
	}
	return "network=unshared;external-egress-denied"
}

func validVerifierCleanStateID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}

func (a *Adapter) lockEngagement(ctx context.Context, key Key) (*os.File, error) {
	if ctx == nil {
		return nil, ErrInvalidRequest
	}
	sum := sha256.Sum256([]byte(key.DomainID + "\x00" + key.CommandID))
	path := filepath.Join(a.profileRoot, "engagement-"+hex.EncodeToString(sum[:])[:32]+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, ErrCorrupt
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func releaseEngagementLock(file *os.File) {
	if file != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
}

func (a *Adapter) loadStep10Engagement(ctx context.Context, key Key, intent []byte, inputDigest string) ([]verifierCheck, *Result, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedIntent, raw []byte
	var state, storedDigest, resultDigest string
	err = tx.QueryRowContext(ctx, `SELECT intent,state,input_digest,coalesce(result_digest,''),result FROM hook_engagements
		WHERE domain_id=? AND command_id=?`, key.DomainID, key.CommandID).
		Scan(&storedIntent, &state, &storedDigest, &resultDigest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(storedIntent, intent) || storedDigest != inputDigest {
		return nil, nil, ErrConflict
	}
	if state == "completed" {
		result, decodeErr := decodeResult(key, inputDigest, resultDigest, raw)
		if decodeErr != nil || tx.Commit() != nil {
			return nil, nil, ErrCorrupt
		}
		return nil, &result, nil
	}
	if state != "running" {
		return nil, nil, ErrCorrupt
	}
	rows, err := tx.QueryContext(ctx, `SELECT check_id,record FROM hook_check_runs WHERE domain_id=? AND command_id=? AND input_digest=?`,
		key.DomainID, key.CommandID, inputDigest)
	if err != nil {
		return nil, nil, err
	}
	var checks []verifierCheck
	for rows.Next() {
		var id string
		var record []byte
		if err = rows.Scan(&id, &record); err != nil {
			break
		}
		var check verifierCheck
		if decodeStrictJSON(record, &check) != nil || id != check.ID {
			err = ErrCorrupt
			break
		}
		checks = append(checks, check)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || tx.Commit() != nil {
		return nil, nil, ErrCorrupt
	}
	for _, check := range checks {
		if !sameVerifierCheckIDsForOne(check) {
			return nil, nil, ErrCorrupt
		}
	}
	return checks, nil, nil
}

func sameVerifierCheckIDsForOne(check verifierCheck) bool {
	for _, spec := range verifierCheckSpecs() {
		if check.ID == spec.id {
			return validVerifierCheckRecord(check, spec)
		}
	}
	return false
}

func (a *Adapter) ensureStep10Intent(ctx context.Context, key Key, intent []byte, inputDigest string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hook_engagements(domain_id,command_id,intent,state,input_digest)
		VALUES(?,?,?,'running',?)`, key.DomainID, key.CommandID, intent, inputDigest); err != nil {
		return err
	}
	var storedIntent []byte
	var state, digest string
	if err = tx.QueryRowContext(ctx, `SELECT intent,state,input_digest FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		key.DomainID, key.CommandID).Scan(&storedIntent, &state, &digest); err != nil {
		return err
	}
	if !bytes.Equal(storedIntent, intent) || digest != inputDigest {
		return ErrConflict
	}
	if state != "running" {
		return ErrCorrupt
	}
	return tx.Commit()
}

func (a *Adapter) storeStep10Check(ctx context.Context, key Key, inputDigest string, check verifierCheck) error {
	record, err := json.Marshal(check)
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hook_check_runs(domain_id,command_id,input_digest,check_id,record)
		VALUES(?,?,?,?,?)`, key.DomainID, key.CommandID, inputDigest, check.ID, record); err != nil {
		return err
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, `SELECT record FROM hook_check_runs WHERE domain_id=? AND command_id=? AND input_digest=? AND check_id=?`,
		key.DomainID, key.CommandID, inputDigest, check.ID).Scan(&stored); err != nil || !bytes.Equal(record, stored) {
		return ErrConflict
	}
	return tx.Commit()
}

func (a *Adapter) commitStep10Result(ctx context.Context, result Result, artifact []byte) error {
	if blobDigest(artifact) != result.Typed.Work.EvidenceRefs[0] {
		return ErrCorrupt
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO hook_evidence(domain_id,digest,byte_length,bytes) VALUES(?,?,?,?)`,
		result.Key.DomainID, result.Typed.Work.EvidenceRefs[0], len(artifact), artifact); err != nil {
		return err
	}
	var storedArtifact []byte
	if err = tx.QueryRowContext(ctx, `SELECT bytes FROM hook_evidence WHERE domain_id=? AND digest=?`,
		result.Key.DomainID, result.Typed.Work.EvidenceRefs[0]).Scan(&storedArtifact); err != nil || !bytes.Equal(storedArtifact, artifact) {
		return ErrCorrupt
	}
	resultBytes, err := wipdwire.EncodeCanonical(result.Typed)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE hook_engagements SET state='completed',result_digest=?,result=?
		WHERE domain_id=? AND command_id=? AND input_digest=? AND state='running'`,
		result.ResultDigest, resultBytes, result.Key.DomainID, result.Key.CommandID, result.InputDigest)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return ErrCorrupt
	}
	return tx.Commit()
}

func randomVerifierID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(raw[:])
}

func extractPinnedSnapshot(raw []byte, tree sourceTreeManifest, destination string) (resultErr error) {
	var reader io.Reader = bytes.NewReader(raw)
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		compressed, err := gzip.NewReader(reader)
		if err != nil {
			return ErrInvalidEvidence
		}
		defer func() {
			resultErr = errors.Join(resultErr, compressed.Close())
		}()
		reader = compressed
	}
	tarReader := tar.NewReader(reader)
	expected := make(map[string]sourceFile, len(tree.Files))
	for _, file := range tree.Files {
		expected[file.Path] = file
	}
	seen := make(map[string]bool, len(expected))
	var expanded int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalidEvidence
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "." && header.Typeflag == tar.TypeDir {
			continue
		}
		if !safeSnapshotPath(name) {
			return ErrInvalidEvidence
		}
		path := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if !snapshotDirectoryExpected(name, expected) || header.Size != 0 {
				return ErrInvalidEvidence
			}
			if err = os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			file, ok := expected[name]
			if !ok || seen[name] || header.Size != file.Size || header.Size < 0 || expanded > verifierMaxTreeBytes-header.Size {
				return ErrInvalidEvidence
			}
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			output, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if openErr != nil {
				return openErr
			}
			hash := sha256.New()
			written, copyErr := io.CopyN(io.MultiWriter(output, hash), tarReader, header.Size)
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil || written != header.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
				return ErrInvalidEvidence
			}
			mode := os.FileMode(0o644)
			if file.Executable {
				mode = 0o755
			}
			if err = os.Chmod(path, mode); err != nil {
				return err
			}
			seen[name] = true
			expanded += written
		default:
			return ErrInvalidEvidence
		}
	}
	if len(seen) != len(expected) {
		return ErrInvalidEvidence
	}
	return nil
}

func runSnapshotIntegrityCheck(root string, tree sourceTreeManifest, snapshot []byte) (int, string, int64, string, string) {
	expected := make(map[string]sourceFile, len(tree.Files))
	for _, file := range tree.Files {
		expected[file.Path] = file
	}
	var totalBytes int64
	fileCount := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		name := filepath.ToSlash(relative)
		file, ok := expected[name]
		if !ok {
			return fmt.Errorf("unexpected materialized file: %s", name)
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != file.Size || (info.Mode()&0o111 != 0) != file.Executable {
			return fmt.Errorf("materialized metadata mismatch: %s", name)
		}
		actualDigest := fileSHA256(path)
		if actualDigest != file.SHA256 {
			return fmt.Errorf("materialized content mismatch: %s", name)
		}
		delete(expected, name)
		totalBytes += info.Size()
		fileCount++
		return nil
	})
	if err == nil && len(expected) != 0 {
		for path := range expected {
			err = fmt.Errorf("missing materialized file: %s", path)
			break
		}
	}
	if err != nil {
		problem := err.Error()
		return 1, problem, 0, verifierRawSHA256(nil), ""
	}
	report, marshalErr := json.Marshal(struct {
		CandidateManifestSHA256 string `json:"candidate_manifest_sha256"`
		SourceTreeSHA256        string `json:"source_tree_sha256"`
		SnapshotSHA256          string `json:"snapshot_sha256"`
		FileCount               int    `json:"file_count"`
		TotalBytes              int64  `json:"total_bytes"`
		Outcome                 string `json:"outcome"`
	}{
		CandidateManifestSHA256: tree.CandidateManifestSHA256,
		SourceTreeSHA256:        blobDigest([]byte(tree.CandidateManifest)),
		SnapshotSHA256:          blobDigest(snapshot),
		FileCount:               fileCount,
		TotalBytes:              totalBytes,
		Outcome:                 "verified",
	})
	if marshalErr != nil {
		return 1, marshalErr.Error(), 0, verifierRawSHA256(nil), ""
	}
	return 0, "", int64(len(report)), verifierRawSHA256(report), string(report)
}

func verifierRawSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func snapshotDirectoryExpected(name string, files map[string]sourceFile) bool {
	prefix := name + "/"
	for path := range files {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func makeSnapshotReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0o444)
		if entry.IsDir() {
			mode = 0o555
		}
		if info, statErr := entry.Info(); statErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	})
}

func resolveVerifierSandbox(ctx context.Context, scratch, snapshot string) (*verifierSandbox, error) {
	goCache := filepath.Join(scratch, "go-cache")
	if err := os.Mkdir(goCache, 0o700); err != nil {
		return nil, err
	}
	nixFound, err := exec.LookPath("nix")
	if err != nil {
		return nil, fmt.Errorf("nix executable is unavailable: %w", err)
	}
	nixPath, err := filepath.EvalSymlinks(nixFound)
	if err != nil || !strings.HasPrefix(nixPath, "/nix/store/") {
		return nil, errors.New("nix executable does not resolve to a pinned Nix store path")
	}
	bwrapFound, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap executable is unavailable: %w", err)
	}
	bwrapPath, err := filepath.EvalSymlinks(bwrapFound)
	if err != nil || !strings.HasPrefix(bwrapPath, "/nix/store/") {
		return nil, errors.New("bubblewrap executable does not resolve to a pinned Nix store path")
	}
	if _, err = os.Stat("/nix/var/nix/db"); err != nil {
		return nil, fmt.Errorf("nix local store database is unavailable: %w", err)
	}
	if verifierTreeFileDigest(snapshot, "flake.nix") != step10VerifierFlakeSHA256 ||
		verifierTreeFileDigest(snapshot, "flake.lock") != step10VerifierLockSHA256 {
		return nil, errors.New("pinned candidate changes the trusted offline verifier flake or lock")
	}
	lockedInputs, err := verifierLockedInputs(snapshot)
	if err != nil {
		return nil, err
	}
	storeQueryPath := filepath.Join(filepath.Dir(nixPath), "nix-store")
	storeQueryTarget, err := filepath.EvalSymlinks(storeQueryPath)
	if err != nil || storeQueryTarget != nixPath {
		return nil, errors.New("pinned Nix store-query compatibility entry is unavailable")
	}
	shellFound, err := exec.LookPath("sh")
	if err != nil {
		return nil, fmt.Errorf("fixed verifier bootstrap shell is unavailable: %w", err)
	}
	shellPath, err := filepath.EvalSymlinks(shellFound)
	if err != nil || !strings.HasPrefix(shellPath, "/nix/store/") {
		return nil, errors.New("fixed verifier bootstrap shell does not resolve to a Nix store path")
	}
	bootstrapTools, err := resolveVerifierBootstrapTools(ctx, nixPath, snapshot)
	if err != nil {
		return nil, fmt.Errorf("resolve pinned loopback setup tools for namespace bootstrap: %w", err)
	}
	bootstrapRoots := append([]string(nil), lockedInputs...)
	for _, path := range []string{
		nixPath, bwrapPath, storeQueryTarget, shellPath,
		bootstrapTools.IPPath, bootstrapTools.CapshPath, bootstrapTools.BashPath, bootstrapTools.EnvPath,
	} {
		root, rootErr := nixStoreObjectPath(path)
		if rootErr != nil {
			return nil, rootErr
		}
		bootstrapRoots = append(bootstrapRoots, root)
	}
	bootstrapClosure, err := verifierNixStoreClosure(ctx, storeQueryPath, bootstrapRoots)
	if err != nil {
		return nil, fmt.Errorf("resolve exact locked verifier inputs and bootstrap closure: %w", err)
	}
	storeScratch := filepath.Join(scratch, "nix-store")
	if err = os.Mkdir(storeScratch, 0o755); err != nil {
		return nil, err
	}
	hostsPath := filepath.Join(scratch, "hosts")
	if err = os.WriteFile(hostsPath, []byte(verifierHostsFile), 0o444); err != nil {
		return nil, fmt.Errorf("write isolated static localhost mapping: %w", err)
	}
	values, checkClosure, bootstrapNetworkState, err := evaluateVerifierFlake(ctx, nixPath, bwrapPath, storeQueryPath, shellPath,
		snapshot, storeScratch, bootstrapClosure, bootstrapTools)
	if err != nil {
		return nil, err
	}
	closure := append(append([]string(nil), bootstrapClosure...), checkClosure...)
	for _, path := range []string{
		nixPath, bwrapPath, storeQueryPath, values.EnvPath, values.GoPath, values.GoModules, values.GoDrv,
		values.GitPath, values.BashPath, values.ShasumPath, values.IPPath, values.CapshPath,
	} {
		root, rootErr := nixStoreObjectPath(path)
		if rootErr != nil {
			return nil, rootErr
		}
		closure = append(closure, root)
	}
	sort.Strings(closure)
	closure = compactStrings(closure)
	availableClosure := make([]string, 0, len(closure))
	closureSource := make(map[string]string, len(closure))
	for _, path := range closure {
		if _, statErr := os.Stat(path); statErr == nil {
			availableClosure = append(availableClosure, path)
			closureSource[path] = path
		} else if errors.Is(statErr, os.ErrNotExist) {
			scratchPath := filepath.Join(storeScratch, filepath.Base(path))
			if _, scratchErr := os.Stat(scratchPath); scratchErr == nil {
				availableClosure = append(availableClosure, path)
				closureSource[path] = scratchPath
			} else if !errors.Is(scratchErr, os.ErrNotExist) || !strings.HasSuffix(path, ".drv") {
				return nil, fmt.Errorf("pinned Nix dependency is unavailable: %s: %w", path, errors.Join(statErr, scratchErr))
			}
		} else {
			return nil, fmt.Errorf("inspect pinned Nix dependency %s: %w", path, statErr)
		}
	}
	closure = availableClosure
	closureProjection := filepath.Join(scratch, "closure-projection")
	closureProjectionSHA256, err := materializeVerifierClosure(closure, closureSource, closureProjection)
	if err != nil {
		return nil, fmt.Errorf("materialize exact read-only verifier closure: %w", err)
	}
	if err = retainVerifierNixDatabaseClosure(ctx, filepath.Join(scratch, "nix-db-bootstrap"), closure); err != nil {
		return nil, fmt.Errorf("retain only the pinned verifier closure in the isolated Nix database: %w", err)
	}
	toolchain := verifierToolchain{
		BootstrapNetworkState: bootstrapNetworkState,
		NixPath:               nixPath, BubblewrapPath: bwrapPath, EnvPath: values.EnvPath, EnvSHA256: fileSHA256(values.EnvPath),
		NixSHA256: fileSHA256(nixPath), BubblewrapSHA256: fileSHA256(bwrapPath),
		GitPath: values.GitPath, GitSHA256: fileSHA256(values.GitPath),
		BashPath: values.BashPath, BashSHA256: fileSHA256(values.BashPath),
		ShasumPath: values.ShasumPath, ShasumSHA256: fileSHA256(values.ShasumPath),
		IPPath: values.IPPath, IPSHA256: fileSHA256(values.IPPath), HostsSHA256: fileSHA256(hostsPath),
		CapshPath: values.CapshPath, CapshSHA256: fileSHA256(values.CapshPath),
		GoDerivationPath: values.GoDrv, GoModulesPath: values.GoModules, GoPath: values.GoPath, GoVersion: values.GoVersion,
		GoSHA256: fileSHA256(values.GoPath), ClosureProjectionSHA256: closureProjectionSHA256,
		Closure: append([]string(nil), closure...),
	}
	if toolchain.GoPath == "" || toolchain.GoVersion == "" || !strings.HasPrefix(toolchain.GoDerivationPath, "/nix/store/") ||
		!strings.HasPrefix(toolchain.GoModulesPath, "/nix/store/") || !validSHA256(toolchain.GitSHA256) ||
		!validSHA256(toolchain.BashSHA256) || !validSHA256(toolchain.ShasumSHA256) || !validSHA256(toolchain.IPSHA256) ||
		!validSHA256(toolchain.CapshSHA256) || !validVerifierNetworkProof(toolchain.BootstrapNetworkState, "down") {
		return nil, errors.New("pinned Nix development closure has no identifiable Go toolchain")
	}
	return &verifierSandbox{
		nixPath: nixPath, bwrapPath: bwrapPath, goModulesPath: values.GoModules, closure: closure,
		closureProjection: closureProjection, workspace: snapshot, storeScratch: storeScratch, hostsPath: hostsPath,
		nixDatabase: filepath.Join(scratch, "nix-db-bootstrap"), goCache: goCache, profile: scratch, toolchain: toolchain,
	}, nil
}

func verifierToolchainDigest(toolchain verifierToolchain) string {
	encoded, err := json.Marshal(toolchain)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func verifierCheckToolchainDigest(toolchain verifierToolchain, environment []string) string {
	identity := struct {
		ToolchainSHA256 string   `json:"toolchain_sha256"`
		Environment     []string `json:"environment"`
	}{ToolchainSHA256: verifierToolchainDigest(toolchain), Environment: append([]string(nil), environment...)}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sortedUniqueStorePaths(paths []string) bool {
	previous := ""
	for _, path := range paths {
		if !strings.HasPrefix(path, "/nix/store/") || previous != "" && previous >= path {
			return false
		}
		previous = path
	}
	return len(paths) > 0
}

func nixStoreObjectPath(path string) (string, error) {
	if !strings.HasPrefix(path, "/nix/store/") {
		return "", errors.New("nix dependency is not a store path")
	}
	name := strings.SplitN(strings.TrimPrefix(path, "/nix/store/"), "/", 2)[0]
	if name == "" || name == "." || name == ".." {
		return "", errors.New("nix dependency has no store object name")
	}
	return filepath.Join("/nix/store", name), nil
}

func containsStoreObject(paths []string, path string) bool {
	root, err := nixStoreObjectPath(path)
	return err == nil && containsPath(paths, root)
}

func verifierTreeFileDigest(root, name string) string {
	return fileSHA256(filepath.Join(root, filepath.FromSlash(name)))
}

type verifierFlakeIdentity struct {
	Inputs     []string `json:"inputs"`
	Dev        string   `json:"dev"`
	Package    string   `json:"package"`
	GoDrv      string   `json:"goDrv"`
	GoModules  string   `json:"goModules"`
	GoPath     string   `json:"goPath"`
	GoVersion  string   `json:"goVersion"`
	EnvPath    string   `json:"envPath"`
	GitPath    string   `json:"gitPath"`
	BashPath   string   `json:"bashPath"`
	ShasumPath string   `json:"shasumPath"`
	IPPath     string   `json:"ipPath"`
	CapshPath  string   `json:"capshPath"`
}

type verifierBootstrapTools struct {
	IPPath    string `json:"ipPath"`
	CapshPath string `json:"capshPath"`
	BashPath  string `json:"bashPath"`
	EnvPath   string `json:"envPath"`
}

func resolveVerifierBootstrapTools(ctx context.Context, nixPath, snapshot string) (verifierBootstrapTools, error) {
	var tools verifierBootstrapTools
	if ctx == nil || !strings.HasPrefix(nixPath, "/nix/store/") || !filepath.IsAbs(snapshot) {
		return tools, ErrInvalidRequest
	}
	base := fmt.Sprintf(`let
	f = builtins.getFlake %q;
	system = builtins.currentSystem;
	pkgs = import f.inputs.nixpkgs { inherit system; };
in `, "path:"+snapshot)
	expression := base + `{
	ipPath = "${pkgs.iproute2}/bin/ip";
	capshPath = "${pkgs.libcap}/bin/capsh";
	bashPath = "${pkgs.bash}/bin/bash";
	envPath = "${pkgs.coreutils}/bin/env";
}`
	command := exec.CommandContext(ctx, nixPath, "eval", "--json", "--offline", "--impure", "--no-write-lock-file", "--expr", expression)
	command.Env = []string{
		"HOME=" + filepath.Dir(snapshot), "NIX_REMOTE=daemon",
		"NIX_CONFIG=experimental-features = nix-command flakes\nsubstituters =\nbuild-users-group =\n",
		"PATH=" + filepath.Dir(nixPath),
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return tools, fmt.Errorf("evaluate exact pinned namespace setup tool paths offline: %w: %s", err, boundedText(stderr.Bytes()))
	}
	if len(output) == 0 || len(output) > 4096 || decodeStrictJSON(output, &tools) != nil {
		return tools, errors.New("offline setup tool evaluation returned malformed identities")
	}
	for _, path := range []string{tools.IPPath, tools.CapshPath, tools.BashPath, tools.EnvPath} {
		if !strings.HasPrefix(path, "/nix/store/") {
			return verifierBootstrapTools{}, errors.New("offline setup tool resolved outside the pinned Nix store")
		}
		if _, err = os.Stat(path); err != nil {
			return verifierBootstrapTools{}, fmt.Errorf("inspect pinned setup tool %s: %w", path, err)
		}
	}
	return tools, nil
}

func evaluateVerifierFlake(ctx context.Context, nixPath, bwrapPath, storeQueryPath, shellPath,
	snapshot, storeScratch string, bootstrapClosure []string, bootstrapTools verifierBootstrapTools,
) (verifierFlakeIdentity, []string, string, error) {
	base := fmt.Sprintf(`let
	f = builtins.getFlake %q;
	system = builtins.currentSystem;
	pkgs = import f.inputs.nixpkgs { inherit system; };
	collect = input: [ input.outPath ] ++ builtins.concatLists (builtins.map collect (builtins.attrValues input.inputs));
	inputs = builtins.concatLists (builtins.map collect (builtins.attrValues f.inputs));
in `, "path:/snapshot")
	identityExpression := base + `{
	inputs = inputs;
	dev = f.devShells.${system}.default.drvPath;
	package = f.packages.${system}.default.drvPath;
	goDrv = pkgs.go.drvPath;
	goModules = f.packages.${system}.default.goModules.outPath;
	goPath = "${pkgs.go}/bin/go";
	goVersion = pkgs.go.version;
	envPath = "${pkgs.coreutils}/bin/env";
	gitPath = "${pkgs.git}/bin/git";
	bashPath = "${pkgs.bash}/bin/bash";
	shasumPath = "${pkgs.perl}/bin/shasum";
	ipPath = "${pkgs.iproute2}/bin/ip";
	capshPath = "${pkgs.libcap}/bin/capsh";
	}`
	rootsExpression := base + `builtins.concatStringsSep "\n" (inputs ++ [
	f.devShells.${system}.default.drvPath
	f.packages.${system}.default.drvPath
	pkgs.go.drvPath
	f.packages.${system}.default.goModules.outPath
	"${pkgs.go}"
	"${pkgs.coreutils}"
	"${pkgs.git}"
	"${pkgs.bash}"
	"${pkgs.perl}"
	"${pkgs.iproute2}"
	"${pkgs.libcap}"
])`
	output, stderr, bootstrapNetworkState, err := runVerifierBootstrapCommand(ctx, nixPath, bwrapPath, storeQueryPath, shellPath,
		snapshot, storeScratch, bootstrapClosure, bootstrapTools, identityExpression, rootsExpression)
	if err != nil {
		return verifierFlakeIdentity{}, nil, "", fmt.Errorf("offline isolated flake dependency resolution failed: %w: %s", err, boundedText(stderr))
	}
	if !validVerifierNetworkProof(bootstrapNetworkState, "down") {
		return verifierFlakeIdentity{}, nil, "", errors.New("offline flake bootstrap did not prove isolated loopback-down state")
	}
	parts := bytes.SplitN(output, []byte("\n--WIPD-VERIFIER-CLOSURE--\n"), 2)
	if len(parts) != 2 {
		return verifierFlakeIdentity{}, nil, "", errors.New("isolated flake resolver returned no closure boundary")
	}
	var values verifierFlakeIdentity
	if err = json.Unmarshal(parts[0], &values); err != nil {
		return verifierFlakeIdentity{}, nil, "", fmt.Errorf("decode offline flake identity: %s", boundedText(parts[0]))
	}
	missing := make([]string, 0, 14)
	for name, value := range map[string]string{
		"dev": values.Dev, "package": values.Package, "goDrv": values.GoDrv, "goModules": values.GoModules,
		"goPath": values.GoPath, "goVersion": values.GoVersion, "envPath": values.EnvPath,
		"gitPath": values.GitPath, "bashPath": values.BashPath, "shasumPath": values.ShasumPath,
		"ipPath": values.IPPath, "capshPath": values.CapshPath,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(values.Inputs) == 0 {
		missing = append(missing, "inputs")
	}
	if values.IPPath != bootstrapTools.IPPath || values.CapshPath != bootstrapTools.CapshPath ||
		values.BashPath != bootstrapTools.BashPath || values.EnvPath != bootstrapTools.EnvPath {
		missing = append(missing, "bootstrap-tool-path-match")
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return verifierFlakeIdentity{}, nil, "", fmt.Errorf("offline flake evaluation returned incomplete or substituted toolchain identities: %s",
			strings.Join(missing, ", "))
	}
	for _, path := range append(append([]string(nil), values.Inputs...), values.GoModules, values.EnvPath,
		values.GitPath, values.BashPath, values.ShasumPath, values.IPPath, values.CapshPath) {
		if !strings.HasPrefix(path, "/nix/store/") {
			return verifierFlakeIdentity{}, nil, "", errors.New("flake input or Go module closure resolved outside the Nix store")
		}
	}
	closure := strings.Fields(string(parts[1]))
	for _, path := range closure {
		if !strings.HasPrefix(path, "/nix/store/") {
			return verifierFlakeIdentity{}, nil, "", errors.New("nix dependency closure contains a non-store path")
		}
	}
	return values, closure, bootstrapNetworkState, nil
}

func runVerifierBootstrapCommand(ctx context.Context, nixPath, bwrapPath, storeQueryPath, shellPath,
	snapshot, storeScratch string, bootstrapClosure []string, bootstrapTools verifierBootstrapTools,
	identityExpression, rootsExpression string,
) (outputBytes, stderrBytes []byte, networkState string, resultErr error) {
	if ctx == nil || identityExpression == "" || rootsExpression == "" ||
		!strings.HasPrefix(nixPath, "/nix/store/") || !strings.HasPrefix(storeQueryPath, "/nix/store/") ||
		!strings.HasPrefix(shellPath, "/nix/store/") || !strings.HasPrefix(bootstrapTools.IPPath, "/nix/store/") ||
		!strings.HasPrefix(bootstrapTools.CapshPath, "/nix/store/") || !strings.HasPrefix(bootstrapTools.BashPath, "/nix/store/") ||
		!strings.HasPrefix(bootstrapTools.EnvPath, "/nix/store/") {
		return nil, nil, "", ErrInvalidRequest
	}
	var err error
	lockLayer := filepath.Join(filepath.Dir(storeScratch), "nix-db-bootstrap")
	if err := prepareVerifierNixDatabaseLayer(ctx, lockLayer); err != nil {
		return nil, nil, "", fmt.Errorf("prepare isolated Nix database snapshot: %w", err)
	}
	bootstrapProjection := filepath.Join(filepath.Dir(storeScratch), "bootstrap-closure")
	defer func() {
		if cleanupErr := removeVerifierScratchTree(bootstrapProjection); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	if _, err := materializeVerifierClosure(bootstrapClosure, nil, bootstrapProjection); err != nil {
		return nil, nil, "", fmt.Errorf("materialize exact read-only bootstrap closure: %w", err)
	}
	storeWork := filepath.Join(filepath.Dir(storeScratch), "nix-store-work")
	if err = os.Mkdir(storeWork, 0o700); err != nil {
		return nil, nil, "", err
	}
	defer func() {
		if cleanupErr := removeVerifierScratchTree(storeWork); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	hostUserNS, err := verifierNamespaceID("/proc/self/ns/user", "user")
	if err != nil {
		return nil, nil, "", err
	}
	hostNetNS, err := verifierNamespaceID("/proc/self/ns/net", "net")
	if err != nil {
		return nil, nil, "", err
	}
	args := []string{
		"--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--new-session",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/home", "--dir", "/home/verifier",
		"--dir", "/nix", "--dir", "/nix/store",
		"--dir", "/nix/var", "--dir", "/nix/var/nix", "--bind", lockLayer, "/nix/var/nix/db",
		"--dir", "/snapshot", "--ro-bind", snapshot, "/snapshot",
		"--chdir", "/snapshot",
		"--setenv", "HOME", "/home/verifier", "--setenv", "TMPDIR", "/tmp",
		"--setenv", "XDG_CONFIG_HOME", "/tmp/config", "--setenv", "NIX_REMOTE", "local",
		"--setenv", "NIX_CONFIG", "experimental-features = nix-command flakes\nsubstituters =\nbuild-users-group =\n",
		"--setenv", "WIPD_NIX_BIN", nixPath, "--setenv", "WIPD_NIX_STORE_BIN", storeQueryPath,
		"--setenv", "WIPD_NIX_IDENTITY_EXPR", identityExpression, "--setenv", "WIPD_NIX_ROOTS_EXPR", rootsExpression,
	}
	args = append(args, verifierNetworkNamespaceArgs()...)
	args = append(args, verifierNixStoreOverlayArgs(bootstrapProjection, storeScratch, storeWork)...)
	const bootstrapScript = `set -eu
set -f
identity=$("$WIPD_NIX_BIN" eval --json --offline --impure --no-write-lock-file --expr "$WIPD_NIX_IDENTITY_EXPR")
roots=$("$WIPD_NIX_BIN" eval --raw --offline --impure --no-write-lock-file --expr "$WIPD_NIX_ROOTS_EXPR")
set -- $roots
closure=$("$WIPD_NIX_STORE_BIN" --query --requisites --include-outputs "$@")
printf '%s\n--WIPD-VERIFIER-CLOSURE--\n%s\n' "$identity" "$closure"
`
	networkScript := verifierNetworkSetupScript(verifierToolchain{
		IPPath: bootstrapTools.IPPath, CapshPath: bootstrapTools.CapshPath,
		BashPath: bootstrapTools.BashPath, EnvPath: bootstrapTools.EnvPath,
	}, hostUserNS, hostNetNS, false)
	args = append(args, "--", shellPath, "-eu", "-c", networkScript,
		"step10-bootstrap", shellPath, "-eu", "-c", bootstrapScript)
	command := exec.CommandContext(ctx, bwrapPath, args...)
	command.Env = []string{"PATH=" + filepath.Dir(bwrapPath), "HOME=/nonexistent"}
	var output, stderr bytes.Buffer
	command.Stdout, command.Stderr = &output, &stderr
	err = command.Run()
	networkState = verifierNetworkProof(stderr.Bytes())
	return output.Bytes(), stderr.Bytes(), networkState, err
}

func prepareVerifierNixDatabaseLayer(ctx context.Context, path string) error {
	return copyVerifierNixDatabaseLayer(ctx, "/nix/var/nix/db", path)
}

func copyVerifierNixDatabaseLayer(ctx context.Context, source, path string) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	database, err := sql.Open("sqlite", "file:"+filepath.Join(source, "db.sqlite")+"?mode=ro")
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(1)
	_, vacuumErr := database.ExecContext(ctx, `VACUUM INTO ?`, filepath.Join(path, "db.sqlite"))
	databaseErr := database.Close()
	if err = errors.Join(vacuumErr, databaseErr); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(path, "db.sqlite"), 0o600); err != nil {
		return err
	}
	schema, err := os.ReadFile(filepath.Join(source, "schema"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(path, "schema"), schema, 0o444); err != nil {
		return err
	}
	reservedInfo, err := os.Stat(filepath.Join(source, "reserved"))
	if err != nil || !reservedInfo.Mode().IsRegular() || reservedInfo.Size() < 0 {
		return errors.Join(errors.New("nix reserved-space file is unavailable"), err)
	}
	reserved, err := os.OpenFile(filepath.Join(path, "reserved"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	err = reserved.Truncate(reservedInfo.Size())
	err = errors.Join(err, reserved.Close())
	if err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(path, "reserved"), 0o600); err != nil {
		return err
	}
	for _, entry := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "big-lock", mode: 0o600},
	} {
		file, createErr := os.OpenFile(filepath.Join(path, entry.name), os.O_CREATE|os.O_EXCL|os.O_RDWR, entry.mode)
		if createErr != nil {
			return createErr
		}
		if createErr = file.Close(); createErr != nil {
			return createErr
		}
		if err = os.Chmod(filepath.Join(path, entry.name), entry.mode); err != nil {
			return err
		}
	}
	return nil
}

func retainVerifierNixDatabaseClosure(ctx context.Context, path string, closure []string) (resultErr error) {
	if ctx == nil || !sortedUniqueStorePaths(closure) {
		return ErrInvalidEvidence
	}
	database, err := sql.Open("sqlite", "file:"+filepath.Join(path, "db.sqlite"))
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(1)
	defer func() {
		resultErr = errors.Join(resultErr, database.Close())
	}()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); !errors.Is(rollbackErr, sql.ErrTxDone) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	if _, err = tx.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE verifier_allowed_paths (path TEXT PRIMARY KEY NOT NULL)`); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO verifier_allowed_paths (path) VALUES (?)`)
	if err != nil {
		return err
	}
	for _, storePath := range closure {
		if _, err = insert.ExecContext(ctx, storePath); err != nil {
			break
		}
	}
	if closeErr := insert.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	var registered int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ValidPaths WHERE path IN (SELECT path FROM verifier_allowed_paths)`).Scan(&registered); err != nil {
		return err
	}
	if registered != len(closure) {
		return fmt.Errorf("isolated Nix database has %d of %d pinned closure registrations", registered, len(closure))
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM Refs WHERE referrer NOT IN (SELECT id FROM ValidPaths WHERE path IN (SELECT path FROM verifier_allowed_paths)) OR reference NOT IN (SELECT id FROM ValidPaths WHERE path IN (SELECT path FROM verifier_allowed_paths))`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM ValidPaths WHERE path NOT IN (SELECT path FROM verifier_allowed_paths)`); err != nil {
		return err
	}
	return tx.Commit()
}

func verifierLockedInputs(snapshot string) (result []string, resultErr error) {
	lockBytes, err := os.ReadFile(filepath.Join(snapshot, "flake.lock"))
	if err != nil {
		return nil, err
	}
	var lock struct {
		Nodes map[string]struct {
			Locked struct {
				NARHash string `json:"narHash"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if json.Unmarshal(lockBytes, &lock) != nil || len(lock.Nodes) == 0 {
		return nil, errors.New("pinned verifier flake lock is malformed")
	}
	var encodedHashes []string
	for name, node := range lock.Nodes {
		if name == "root" {
			continue
		}
		const prefix = "sha256-"
		if !strings.HasPrefix(node.Locked.NARHash, prefix) {
			return nil, fmt.Errorf("pinned flake input %q has no fixed SHA-256 NAR hash", name)
		}
		digest, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(node.Locked.NARHash, prefix))
		if decodeErr != nil || len(digest) != sha256.Size {
			return nil, fmt.Errorf("pinned flake input %q has an invalid NAR hash", name)
		}
		encodedHashes = append(encodedHashes, "fixed:r:sha256:"+nixStoreBase32(digest))
	}
	sort.Strings(encodedHashes)
	encodedHashes = compactStrings(encodedHashes)
	db, err := sql.Open("sqlite", "file:/nix/var/nix/db/db.sqlite?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, db.Close())
	}()
	paths := make([]string, 0, len(encodedHashes))
	for _, ca := range encodedHashes {
		before := len(paths)
		rows, queryErr := db.Query(`SELECT path FROM ValidPaths WHERE ca=? ORDER BY path`, ca)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var path string
			if queryErr = rows.Scan(&path); queryErr != nil {
				break
			}
			paths = append(paths, path)
		}
		queryErr = errors.Join(queryErr, rows.Err(), rows.Close())
		if queryErr != nil {
			return nil, queryErr
		}
		if len(paths) == before || !strings.HasPrefix(paths[len(paths)-1], "/nix/store/") {
			return nil, fmt.Errorf("locked verifier input %s is unavailable in the local Nix store", ca)
		}
	}
	sort.Strings(paths)
	return compactStrings(paths), nil
}

func nixStoreBase32(digest []byte) string {
	const alphabet = "0123456789abcdfghijklmnpqrsvwxyz"
	var encoded strings.Builder
	for index := (len(digest)*8 - 1) / 5; index >= 0; index-- {
		bit := index * 5
		byteIndex, offset := bit/8, bit%8
		value := digest[byteIndex] >> offset
		if byteIndex+1 < len(digest) {
			value |= digest[byteIndex+1] << (8 - offset)
		}
		encoded.WriteByte(alphabet[value&0x1f])
	}
	return encoded.String()
}

func verifierNixStoreClosure(ctx context.Context, storeQueryPath string, roots []string) ([]string, error) {
	if ctx == nil || !strings.HasPrefix(storeQueryPath, "/nix/store/") || len(roots) == 0 {
		return nil, ErrInvalidEvidence
	}
	args := []string{"--query", "--requisites", "--include-outputs"}
	for _, path := range roots {
		if !strings.HasPrefix(path, "/nix/store/") {
			return nil, errors.New("verifier store closure root is not a Nix store path")
		}
		args = append(args, path)
	}
	command := exec.CommandContext(ctx, storeQueryPath, args...)
	command.Env = []string{
		"HOME=/nonexistent", "NIX_REMOTE=daemon",
		"NIX_CONFIG=experimental-features = nix-command flakes\nsubstituters =\n",
		"PATH=" + filepath.Dir(storeQueryPath),
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("query pinned Nix dependency closure: %w", err)
	}
	if len(output) == 0 || len(output) > 16<<20 {
		return nil, errors.New("nix dependency closure output is empty or exceeds its bound")
	}
	closure := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	for _, path := range closure {
		if !strings.HasPrefix(path, "/nix/store/") || filepath.Base(path) == "." || filepath.Base(path) == "/" {
			return nil, errors.New("nix dependency closure contains a non-store path")
		}
	}
	sort.Strings(closure)
	closure = compactStrings(closure)
	for _, root := range roots {
		if !containsPath(closure, root) {
			return nil, fmt.Errorf("nix dependency closure omitted requested root %s", root)
		}
	}
	if len(closure) == 0 {
		return nil, errors.New("nix dependency closure is empty")
	}
	return closure, nil
}

func verifierNetworkNamespaceArgs() []string {
	return []string{"--uid", "0", "--gid", "0", "--cap-add", "CAP_NET_ADMIN"}
}

func verifierShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func verifierNetworkSetupScript(toolchain verifierToolchain, hostUserNS, hostNetNS string, loopback bool) string {
	loopbackState, setupAction := "down", "down"
	if loopback {
		loopbackState, setupAction = "up", "up"
	}
	postScript := `set -eu
set -f
IP=$1
ID=$2
READLINK=$3
HOST_USER_NS=$4
HOST_NET_NS=$5
LOOPBACK_ACTION=$6
EXPECTED_SETUP_CAPS=$7
PRE_SETUP_STATE=$8
POST_SETUP_STATE=$9
EXPECTED_LOOPBACK=` + verifierShellQuote(loopbackState) + `
shift 9

fail() { printf 'verifier loopback postflight failed: %s\n' "$1" >&2; exit 125; }
cap_snapshot() {
	while IFS= read -r line; do
		case "$line" in
			CapInh:*|CapPrm:*|CapEff:*|CapBnd:*|CapAmb:*|NoNewPrivs:*) printf '%s\n' "$line" ;;
		esac
	done < /proc/self/status
}
capture_state() {
	label=$1
	printf '%s uid/gid: ' "$label"
	"$ID" -a
	printf '%s parent user namespace: %s\n' "$label" "$HOST_USER_NS"
	printf '%s parent network namespace: %s\n' "$label" "$HOST_NET_NS"
	printf '%s user namespace: ' "$label"
	"$READLINK" /proc/self/ns/user
	printf '%s network namespace: ' "$label"
	"$READLINK" /proc/self/ns/net
	printf '%s uid_map:\n' "$label"
	while IFS= read -r line; do printf '%s\n' "$line"; done < /proc/self/uid_map
	printf '%s gid_map:\n' "$label"
	while IFS= read -r line; do printf '%s\n' "$line"; done < /proc/self/gid_map
	printf '%s links:\n' "$label"
	"$IP" -o link show
	printf '%s ipv4 routes:\n' "$label"
	"$IP" -4 route show
	printf '%s ipv6 routes:\n' "$label"
	"$IP" -6 route show
	printf '%s capability sets:\n' "$label"
	cap_snapshot
}
assert_namespace() {
	user_ns=$("$READLINK" /proc/self/ns/user)
	net_ns=$("$READLINK" /proc/self/ns/net)
	[ "$user_ns" != "$HOST_USER_NS" ] || fail 'user namespace is shared with parent'
	[ "$net_ns" != "$HOST_NET_NS" ] || fail 'network namespace is shared with parent'
}
assert_isolated_interfaces() {
	links=$("$IP" -o link show) || fail 'cannot inspect links'
	case "$links" in *$'\n'*) fail 'unexpected non-loopback interface' ;; esac
	case "$links" in *": lo:"*) ;; *) fail 'loopback interface is missing' ;; esac
	routes4=$("$IP" -4 route show) || fail 'cannot inspect ipv4 routes'
	routes6=$("$IP" -6 route show) || fail 'cannot inspect ipv6 routes'
	[ -z "$routes4" ] || fail 'unexpected ipv4 route'
	[ -z "$routes6" ] || fail 'unexpected ipv6 route'
}
assert_isolated_network() {
	assert_isolated_interfaces
	loopback=$("$IP" -o link show dev lo) || fail 'cannot inspect loopback state'
	case "$loopback" in
		*'<LOOPBACK,UP,'*) loopback_state=up ;;
		*'<LOOPBACK>'*) loopback_state=down ;;
		*) fail 'loopback has an unexpected state' ;;
	esac
	[ "$loopback_state" = "$EXPECTED_LOOPBACK" ] || fail "loopback is $loopback_state; expected $EXPECTED_LOOPBACK"
}
expected_caps=$(printf 'CapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1')
setup_caps=$(printf 'CapInh:\t0000000000001000\nCapPrm:\t0000000000001000\nCapEff:\t0000000000001000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000001000\nNoNewPrivs:\t1')
[ "$EXPECTED_SETUP_CAPS" = "$setup_caps" ] || fail 'setup capability identity changed before check'
actual_caps=$(cap_snapshot)
precheck_state=$(capture_state post-drop-before-check)
printf '%s\n' "$precheck_state" >&2
[ "$("$READLINK" /proc/self/ns/user)" != "$HOST_USER_NS" ] || fail 'user namespace changed or shared with parent'
[ "$("$READLINK" /proc/self/ns/net)" != "$HOST_NET_NS" ] || fail 'network namespace changed or shared with parent'
[ "$actual_caps" = "$expected_caps" ] || fail "unexpected post-drop capability state: $actual_caps"
assert_isolated_network

set +e
"$@"
status=$?
set -e
postcheck_state=$(capture_state post-check)
printf '%s\n' "$postcheck_state" >&2
[ "$(cap_snapshot)" = "$expected_caps" ] || fail 'check wrapper capability state changed'
assert_isolated_network
printf 'pre-check namespace/network/capability state (retained after command):\n%s\n' "$precheck_state" >&2
printf 'pre-drop capability vector (verified before setup):\n%s\n' "$setup_caps" >&2
printf 'post-drop capability vector (verified before and after check):\n%s\n' "$expected_caps" >&2
printf 'loopback setup command: ip link set dev lo %s exit=0\n' "$EXPECTED_LOOPBACK" >&2
printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' \
	'WIPD-VERIFIER-NETWORK-PROOF-BEGIN' \
	"loopback setup command: ip link set dev lo $LOOPBACK_ACTION exit=0" \
	"$PRE_SETUP_STATE" "$POST_SETUP_STATE" "$precheck_state" "$postcheck_state" \
	'WIPD-VERIFIER-NETWORK-PROOF-END' >&2
exit "$status"
`

	return "set -eu\nset -f\n" +
		"IP=" + verifierShellQuote(toolchain.IPPath) + "\n" +
		"CAPSH=" + verifierShellQuote(toolchain.CapshPath) + "\n" +
		"BASH=" + verifierShellQuote(toolchain.BashPath) + "\n" +
		"ID=" + verifierShellQuote(filepath.Join(filepath.Dir(toolchain.EnvPath), "id")) + "\n" +
		"READLINK=" + verifierShellQuote(filepath.Join(filepath.Dir(toolchain.EnvPath), "readlink")) + "\n" +
		"HOST_USER_NS=" + verifierShellQuote(hostUserNS) + "\n" +
		"HOST_NET_NS=" + verifierShellQuote(hostNetNS) + "\n" +
		"EXPECTED_LOOPBACK=" + verifierShellQuote(loopbackState) + "\n" +
		"LOOPBACK_ACTION=" + verifierShellQuote(setupAction) + "\n" +
		`fail() { printf 'verifier network setup failed: %s\n' "$1" >&2; exit 125; }
cap_snapshot() {
	while IFS= read -r line; do
		case "$line" in
			CapInh:*|CapPrm:*|CapEff:*|CapBnd:*|CapAmb:*|NoNewPrivs:*) printf '%s\n' "$line" ;;
		esac
	done < /proc/self/status
}
capture_state() {
	label=$1
	printf '%s uid/gid: ' "$label"
	"$ID" -a
	printf '%s parent user namespace: %s\n' "$label" "$HOST_USER_NS"
	printf '%s parent network namespace: %s\n' "$label" "$HOST_NET_NS"
	printf '%s user namespace: ' "$label"
	"$READLINK" /proc/self/ns/user
	printf '%s network namespace: ' "$label"
	"$READLINK" /proc/self/ns/net
	printf '%s uid_map:\n' "$label"
	while IFS= read -r line; do printf '%s\n' "$line"; done < /proc/self/uid_map
	printf '%s gid_map:\n' "$label"
	while IFS= read -r line; do printf '%s\n' "$line"; done < /proc/self/gid_map
	printf '%s links:\n' "$label"
	"$IP" -o link show
	printf '%s ipv4 routes:\n' "$label"
	"$IP" -4 route show
	printf '%s ipv6 routes:\n' "$label"
	"$IP" -6 route show
	printf '%s capability sets:\n' "$label"
	cap_snapshot
}
assert_namespace() {
	user_ns=$("$READLINK" /proc/self/ns/user)
	net_ns=$("$READLINK" /proc/self/ns/net)
	[ "$user_ns" != "$HOST_USER_NS" ] || fail 'user namespace is shared with parent'
	[ "$net_ns" != "$HOST_NET_NS" ] || fail 'network namespace is shared with parent'
}
assert_isolated_interfaces() {
	links=$("$IP" -o link show) || fail 'cannot inspect links'
	case "$links" in *$'\n'*) fail 'unexpected non-loopback interface' ;; esac
	case "$links" in *": lo:"*) ;; *) fail 'loopback interface is missing' ;; esac
	routes4=$("$IP" -4 route show) || fail 'cannot inspect ipv4 routes'
	routes6=$("$IP" -6 route show) || fail 'cannot inspect ipv6 routes'
	[ -z "$routes4" ] || fail 'unexpected ipv4 route'
	[ -z "$routes6" ] || fail 'unexpected ipv6 route'
}
assert_isolated_network() {
	links=$("$IP" -o link show) || fail 'cannot inspect links'
	case "$links" in *$'\n'*) fail 'unexpected non-loopback interface' ;; esac
	case "$links" in *": lo:"*) ;; *) fail 'loopback interface is missing' ;; esac
	routes4=$("$IP" -4 route show) || fail 'cannot inspect ipv4 routes'
	routes6=$("$IP" -6 route show) || fail 'cannot inspect ipv6 routes'
	[ -z "$routes4" ] || fail 'unexpected ipv4 route'
	[ -z "$routes6" ] || fail 'unexpected ipv6 route'
	loopback=$("$IP" -o link show dev lo) || fail 'cannot inspect loopback state'
	case "$loopback" in
		*'<LOOPBACK,UP,'*) loopback_state=up ;;
		*'<LOOPBACK>'*) loopback_state=down ;;
		*) fail 'loopback has an unexpected state' ;;
	esac
	[ "$loopback_state" = "$EXPECTED_LOOPBACK" ] || fail "loopback is $loopback_state; expected $EXPECTED_LOOPBACK"
}
setup_caps=$(printf 'CapInh:\t0000000000001000\nCapPrm:\t0000000000001000\nCapEff:\t0000000000001000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000001000\nNoNewPrivs:\t1')
assert_namespace
[ "$(cap_snapshot)" = "$setup_caps" ] || fail "unexpected pre-setup capability state: $(cap_snapshot)"
pre_setup_state=$(capture_state pre-setup)
printf '%s\n' "$pre_setup_state" >&2
assert_isolated_interfaces
"$IP" link set dev lo "$LOOPBACK_ACTION" || fail 'cannot set namespace loopback state'
post_setup_state=$(capture_state post-loopback-setup)
printf '%s\n' "$post_setup_state" >&2
assert_namespace
[ "$(cap_snapshot)" = "$setup_caps" ] || fail 'loopback setup changed the expected capability state'
assert_isolated_network
exec "$CAPSH" --noamb --caps=cap_net_admin-eip --no-new-privs --shell="$BASH" -- \
	-eu -c ` + verifierShellQuote(postScript) + ` step10-verifier-post \
	"$IP" "$ID" "$READLINK" "$HOST_USER_NS" "$HOST_NET_NS" "$LOOPBACK_ACTION" "$setup_caps" "$pre_setup_state" "$post_setup_state" "$@"
`
}

func verifierNetworkProof(stderr []byte) string {
	const begin, end = "WIPD-VERIFIER-NETWORK-PROOF-BEGIN", "WIPD-VERIFIER-NETWORK-PROOF-END"
	start := strings.LastIndex(string(stderr), begin)
	finish := strings.LastIndex(string(stderr), end)
	if start < 0 || finish <= start+len(begin) {
		return ""
	}
	return strings.TrimSpace(string(stderr[start+len(begin) : finish]))
}

func validVerifierNetworkProof(proof string, expectedLoopback string) bool {
	if proof == "" || len(proof) > 16<<10 || expectedLoopback != "up" && expectedLoopback != "down" {
		return false
	}
	if strings.Count(proof, "loopback setup command:") != 1 ||
		!strings.Contains(proof, "loopback setup command: ip link set dev lo "+expectedLoopback+" exit=0") {
		return false
	}
	field := func(label, name string) (string, bool) {
		prefix := label + " " + name + ":"
		value, count := "", 0
		for _, line := range strings.Split(proof, "\n") {
			if strings.HasPrefix(line, prefix) {
				value = strings.TrimSpace(strings.TrimPrefix(line, prefix))
				count++
			}
		}
		return value, count == 1
	}
	block := func(label, name, endMarker string) (string, bool) {
		marker := label + " " + name + ":\n"
		if strings.Count(proof, marker) != 1 {
			return "", false
		}
		start := strings.Index(proof, marker) + len(marker)
		end := len(proof) - start
		if endMarker != "" {
			end = strings.Index(proof[start:], endMarker)
			if end < 0 {
				return "", false
			}
		}
		return strings.TrimSpace(proof[start : start+end]), true
	}
	validNamespace := func(value, prefix string) bool {
		if !strings.HasPrefix(value, prefix+"[") || !strings.HasSuffix(value, "]") {
			return false
		}
		id := strings.TrimSuffix(strings.TrimPrefix(value, prefix+"["), "]")
		parsed, err := strconv.ParseUint(id, 10, 64)
		return err == nil && parsed > 0
	}
	mapLineValid := func(value string) bool {
		parts := strings.Fields(value)
		if len(parts) != 3 || parts[0] != "0" || parts[2] != "1" {
			return false
		}
		_, err := strconv.ParseUint(parts[1], 10, 32)
		return err == nil
	}
	setupCaps := "CapInh:\t0000000000001000\nCapPrm:\t0000000000001000\nCapEff:\t0000000000001000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000001000\nNoNewPrivs:\t1"
	postCaps := "CapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1"
	type state struct {
		parentUser, parentNet, user, net string
		uidGID, uidMap, gidMap, links    string
	}
	labels := []string{"pre-setup", "post-loopback-setup", "post-drop-before-check", "post-check"}
	states := make([]state, 0, len(labels))
	for index, label := range labels {
		uidGID, okUIDGID := field(label, "uid/gid")
		parentUser, okParentUser := field(label, "parent user namespace")
		parentNet, okParentNet := field(label, "parent network namespace")
		userNS, okUserNS := field(label, "user namespace")
		netNS, okNetNS := field(label, "network namespace")
		parts := strings.Fields(uidGID)
		if !okUIDGID || len(parts) < 3 || parts[0] != "uid=0" || parts[1] != "gid=0" ||
			!strings.HasPrefix(parts[2], "groups=") || !okParentUser || !okParentNet || !okUserNS || !okNetNS ||
			!validNamespace(parentUser, "user:") || !validNamespace(parentNet, "net:") ||
			!validNamespace(userNS, "user:") || !validNamespace(netNS, "net:") ||
			parentUser == userNS || parentNet == netNS {
			return false
		}
		uidMap, okUIDMap := block(label, "uid_map", label+" gid_map:")
		gidMap, okGIDMap := block(label, "gid_map", label+" links:")
		links, okLinks := block(label, "links", label+" ipv4 routes:")
		routes4, okRoutes4 := block(label, "ipv4 routes", label+" ipv6 routes:")
		routes6, okRoutes6 := block(label, "ipv6 routes", label+" capability sets:")
		capEnd := ""
		if index+1 < len(labels) {
			capEnd = labels[index+1] + " uid/gid:"
		}
		caps, okCaps := block(label, "capability sets", capEnd)
		wantCaps := setupCaps
		if index >= 2 {
			wantCaps = postCaps
		}
		if !okUIDMap || !okGIDMap || !okLinks || !okRoutes4 || !okRoutes6 || !okCaps ||
			!mapLineValid(uidMap) || !mapLineValid(gidMap) || routes4 != "" || routes6 != "" ||
			caps != wantCaps || !strings.HasPrefix(links, "1: lo: <LOOPBACK") || strings.Contains(links, "\n") {
			return false
		}
		states = append(states, state{
			parentUser: parentUser, parentNet: parentNet, user: userNS, net: netNS,
			uidGID: uidGID, uidMap: uidMap, gidMap: gidMap, links: links,
		})
	}
	for index := 1; index < len(states); index++ {
		if states[index].parentUser != states[0].parentUser || states[index].parentNet != states[0].parentNet ||
			states[index].user != states[0].user || states[index].net != states[0].net ||
			states[index].uidGID != states[0].uidGID || states[index].uidMap != states[0].uidMap || states[index].gidMap != states[0].gidMap {
			return false
		}
	}
	for index := 1; index < len(states); index++ {
		wantUp := expectedLoopback == "up"
		isUp := strings.Contains(states[index].links, "<LOOPBACK,UP,")
		if wantUp != isUp || !wantUp && !strings.Contains(states[index].links, "<LOOPBACK>") {
			return false
		}
	}
	return true
}

func verifierNamespaceID(path, prefix string) (string, error) {
	value, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(value, prefix+":"+"[") || !strings.HasSuffix(value, "]") {
		return "", errors.New("unexpected namespace identity")
	}
	return value, nil
}

func verifierCheckNixConfig(spec verifierCheckSpec) string {
	config := "experimental-features = nix-command flakes\nsubstituters =\nbuild-users-group =\n"
	usesNixCommand := len(spec.argv) >= 2 && spec.argv[0] == "nix" &&
		(spec.argv[1] == "develop" || spec.argv[1] == "build")
	if usesNixCommand {
		config += "require-drop-supplementary-groups = false\n"
	}
	usesNixDevelop := len(spec.argv) >= 2 && spec.argv[0] == "nix" && spec.argv[1] == "develop"
	if usesNixDevelop || spec.id == "candidate-build" || spec.id == "default-flake-build" {
		config += "sandbox = false\n"
	}
	return config
}

func runVerifierCommand(ctx context.Context, sandbox *verifierSandbox, spec verifierCheckSpec) (int, string, int64, int64, string, string, string, string) {
	var expected *verifierCheckSpec
	for _, candidate := range verifierCheckSpecs() {
		if candidate.id == spec.id {
			expected = &candidate
			break
		}
	}
	if expected == nil || !sameStrings(spec.argv, expected.argv) || sandbox == nil || sandbox.goCache == "" ||
		!validSHA256(sandbox.toolchain.ClosureProjectionSHA256) || len(sandbox.closure) == 0 || sandbox.closureProjection == "" {
		return -1, "fixed verifier check or read-only closure identity mismatch", 0, 0, "", "", "", ""
	}
	var err error
	workspace := filepath.Join(sandbox.profile, "check-"+spec.id)
	if err := copySnapshotTree(sandbox.workspace, workspace); err != nil {
		return -1, err.Error(), 0, 0, "", "", "", ""
	}
	defer func() {
		if cleanupErr := removeVerifierScratchTree(workspace); cleanupErr != nil {
			fmt.Printf("step10 verifier workspace cleanup failed: %v\n", cleanupErr)
		}
	}()
	vendorPath := filepath.Join(workspace, "vendor")
	if err := os.RemoveAll(vendorPath); err != nil {
		return -1, err.Error(), 0, 0, "", "", "", ""
	}
	if err := os.Symlink(sandbox.goModulesPath, vendorPath); err != nil {
		return -1, err.Error(), 0, 0, "", "", "", ""
	}
	nixStoreDir := filepath.Join(sandbox.storeScratch, spec.id)
	if err := os.Mkdir(nixStoreDir, 0o755); err != nil {
		return -1, err.Error(), 0, 0, "", "", "", ""
	}
	defer func() {
		if cleanupErr := removeVerifierScratchTree(nixStoreDir); cleanupErr != nil {
			fmt.Printf("step10 verifier store upper cleanup failed: %v\n", cleanupErr)
		}
	}()
	nixStoreWorkDir := filepath.Join(sandbox.storeScratch, spec.id+"-work")
	if err := os.Mkdir(nixStoreWorkDir, 0o700); err != nil {
		return -1, err.Error(), 0, 0, "", "", "", ""
	}
	defer func() {
		if cleanupErr := removeVerifierScratchTree(nixStoreWorkDir); cleanupErr != nil {
			fmt.Printf("step10 verifier store work cleanup failed: %v\n", cleanupErr)
		}
	}()
	lockLayer := filepath.Join(sandbox.profile, "nix-db-"+spec.id)
	if err := copyVerifierNixDatabaseLayer(ctx, sandbox.nixDatabase, lockLayer); err != nil {
		return -1, "prepare isolated Nix database snapshot: " + err.Error(), 0, 0, "", "", "", ""
	}
	defer func() {
		if cleanupErr := removeVerifierScratchTree(lockLayer); cleanupErr != nil {
			fmt.Printf("step10 verifier database cleanup failed: %v\n", cleanupErr)
		}
	}()
	hostUserNS, err := verifierNamespaceID("/proc/self/ns/user", "user")
	if err != nil {
		return -1, "read parent user namespace identity: " + err.Error(), 0, 0, "", "", "", ""
	}
	hostNetNS, err := verifierNamespaceID("/proc/self/ns/net", "net")
	if err != nil {
		return -1, "read parent network namespace identity: " + err.Error(), 0, 0, "", "", "", ""
	}
	args := []string{
		"--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--new-session",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/tmp/go-cache", "--bind", sandbox.goCache, "/tmp/go-cache",
		"--dir", "/home", "--dir", "/home/verifier",
		"--dir", "/etc", "--ro-bind", sandbox.hostsPath, "/etc/hosts",
		"--dir", "/bin", "--symlink", sandbox.toolchain.BashPath, "/bin/sh",
		"--dir", "/usr", "--dir", "/usr/bin", "--ro-bind", sandbox.toolchain.EnvPath, "/usr/bin/env",
		"--dir", "/nix", "--dir", "/nix/store",
		"--dir", "/nix/var", "--dir", "/nix/var/nix", "--bind", lockLayer, "/nix/var/nix/db",
		"--dir", "/workspace", "--bind", workspace, "/workspace",
		"--dir", "/snapshot", "--ro-bind", sandbox.workspace, "/snapshot",
		"--chdir", "/workspace",
		"--setenv", "HOME", "/home/verifier", "--setenv", "TMPDIR", "/tmp",
		"--setenv", "GOCACHE", "/tmp/go-cache", "--setenv", "GOMODCACHE", "/tmp/go-mod-cache",
		"--setenv", "GOPROXY", "off", "--setenv", "GOSUMDB", "off", "--setenv", "GOTOOLCHAIN", "local",
		"--setenv", "WIPD_STEP10_VERIFIER_CHECK", spec.id,
	}
	args = append(args, verifierNetworkNamespaceArgs()...)
	args = append(args,
		"--setenv", "NIX_REMOTE", "local", "--setenv", "NIX_CONFIG", verifierCheckNixConfig(spec),
	)
	args = append(args, verifierNixStoreOverlayArgs(sandbox.closureProjection, nixStoreDir, nixStoreWorkDir)...)
	script := verifierNetworkSetupScript(sandbox.toolchain, hostUserNS, hostNetNS, spec.loopback)
	args = append(args, "--", sandbox.toolchain.BashPath, "-eu", "-c", script, "step10-verifier", sandbox.nixPath)
	args = append(args, spec.argv[1:]...)
	command := exec.CommandContext(ctx, sandbox.bwrapPath, args...)
	command.Env = []string{"PATH=" + filepath.Dir(sandbox.nixPath), "HOME=/nonexistent"}
	stdout, stderr := newBoundedOutput(), newBoundedOutput()
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	code := 0
	problem := ""
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code = -1
		}
		problem = err.Error()
	}
	if code == 0 {
		loopbackState := "down"
		if spec.loopback {
			loopbackState = "up"
		}
		if !validVerifierNetworkProof(verifierNetworkProof(stderr.tail), loopbackState) {
			code = 125
			problem = "isolated namespace postflight proof is missing or invalid"
		}
	}
	return code, problem, stdout.size, stderr.size, stdout.digest(), stderr.digest(), string(stdout.tail), string(stderr.tail)
}

func materializeVerifierClosure(paths []string, sources map[string]string, destination string) (string, error) {
	if !sortedUniqueStorePaths(paths) {
		return "", errors.New("verifier closure is empty, unsorted, or contains a non-store path")
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, path := range paths {
		root, err := nixStoreObjectPath(path)
		if err != nil || root != path {
			return "", errors.New("verifier closure contains a non-object store path")
		}
		source := path
		if sources != nil && sources[path] != "" {
			source = sources[path]
		}
		if !filepath.IsAbs(source) || filepath.Clean(source) != source {
			return "", errors.New("verifier closure source is not a canonical absolute path")
		}
		if err = copyVerifierStoreEntry(source, filepath.Join(destination, filepath.Base(path)), filepath.Base(path), digest); err != nil {
			return "", fmt.Errorf("copy pinned store object %s: %w", path, err)
		}
	}
	if err := os.Chmod(destination, 0o555); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func copyVerifierStoreEntry(source, destination, relative string, digest hash.Hash) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, readErr := os.Readlink(source)
		if readErr != nil || target == "" || strings.IndexByte(target, 0) >= 0 {
			return errors.Join(errors.New("invalid symlink in pinned Nix store object"), readErr)
		}
		writeVerifierTreeRecord(digest, 'L', relative, 0, int64(len(target)))
		_, _ = io.WriteString(digest, target)
		_, _ = digest.Write([]byte{0})
		return os.Symlink(target, destination)
	case info.IsDir():
		if info.Mode().Perm()&0o222 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("pinned Nix store directory is writable or has special permissions")
		}
		if err = os.Mkdir(destination, 0o700); err != nil {
			return err
		}
		writeVerifierTreeRecord(digest, 'D', relative, info.Mode().Perm(), 0)
		entries, readErr := os.ReadDir(source)
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			name := entry.Name()
			childRelative := filepath.ToSlash(filepath.Join(relative, name))
			if err = copyVerifierStoreEntry(filepath.Join(source, name), filepath.Join(destination, name), childRelative, digest); err != nil {
				return err
			}
		}
		return os.Chmod(destination, info.Mode().Perm())
	case info.Mode().IsRegular():
		if info.Mode().Perm()&0o222 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("pinned Nix store file is writable or has special permissions")
		}
		input, openErr := os.Open(source)
		if openErr != nil {
			return openErr
		}
		openedInfo, statErr := input.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
			return errors.Join(errors.New("pinned Nix store file changed while opening"), statErr, input.Close())
		}
		output, createErr := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr != nil {
			return errors.Join(createErr, input.Close())
		}
		writeVerifierTreeRecord(digest, 'F', relative, info.Mode().Perm(), info.Size())
		copied, copyErr := io.Copy(io.MultiWriter(output, digest), input)
		if copyErr == nil && copied != info.Size() {
			copyErr = io.ErrUnexpectedEOF
		}
		if copyErr == nil {
			copyErr = output.Chmod(info.Mode().Perm())
		}
		return errors.Join(copyErr, output.Close(), input.Close())
	default:
		return errors.New("pinned Nix store object contains a special filesystem entry")
	}
}

func writeVerifierTreeRecord(digest hash.Hash, kind byte, path string, mode os.FileMode, size int64) {
	_, _ = digest.Write([]byte{kind})
	_, _ = fmt.Fprintf(digest, "%s\x00%04o\x00%d\x00", path, mode, size)
}

func verifierNixStoreOverlayArgs(projection, upper, work string) []string {
	return []string{"--overlay-src", projection, "--overlay", upper, work, "/nix/store"}
}

func copySnapshotTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return ErrInvalidEvidence
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			_, err = io.Copy(output, input)
		}
		closeErr := errors.Join(outputClose(output), input.Close())
		return errors.Join(err, closeErr)
	})
}

func outputClose(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func removeVerifierScratchTree(root string) error {
	root = filepath.Clean(root)
	prefix := filepath.Join(verifierScratchDir, verifierScratchPrefix)
	if !strings.HasPrefix(root, prefix) || root == prefix {
		return ErrInvalidRequest
	}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return nil
	})
	return errors.Join(walkErr, os.RemoveAll(root))
}

func runSnapshotStaticCheck(root string, tree sourceTreeManifest) (int, string) {
	for _, file := range tree.Files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		content, err := os.ReadFile(path)
		if err != nil {
			return 1, "read pinned file: " + file.Path
		}
		if bytes.Contains(content, []byte("\x00")) || !utf8.Valid(content) {
			continue
		}
		if len(content) > 0 && content[len(content)-1] != '\n' {
			return 1, "missing final newline: " + file.Path
		}
		for lineNumber, line := range bytes.Split(content, []byte("\n")) {
			if len(line) > 0 && (line[len(line)-1] == ' ' || line[len(line)-1] == '\t') {
				return 1, fmt.Sprintf("trailing whitespace: %s:%d", file.Path, lineNumber+1)
			}
			if isMergeConflictLine(string(line)) {
				return 1, fmt.Sprintf("merge conflict marker: %s:%d", file.Path, lineNumber+1)
			}
		}
		if strings.HasSuffix(file.Path, ".yaml") || strings.HasSuffix(file.Path, ".yml") {
			if err = yamlDocumentsValid(content); err != nil {
				return 1, "invalid YAML: " + file.Path
			}
		}
	}
	return 0, ""
}

func fileSHA256(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, file)
	closeErr := file.Close()
	if errors.Join(copyErr, closeErr) != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}

func compactStrings(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	result := paths[:1]
	for _, path := range paths[1:] {
		if path != result[len(result)-1] {
			result = append(result, path)
		}
	}
	return result
}

func boundedText(data []byte) string {
	if len(data) > 2048 {
		data = data[len(data)-2048:]
	}
	return strings.TrimSpace(string(data))
}
