package wipdauthority

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

const (
	step10VerifierBaseRevision        = "aa21e333b6000195d867b4f164fdb18159fb8c3b"
	step10VerifierModelCommit         = "7caaf6e07f0b22d778f8431b37b2629a910b9b45"
	step10VerifierWorkplanCommit      = "92d3bd5dbe9d39203f3722ba916c768002a133ae"
	step10VerifierModelPin            = "sha256:77fb90f96d7a43cac041cb18fea8b27345e1b43b5b1b661b21cb04cb4ced8388"
	step10VerifierWorkplanPin         = "sha256:8c4a01850a07a97bd71b19ccbed97853950226d6f82c846fbeea8b5d7ed3ca71"
	step10VerifierInputManifest       = "manifest.json"
	step10VerifierInputModel          = "MODEL.md"
	step10VerifierInputWorkplan       = "step10-workplan.md"
	step10VerifierInputManifestSchema = "wipd.step10-test-inputs/1"
)

//go:embed testdata/step10-adversarial-analysis-fixture.txt
var step10VerifierAdversarialAnalysisFixture []byte

const step10VerifierAdversarialAnalysisFixtureSHA256 = "sha256:fdb89734d33ab2dde1224128b282b741a269a948600db71f9786e5c3ba4b584e"

type step10VerifierInputFilePin struct {
	Commit           string `json:"commit"`
	Path             string `json:"path"`
	CISparseCheckout string `json:"ci_sparse_checkout"`
	SHA256           string `json:"sha256"`
}

type step10VerifierInputManifestData struct {
	Schema                 string                     `json:"schema"`
	Repository             string                     `json:"repository"`
	Preparation            string                     `json:"preparation"`
	CICheckoutAction       string                     `json:"ci_checkout_action"`
	CIFetchDepth           int                        `json:"ci_fetch_depth"`
	CICredentialsPersisted bool                       `json:"ci_credentials_persisted"`
	VerifierNetwork        string                     `json:"verifier_network"`
	Model                  step10VerifierInputFilePin `json:"model"`
	Workplan               step10VerifierInputFilePin `json:"workplan"`
}

type step10VerifierReviewInput struct {
	Schema                  string   `json:"schema"`
	DomainID                string   `json:"domain_id"`
	EnvironmentID           string   `json:"environment_id"`
	CommandID               string   `json:"command_id"`
	RunID                   string   `json:"run_id"`
	MatterID                string   `json:"matter_id"`
	TargetID                string   `json:"target_id"`
	RoleID                  string   `json:"role_id"`
	DispatchID              string   `json:"dispatch_id"`
	ClaimID                 string   `json:"claim_id"`
	ClaimEpoch              uint64   `json:"claim_epoch"`
	CandidateRevision       string   `json:"candidate_revision"`
	CandidateManifestSHA256 string   `json:"candidate_manifest_sha256"`
	ModelSHA256             string   `json:"model_sha256"`
	WorkplanSHA256          string   `json:"workplan_sha256"`
	ModelSections           []string `json:"model_sections"`
	WorkplanSections        []string `json:"workplan_sections"`
	DecisionIDs             []string `json:"decision_ids"`
	AcceptancePhrases       []string `json:"acceptance_phrases"`
	AdversarialAnalysis     string   `json:"adversarial_analysis"`
	Findings                []struct {
		ID         string `json:"id"`
		Finding    string `json:"finding"`
		Resolution string `json:"resolution"`
	} `json:"findings"`
	Conclusion string `json:"conclusion"`
}

type step10VerifierCandidate struct {
	Format string                        `json:"format"`
	Base   string                        `json:"base"`
	Head   string                        `json:"head"`
	Files  []step10VerifierCandidateFile `json:"files"`
}

type step10VerifierCandidateFile struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Executable bool   `json:"executable,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type step10VerifierTreeFile struct {
	Path       string `json:"path"`
	Executable bool   `json:"executable"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

type step10VerifierTreeManifest struct {
	Schema                  string                   `json:"schema"`
	CandidateManifest       string                   `json:"candidate_manifest"`
	CandidateManifestSHA256 string                   `json:"candidate_manifest_sha256"`
	SnapshotSHA256          string                   `json:"snapshot_sha256"`
	Files                   []step10VerifierTreeFile `json:"files"`
}

type step10VerifierFile struct {
	content    []byte
	executable bool
}

var step10ExpectedVerifierChecks = []string{
	"adapter-operation-remote-race", "candidate-build", "changed-package-regressions", "cross-compile",
	"default-flake-build", "focused-step10", "gofumpt", "golangci-lint", "lifecycle-regression",
	"repository-check", "shellcheck", "snapshot-integrity", "static-whitespace-yaml-conflicts",
}

// step10VerifierInputs constructs the exact authenticated test command's
// immutable source inputs from the assigned base and current candidate. It
// reads the checkout only in the outer process acceptance test; the verifier
// receives staged bytes and materializes them into its own isolated scratch.
func step10VerifierInputs(t *testing.T, environment m5ProcessEnvironment, identity operation.SchedulerHookInput,
	commandID, claimID string, claimEpoch uint64, substituteBaseline bool,
) ([]operation.BlobInput, string, string) {
	t.Helper()
	root := step10VerifierRepositoryRoot(t)
	model, workplan, sourceManifest := step10VerifierExternalInputs(t)
	fixtureDigest := step10BlobDigest(step10VerifierAdversarialAnalysisFixture)
	if fixtureDigest != step10VerifierAdversarialAnalysisFixtureSHA256 {
		t.Fatalf("test-only adversarial analysis fixture digest=%s want=%s", fixtureDigest, step10VerifierAdversarialAnalysisFixtureSHA256)
	}
	analysis := string(bytes.TrimSpace(step10VerifierAdversarialAnalysisFixture)) +
		"\n\nPinned external-input provenance (test fixture, not independent review evidence):\n" + string(sourceManifest)

	files, candidateFiles := step10VerifierSnapshotFiles(t, root)
	if substituteBaseline {
		for _, candidateFile := range candidateFiles {
			if candidateFile.Path == "README.md" {
				t.Fatal("README.md is a candidate delta; baseline substitution test would not distinguish an omitted path")
			}
		}
		readme, ok := files["README.md"]
		if !ok {
			t.Fatal("pinned source snapshot omitted baseline README.md")
		}
		readme.content = append(bytes.Clone(readme.content), []byte("\nStep 10 substituted unlisted baseline bytes.\n")...)
		files["README.md"] = readme
	}
	candidate := step10VerifierCandidate{
		Format: "candidate-v1", Base: step10VerifierBaseRevision,
		Head: step10VerifierBaseRevision, Files: candidateFiles,
	}
	candidateBytes := step10CanonicalCandidate(t, candidate)
	candidateManifestDigest := step10BlobDigest(candidateBytes)
	snapshot := step10VerifierTarball(t, files)
	if len(snapshot) > 4<<20 {
		t.Fatalf("immutable compressed verifier snapshot exceeds the authenticated blob bound: %d bytes", len(snapshot))
	}
	tree := step10VerifierTreeManifest{
		Schema: "wipd.step10-source-snapshot/1", CandidateManifest: string(candidateBytes),
		CandidateManifestSHA256: candidateManifestDigest, SnapshotSHA256: step10BlobDigest(snapshot),
	}
	for _, name := range sortedStep10VerifierFileNames(files) {
		file := files[name]
		sum := sha256.Sum256(file.content)
		tree.Files = append(tree.Files, step10VerifierTreeFile{
			Path: name, Executable: file.executable, Size: int64(len(file.content)), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	treeBytes, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("encode immutable source-tree manifest: %v", err)
	}
	treeBytes = append(treeBytes, '\n')
	candidateRevision := "candidate-v1:" + strings.TrimPrefix(candidateManifestDigest, "sha256:")
	modelDigest, workplanDigest := step10BlobDigest(model), step10BlobDigest(workplan)
	review := step10VerifierReviewInput{
		Schema: "wipd.step10-contract-review/2", DomainID: m5TestDomain,
		EnvironmentID: m5TestEnv, CommandID: commandID, RunID: identity.RunID,
		MatterID: identity.MatterID, TargetID: identity.TargetID, RoleID: identity.RoleID,
		DispatchID: identity.DispatchID, ClaimID: claimID, ClaimEpoch: claimEpoch,
		CandidateRevision: candidateRevision, CandidateManifestSHA256: candidateManifestDigest,
		ModelSHA256: modelDigest, WorkplanSHA256: workplanDigest,
		ModelSections: step10MarkdownHeadings(model), WorkplanSections: step10MarkdownHeadings(workplan),
		DecisionIDs: step10ModelDecisionIDs(model), AcceptancePhrases: []string{
			"Authority claim fences and non-CLI Orchestrate/Resume",
			"D85 locks only aid liveness",
			"crash recovery across hook launch/result receipt cannot duplicate effects",
			"unsupported non-idempotent effects refuse before advertisement",
			"integrate role-owned gates/sweep without ordinary CLI control-plane verbs",
		},
		AdversarialAnalysis: analysis, Conclusion: "pass",
		Findings: []struct {
			ID         string `json:"id"`
			Finding    string `json:"finding"`
			Resolution string `json:"resolution"`
		}{
			{ID: "D106", Finding: "A forged success could reuse an outline, stale manifest, skipped clean-state command, writable source, host-network path, or another role's artifact. The code binds all five inputs and the complete source tree to the current verifier command, hashes and modes each file, reruns every fixed check in a fresh network-isolated sandbox, and makes positive evidence contingent on every required exit and one toolchain fingerprint.", Resolution: "The real process acceptance captures all per-check argv, environment, exit code, output hashes, toolchain identity, candidate identity, and review in the retained artifact; authority-side gate validation reopens the staged inputs and artifact by digest before appending gate.closed."},
			{ID: "D107", Finding: "A negative or reaped pass must not authorize closure or turn a retry into a new effect under the same command. The authority checks the exact terminal verifier receipt and complete evidence outcome; Work-unit identity remains original-command-bound, and changed intent conflicts.", Resolution: "The integrated process proves outline-only and invalid-result refusals append no gate event, then closes that role and runs a new full verifier under a distinct role and command before closure; the candidate review records the correction boundary and recovery checks preserve the original command identity."},
			{ID: "D108", Finding: "A Matter-level verdict must not be confused with one Step's completion, reviewed-local, or an unrelated revision. The command targets the exact Matter, and the artifact binds the Matter, candidate manifest, complete source tree, model, Workplan, review, and command identity.", Resolution: "The test closes the Repo-declared matter-scale verified gate only after authority independently validates the full artifact; reviewed-local remains a separate human-owned declaration and no gate outcome substitutes for the other."},
			{ID: "D109", Finding: "An implementation or evidence Matter must not receive a policy exemption from Repo verification. Gate declarations are Repo configuration keyed by Repo and gate; matter scale selects every Matter subject in that Repo rather than a node-local declaration.", Resolution: "The process installs one Repo-level verified declaration at matter scale, proves its subject is the exact Matter, and the authority gate projection uses the same declaration for every Matter without a Step-specific or artifact-specific exemption."},
			{ID: "D110", Finding: "Actor text, a closed/reaped Role, a different Dispatch, or a receipt from another Environment must not close verified. The authority resolves the active Verifier Role at the exact Run and checks Environment, claim epoch, Dispatch, Matter, command receipt, result, and durable evidence before producing a gate event.", Resolution: "The process uses an activated Verifier role and exact current claim/Dispatch; negative results are refused before projection, and the positive gate command causally references the complete verifier command whose evidence is already authority-retained."},
		},
	}
	reviewBytes, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("encode identity-bound Step 10 adversarial review: %v", err)
	}
	reviewBytes = append(reviewBytes, '\n')

	contents := []struct {
		name string
		data []byte
	}{
		{name: "model", data: model},
		{name: "review", data: reviewBytes},
		{name: "snapshot", data: snapshot},
		{name: "tree-manifest", data: treeBytes},
		{name: "workplan", data: workplan},
	}
	blobs := make([]operation.BlobInput, 0, len(contents))
	for _, content := range contents {
		staged := stageM5ProcessBlob(t, environment, content.data)
		if staged.Digest != step10BlobDigest(content.data) || staged.Size != int64(len(content.data)) {
			t.Fatalf("staged %s differs from its independently computed digest/size", content.name)
		}
		blobs = append(blobs, operation.BlobInput{Name: content.name, Digest: staged.Digest, Size: staged.Size})
	}
	t.Logf("D106 request pins candidate %s, manifest %s, snapshot %s, review %s, external-input provenance %s, test-only analysis fixture %s",
		candidateRevision, candidateManifestDigest, step10BlobDigest(snapshot), step10BlobDigest(reviewBytes),
		step10BlobDigest(sourceManifest), fixtureDigest)
	return blobs, candidateRevision, candidateManifestDigest
}

func step10VerifierExternalInputs(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	inputDirectory := os.Getenv("WIPD_STEP10_VERIFIER_INPUTS")
	if !filepath.IsAbs(inputDirectory) {
		t.Fatalf("WIPD_STEP10_VERIFIER_INPUTS must name a prepared absolute input directory; run make prepare-step10-verifier-inputs first")
	}
	info, err := os.Lstat(inputDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("prepared verifier input directory must be a private, non-symlink directory with mode 0700: path=%s info=%v error=%v",
			inputDirectory, info, err)
	}
	entries, err := os.ReadDir(inputDirectory)
	if err != nil {
		t.Fatalf("read prepared verifier input directory %s: %v", inputDirectory, err)
	}
	wantNames := []string{step10VerifierInputModel, step10VerifierInputManifest, step10VerifierInputWorkplan}
	if len(entries) != len(wantNames) {
		t.Fatalf("prepared verifier input directory contains %d entries; want exactly %v", len(entries), wantNames)
	}
	for index, name := range wantNames {
		if entries[index].Name() != name || entries[index].Type()&os.ModeSymlink != 0 {
			t.Fatalf("prepared verifier input entry %d=%q; want regular %q", index, entries[index].Name(), name)
		}
		entryInfo, statErr := os.Lstat(filepath.Join(inputDirectory, name))
		if statErr != nil || !entryInfo.Mode().IsRegular() {
			t.Fatalf("prepared verifier input %q is not a regular file: info=%v error=%v", name, entryInfo, statErr)
		}
	}

	manifest, err := os.ReadFile(filepath.Join(inputDirectory, step10VerifierInputManifest))
	if err != nil {
		t.Fatalf("read prepared verifier source manifest: %v", err)
	}
	expectedManifest := step10VerifierInputManifestData{
		Schema: step10VerifierInputManifestSchema, Repository: "https://github.com/simensen/wip-reboot",
		Preparation: "detached-commit-checkouts-validated", CICredentialsPersisted: false,
		CICheckoutAction: "actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd", CIFetchDepth: 1,
		VerifierNetwork: "denied",
		Model: step10VerifierInputFilePin{
			Commit: step10VerifierModelCommit, Path: "MODEL.md", CISparseCheckout: "MODEL.md",
			SHA256: strings.TrimPrefix(step10VerifierModelPin, "sha256:"),
		},
		Workplan: step10VerifierInputFilePin{
			Commit: step10VerifierWorkplanCommit, Path: "workplans/daemon-operation-parity-continuation.md",
			CISparseCheckout: "workplans/daemon-operation-parity-continuation.md",
			SHA256:           strings.TrimPrefix(step10VerifierWorkplanPin, "sha256:"),
		},
	}
	canonicalManifest, err := json.Marshal(expectedManifest)
	if err != nil {
		t.Fatalf("encode expected prepared verifier source manifest: %v", err)
	}
	canonicalManifest = append(canonicalManifest, '\n')
	if !bytes.Equal(manifest, canonicalManifest) {
		t.Fatalf("prepared verifier source manifest differs from pinned source/config identity: got=%s want=%s", manifest, canonicalManifest)
	}
	model := step10ReadPinnedFile(t, filepath.Join(inputDirectory, step10VerifierInputModel), step10VerifierModelPin)
	workplan := step10ReadPinnedFile(t, filepath.Join(inputDirectory, step10VerifierInputWorkplan), step10VerifierWorkplanPin)
	return model, workplan, manifest
}

func step10VerifierRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve verifier acceptance source location")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	if filepath.Base(root) != "wip" {
		t.Fatalf("unexpected verifier acceptance repository root %q", root)
	}
	return root
}

func step10ReadPinnedFile(t *testing.T, path, expected string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || step10BlobDigest(content) != expected {
		t.Fatalf("pinned input %s digest=%s want=%s error=%v", path, step10BlobDigest(content), expected, err)
	}
	return content
}

func step10VerifierSnapshotFiles(t *testing.T, root string) (map[string]step10VerifierFile, []step10VerifierCandidateFile) {
	t.Helper()
	trackedOutput := step10GitOutput(t, root, "ls-files", "-z")
	tracked := strings.Split(strings.TrimSuffix(string(trackedOutput), "\x00"), "\x00")
	if len(tracked) == 1 && tracked[0] == "" {
		t.Fatal("candidate source snapshot has no tracked files")
	}
	changedOutput := step10GitOutput(t, root, "diff", "--name-only", "-z", "--no-renames", step10VerifierBaseRevision, "--")
	untrackedOutput := step10GitOutput(t, root, "ls-files", "--others", "--exclude-standard", "-z", "--", "internal", "scripts", "docs")
	changed := make(map[string]bool)
	for _, path := range append(strings.Split(strings.TrimSuffix(string(changedOutput), "\x00"), "\x00"),
		strings.Split(strings.TrimSuffix(string(untrackedOutput), "\x00"), "\x00")...) {
		if path != "" {
			changed[path] = true
		}
	}
	if len(changed) == 0 {
		t.Fatal("Step 10 candidate manifest is empty")
	}
	paths := make(map[string]bool, len(tracked)+len(changed))
	for _, path := range tracked {
		if path != "" {
			paths[path] = true
		}
	}
	for path := range changed {
		paths[path] = true
	}
	files := make(map[string]step10VerifierFile, len(paths))
	candidatePaths := make([]string, 0, len(changed))
	for path := range changed {
		candidatePaths = append(candidatePaths, path)
	}
	sort.Strings(candidatePaths)
	candidateFiles := make([]step10VerifierCandidateFile, 0, len(candidatePaths))
	for _, path := range candidatePaths {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if os.IsNotExist(err) {
			candidateFiles = append(candidateFiles, step10VerifierCandidateFile{Path: path, Kind: "deleted"})
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("candidate path %q is not a regular immutable source file: info=%v error=%v", path, info, err)
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("read candidate source %q: %v", path, err)
		}
		executable := info.Mode().Perm()&0o111 != 0
		sum := sha256.Sum256(content)
		candidateFiles = append(candidateFiles, step10VerifierCandidateFile{
			Path: path, Kind: "file", Executable: executable, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	for path := range paths {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if os.IsNotExist(err) {
			if changed[path] {
				continue
			}
			t.Fatalf("tracked source %q disappeared without candidate deletion metadata", path)
		}
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("source snapshot path %q is not regular: info=%v error=%v", path, info, err)
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("read source snapshot file %q: %v", path, err)
		}
		files[path] = step10VerifierFile{content: content, executable: info.Mode().Perm()&0o111 != 0}
	}
	return files, candidateFiles
}

func step10CanonicalCandidate(t *testing.T, candidate step10VerifierCandidate) []byte {
	t.Helper()
	files := make([]map[string]any, 0, len(candidate.Files))
	for _, file := range candidate.Files {
		entry := map[string]any{"kind": file.Kind, "path": file.Path}
		switch file.Kind {
		case "file":
			entry["executable"], entry["size"], entry["sha256"] = file.Executable, file.Size, file.SHA256
		case "deleted":
		default:
			t.Fatalf("unsupported candidate file kind %q", file.Kind)
		}
		files = append(files, entry)
	}
	content, err := json.Marshal(map[string]any{
		"base": candidate.Base, "files": files, "format": candidate.Format, "head": candidate.Head,
	})
	if err != nil {
		t.Fatalf("canonicalize candidate manifest: %v", err)
	}
	return append(content, '\n')
}

func step10VerifierTarball(t *testing.T, files map[string]step10VerifierFile) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	gzipWriter.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, path := range sortedStep10VerifierFileNames(files) {
		file := files[path]
		mode := int64(0o644)
		if file.executable {
			mode = 0o755
		}
		header := &tar.Header{
			Name: path, Mode: mode, Size: int64(len(file.content)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0).UTC(), Uid: 0, Gid: 0, Format: tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write pinned snapshot header %q: %v", path, err)
		}
		if _, err := tarWriter.Write(file.content); err != nil {
			t.Fatalf("write pinned snapshot file %q: %v", path, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close pinned snapshot tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close pinned snapshot gzip: %v", err)
	}
	return compressed.Bytes()
}

func sortedStep10VerifierFileNames(files map[string]step10VerifierFile) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func step10GitOutput(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
	return output
}

func step10BlobDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var (
	step10HeadingPattern     = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)
	step10DecisionRowPattern = regexp.MustCompile(`(?m)^\|\s*D([0-9]+)\s*\|`)
)

func step10MarkdownHeadings(content []byte) []string {
	matches := step10HeadingPattern.FindAllSubmatch(content, -1)
	sections := make([]string, len(matches))
	for index, match := range matches {
		sections[index] = strings.TrimSpace(string(match[1]))
	}
	return sections
}

func step10ModelDecisionIDs(content []byte) []string {
	matches := step10DecisionRowPattern.FindAllSubmatch(content, -1)
	seen := make(map[string]bool, len(matches))
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		id := "D" + string(match[1])
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

type step10VerifierCheckEvidence struct {
	ID              string   `json:"id"`
	Required        bool     `json:"required"`
	Argv            []string `json:"argv"`
	Environment     []string `json:"environment"`
	CleanStateID    string   `json:"clean_state_id"`
	ToolchainSHA256 string   `json:"toolchain_sha256"`
	ExitCode        int      `json:"exit_code"`
	Skipped         bool     `json:"skipped"`
	Problem         string   `json:"problem"`
	StdoutTail      string   `json:"stdout_tail"`
	StderrTail      string   `json:"stderr_tail"`
}

type step10VerifierEvidence struct {
	Schema                  string `json:"schema"`
	CheckSet                string `json:"check_set"`
	DomainID                string `json:"domain_id"`
	EnvironmentID           string `json:"environment_id"`
	RunID                   string `json:"run_id"`
	MatterID                string `json:"matter_id"`
	TargetID                string `json:"target_id"`
	VerifierRoleID          string `json:"verifier_role_id"`
	ClaimID                 string `json:"claim_id"`
	ClaimEpoch              uint64 `json:"claim_epoch"`
	DispatchID              string `json:"dispatch_id"`
	OriginalCommandID       string `json:"original_command_id"`
	RequestHash             string `json:"request_hash"`
	InputDigest             string `json:"input_digest"`
	CandidateRevision       string `json:"candidate_revision"`
	CandidateManifestSHA256 string `json:"candidate_manifest_sha256"`
	SourceTreeSHA256        string `json:"source_tree_sha256"`
	SnapshotSHA256          string `json:"snapshot_sha256"`
	SourceRevision          struct {
		ProtocolRevision        string `json:"protocol_revision"`
		BaseRevision            string `json:"base_revision"`
		CandidateRevision       string `json:"candidate_revision"`
		CandidateManifestSHA256 string `json:"candidate_manifest_sha256"`
		CompleteTreeSHA256      string `json:"complete_tree_sha256"`
		SnapshotSHA256          string `json:"snapshot_sha256"`
	} `json:"source_revision"`
	ModelSHA256    string `json:"model_sha256"`
	ReviewSHA256   string `json:"review_sha256"`
	WorkplanSHA256 string `json:"workplan_sha256"`
	NetworkMode    string `json:"network_mode"`
	Toolchain      struct {
		BootstrapNetworkState   string   `json:"bootstrap_network_state"`
		NixPath                 string   `json:"nix_path"`
		NixSHA256               string   `json:"nix_sha256"`
		BubblewrapPath          string   `json:"bubblewrap_path"`
		BubblewrapSHA256        string   `json:"bubblewrap_sha256"`
		EnvPath                 string   `json:"env_path"`
		EnvSHA256               string   `json:"env_sha256"`
		GitPath                 string   `json:"git_path"`
		GitSHA256               string   `json:"git_sha256"`
		BashPath                string   `json:"bash_path"`
		BashSHA256              string   `json:"bash_sha256"`
		ShasumPath              string   `json:"shasum_path"`
		ShasumSHA256            string   `json:"shasum_sha256"`
		IPPath                  string   `json:"ip_path"`
		IPSHA256                string   `json:"ip_sha256"`
		CapshPath               string   `json:"capsh_path"`
		CapshSHA256             string   `json:"capsh_sha256"`
		HostsSHA256             string   `json:"hosts_sha256"`
		GoDerivationPath        string   `json:"go_derivation_path"`
		GoModulesPath           string   `json:"go_modules_path"`
		GoPath                  string   `json:"go_path"`
		GoSHA256                string   `json:"go_sha256"`
		GoVersion               string   `json:"go_version"`
		ClosureProjectionSHA256 string   `json:"closure_projection_sha256"`
		Closure                 []string `json:"closure"`
	} `json:"toolchain"`
	Checks  []step10VerifierCheckEvidence `json:"checks"`
	Outcome string                        `json:"outcome"`
}

func step10VerifierLedgerSnapshot(t *testing.T, profileRoot, commandID string) (string, map[string][]byte, []byte) {
	t.Helper()
	db, err := step10HookLedgerDB(profileRoot)
	if err != nil {
		t.Fatalf("open disposable verifier ledger: %v", err)
	}
	defer func() { _ = db.Close() }()
	var inputDigest string
	if err = db.QueryRow(`SELECT input_digest FROM hook_engagements WHERE domain_id=? AND command_id=? AND state='completed'`,
		m5TestDomain, commandID).Scan(&inputDigest); err != nil {
		t.Fatalf("read completed original Verifier engagement: %v", err)
	}
	rows, err := db.Query(`SELECT check_id,record FROM hook_check_runs WHERE domain_id=? AND command_id=? AND input_digest=? ORDER BY check_id`,
		m5TestDomain, commandID, inputDigest)
	if err != nil {
		t.Fatalf("read durable clean-state records: %v", err)
	}
	checks := make(map[string][]byte)
	for rows.Next() {
		var id string
		var record []byte
		if err = rows.Scan(&id, &record); err != nil {
			_ = rows.Close()
			t.Fatalf("scan durable clean-state record: %v", err)
		}
		if _, duplicate := checks[id]; duplicate {
			_ = rows.Close()
			t.Fatalf("duplicate clean-state record %q for one original command", id)
		}
		checks[id] = bytes.Clone(record)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate durable clean-state records: %v", err)
	}
	if err = rows.Close(); err != nil {
		t.Fatalf("close durable clean-state records: %v", err)
	}
	evidenceRows, err := db.Query(`SELECT bytes FROM hook_evidence WHERE domain_id=? ORDER BY digest`, m5TestDomain)
	if err != nil {
		t.Fatalf("read durable adapter evidence: %v", err)
	}
	var artifact []byte
	for evidenceRows.Next() {
		var candidate []byte
		var identity struct {
			OriginalCommandID string `json:"original_command_id"`
		}
		if err = evidenceRows.Scan(&candidate); err != nil {
			_ = evidenceRows.Close()
			t.Fatalf("scan durable adapter evidence: %v", err)
		}
		if json.Unmarshal(candidate, &identity) == nil && identity.OriginalCommandID == commandID {
			if artifact != nil {
				_ = evidenceRows.Close()
				t.Fatalf("multiple artifacts claim original Verifier command %s", commandID)
			}
			artifact = bytes.Clone(candidate)
		}
	}
	if err = evidenceRows.Err(); err != nil {
		_ = evidenceRows.Close()
		t.Fatalf("iterate durable adapter evidence: %v", err)
	}
	if err = evidenceRows.Close(); err != nil {
		t.Fatalf("close durable adapter evidence: %v", err)
	}
	if len(artifact) == 0 {
		t.Fatalf("no durable verifier evidence is bound to original command %s", commandID)
	}
	return inputDigest, checks, artifact
}

func validateStep10VerifierArtifact(t *testing.T, raw []byte, input operation.SchedulerHookInput, commandID,
	requestHash, inputDigest, candidateRevision, candidateManifestDigest string, blobs []operation.BlobInput,
) {
	t.Helper()
	var artifact step10VerifierEvidence
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("decode retained D106 evidence: %v", err)
	}
	if artifact.Schema != "wipd.step10-verification-evidence/5" || artifact.CheckSet != "m6-step-10-clean-state/4" ||
		artifact.DomainID != m5TestDomain || artifact.EnvironmentID != m5TestEnv || artifact.RunID != input.RunID ||
		artifact.MatterID != input.MatterID || artifact.TargetID != input.TargetID || artifact.VerifierRoleID != input.RoleID ||
		artifact.ClaimID != input.TargetClaims[0].ClaimID || artifact.ClaimEpoch != input.TargetClaims[0].ClaimEpoch ||
		artifact.DispatchID != input.DispatchID || artifact.OriginalCommandID != commandID || artifact.RequestHash != requestHash ||
		artifact.InputDigest != inputDigest || artifact.CandidateRevision != candidateRevision ||
		artifact.CandidateManifestSHA256 != candidateManifestDigest ||
		artifact.SourceRevision.ProtocolRevision != operation.SchedulerStep10VerifierRevision ||
		artifact.SourceRevision.BaseRevision != step10VerifierBaseRevision ||
		artifact.SourceRevision.CandidateRevision != candidateRevision ||
		artifact.SourceRevision.CandidateManifestSHA256 != candidateManifestDigest ||
		artifact.SourceRevision.CompleteTreeSHA256 != artifact.SourceTreeSHA256 ||
		artifact.SourceRevision.SnapshotSHA256 != artifact.SnapshotSHA256 ||
		artifact.NetworkMode != "bubblewrap-user,pid,ipc,uts,network namespaces; namespace loopback is explicitly down for bootstrap and non-loopback checks and up only for five allowlisted process checks; setup CAP_NET_ADMIN is cleared from effective, permitted, inheritable, and ambient sets before every command; only the isolated bounding set retains it, with no_new_privs preventing regain; no host interfaces or external routes" ||
		len(artifact.Checks) != len(step10ExpectedVerifierChecks) || !validSHA256Test(artifact.Toolchain.GoSHA256) ||
		!validSHA256Test(artifact.Toolchain.ClosureProjectionSHA256) {
		t.Fatalf("retained D106 artifact is not a positive result for this exact authenticated command and candidate: %+v", artifact)
	}
	inputDigests := make(map[string]string, len(blobs))
	for _, blob := range blobs {
		inputDigests[blob.Name] = blob.Digest
	}
	if artifact.ModelSHA256 != inputDigests["model"] || artifact.ReviewSHA256 != inputDigests["review"] ||
		artifact.SnapshotSHA256 != inputDigests["snapshot"] || artifact.SourceTreeSHA256 != inputDigests["tree-manifest"] ||
		artifact.WorkplanSHA256 != inputDigests["workplan"] {
		t.Fatalf("retained D106 evidence substitutes one or more staged input digests: %+v inputs=%+v", artifact, inputDigests)
	}
	seenCleanStates := make(map[string]bool, len(artifact.Checks))
	for index, check := range artifact.Checks {
		if check.ID != step10ExpectedVerifierChecks[index] || check.CleanStateID == "" || seenCleanStates[check.CleanStateID] ||
			check.ToolchainSHA256 == "" || check.Required && (check.Skipped || check.ExitCode != 0) {
			t.Fatalf("clean-state check %d is missing, substituted, skipped, failed, or shares state: %+v stdout=%q stderr=%q",
				index, check, check.StdoutTail, check.StderrTail)
		}
		seenCleanStates[check.CleanStateID] = true
		// Each check binds its exact environment, so the NIX_CONFIG-scoped fingerprints
		// intentionally differ while still sharing the same toolchain identity.
		if check.ToolchainSHA256 != step10VerifierCheckToolchainSHA256(artifact, check.Environment) {
			t.Fatalf("check %q toolchain fingerprint is not bound to its exact environment", check.ID)
		}
		config, found := "", false
		for _, value := range check.Environment {
			if strings.HasPrefix(value, "NIX_CONFIG=") {
				if found {
					t.Fatalf("check %q recorded duplicate NIX_CONFIG values", check.ID)
				}
				config, found = strings.TrimPrefix(value, "NIX_CONFIG="), true
			}
		}
		wantConfig := "experimental-features = nix-command flakes\nsubstituters =\nbuild-users-group =\n"
		if len(check.Argv) >= 2 && check.Argv[0] == "nix" &&
			(check.Argv[1] == "develop" || check.Argv[1] == "build") {
			wantConfig += "require-drop-supplementary-groups = false\n"
		}
		usesNixDevelop := len(check.Argv) >= 2 && check.Argv[0] == "nix" && check.Argv[1] == "develop"
		if usesNixDevelop || check.ID == "candidate-build" || check.ID == "default-flake-build" {
			wantConfig += "sandbox = false\n"
		}
		if !found || config != wantConfig {
			t.Fatalf("check %q recorded NIX_CONFIG %q, want %q", check.ID, config, wantConfig)
		}
	}
	if artifact.Outcome != "verified" {
		t.Fatalf("retained D106 evidence did not establish a positive result: outcome=%q", artifact.Outcome)
	}
}

func step10VerifierCheckToolchainSHA256(artifact step10VerifierEvidence, environment []string) string {
	toolchainJSON, err := json.Marshal(artifact.Toolchain)
	if err != nil {
		return ""
	}
	toolchainDigest := sha256.Sum256(toolchainJSON)
	identity := struct {
		ToolchainSHA256 string   `json:"toolchain_sha256"`
		Environment     []string `json:"environment"`
	}{ToolchainSHA256: hex.EncodeToString(toolchainDigest[:]), Environment: append([]string(nil), environment...)}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validSHA256Test(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func step10PersistVerifierArtifact(t *testing.T, candidateRevision string, artifact []byte) string {
	t.Helper()
	root := step10VerifierRepositoryRoot(t)
	directory := filepath.Join(root, ".amp/in/artifacts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("prepare ignored verifier evidence artifact directory: %v", err)
	}
	evidenceSum := sha256.Sum256(artifact)
	name := "m6-step-10-completion-05-verifier-" + strings.TrimPrefix(candidateRevision, "candidate-v1:") +
		"-evidence-" + hex.EncodeToString(evidenceSum[:]) + ".json"
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		prior, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(prior, artifact) {
			t.Fatalf("existing completion evidence %s differs from this exact retained artifact: %v", path, readErr)
		}
		return path
	}
	if err != nil {
		t.Fatalf("create durable local verifier evidence artifact: %v", err)
	}
	if _, err = file.Write(artifact); err != nil {
		_ = file.Close()
		t.Fatalf("write durable local verifier evidence artifact: %v", err)
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("sync durable local verifier evidence artifact: %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("close durable local verifier evidence artifact: %v", err)
	}
	return path
}

func killStep10WipdProcess(t *testing.T, binary, profileRoot string) error {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	var pid int
	for _, entry := range entries {
		candidatePID, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || !entry.IsDir() {
			continue
		}
		commandLine, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if readErr != nil {
			continue
		}
		args := strings.Split(strings.TrimSuffix(string(commandLine), "\x00"), "\x00")
		if len(args) == 3 && args[0] == binary && args[1] == "--profile-root" && args[2] == profileRoot {
			if pid != 0 {
				return fmt.Errorf("multiple wipd processes match disposable profile %s", profileRoot)
			}
			pid = candidatePID
		}
	}
	if pid == 0 {
		return fmt.Errorf("no wipd child process matches binary %s and disposable profile %s", binary, profileRoot)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func step10AuthorityEvidencePosition(t *testing.T, authorityPath, commandID, digest string) (uint64, uint64) {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatalf("open disposable authority evidence: %v", err)
	}
	defer func() { _ = db.Close() }()
	var evidencePosition, completionPosition uint64
	err = db.QueryRow(`SELECT r.first_position,e.position FROM blob_references r
		JOIN authority_events e ON e.domain_id=r.domain_id AND e.command_id=?
		WHERE r.domain_id=? AND r.digest=?`, commandID, m5TestDomain, digest).
		Scan(&evidencePosition, &completionPosition)
	if err != nil {
		t.Fatalf("read authority-retained Verifier artifact at its completion event: %v", err)
	}
	return evidencePosition, completionPosition
}
