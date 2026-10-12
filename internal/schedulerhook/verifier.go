package schedulerhook

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/procrastivity/wip/internal/operation"
	"gopkg.in/yaml.v3"
)

const (
	step10VerifierCheckSet       = "m6-step-10-clean-state/4"
	step10VerifierBase           = "aa21e333b6000195d867b4f164fdb18159fb8c3b"
	step10VerifierBaseTreeSHA256 = "2048c7caefc9db94d4cebaf2640aa54bca5e45339b17e06b1620b15aa3e9e25a"
	step10VerifierModelSHA256    = "sha256:77fb90f96d7a43cac041cb18fea8b27345e1b43b5b1b661b21cb04cb4ced8388"
	step10VerifierPlanSHA256     = "sha256:8c4a01850a07a97bd71b19ccbed97853950226d6f82c846fbeea8b5d7ed3ca71"
	step10VerifierFlakeSHA256    = "073c3f869b9c5253d3192f7ba3ab0831a66586d64001f5ab17b6fa0148848207"
	step10VerifierLockSHA256     = "c2b6e06a956dba6fd13a924da802a8923cf731fdd7caf0e37e6a5f281460a51e"
	step10VerifierNetworkMode    = "bubblewrap-user,pid,ipc,uts,network namespaces; namespace loopback is explicitly down for bootstrap and non-loopback checks and up only for five allowlisted process checks; setup CAP_NET_ADMIN is cleared from effective, permitted, inheritable, and ambient sets before every command; only the isolated bounding set retains it, with no_new_privs preventing regain; no host interfaces or external routes"
)

//go:embed verifier-base-tree.json
var step10VerifierBaseTreeBytes []byte

var (
	decisionRowPattern = regexp.MustCompile(`(?m)^\|\s*D([0-9]+)\s*\|`)
	headingPattern     = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)
	conflictLine       = regexp.MustCompile(`^(<<<<<<< |=======|>>>>>>> |\|\|\|\|\|\|\| )`)
)

var step10AcceptancePhrases = []string{
	"Authority claim fences and non-CLI Orchestrate/Resume",
	"D85 locks only aid liveness",
	"crash recovery across hook launch/result receipt cannot duplicate effects",
	"unsupported non-idempotent effects refuse before advertisement",
	"integrate role-owned gates/sweep without ordinary CLI control-plane verbs",
}

type candidateManifest struct {
	Format string           `json:"format"`
	Base   string           `json:"base"`
	Head   string           `json:"head"`
	Files  []candidateEntry `json:"files"`
}

type candidateEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Executable bool   `json:"executable,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type sourceTreeManifest struct {
	Schema                  string       `json:"schema"`
	CandidateManifest       string       `json:"candidate_manifest"`
	CandidateManifestSHA256 string       `json:"candidate_manifest_sha256"`
	SnapshotSHA256          string       `json:"snapshot_sha256"`
	Files                   []sourceFile `json:"files"`
}

type sourceFile struct {
	Path       string `json:"path"`
	Executable bool   `json:"executable"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

type verifierBaseTree struct {
	Schema string       `json:"schema"`
	Base   string       `json:"base"`
	Files  []sourceFile `json:"files"`
}

// SelectedSourceRevision distinguishes the source commit, candidate contents,
// complete tree, and adapter protocol. It is derived only after the full tree
// has been checked against the embedded authority-selected base tree.
type SelectedSourceRevision struct {
	ProtocolRevision        string `cbor:"protocol_revision" json:"protocol_revision"`
	BaseRevision            string `cbor:"base_revision" json:"base_revision"`
	CandidateRevision       string `cbor:"candidate_revision" json:"candidate_revision"`
	CandidateManifestSHA256 string `cbor:"candidate_manifest_sha256" json:"candidate_manifest_sha256"`
	CompleteTreeSHA256      string `cbor:"complete_tree_sha256" json:"complete_tree_sha256"`
	SnapshotSHA256          string `cbor:"snapshot_sha256" json:"snapshot_sha256"`
}

type verifierReview struct {
	Schema                  string          `json:"schema"`
	DomainID                string          `json:"domain_id"`
	EnvironmentID           string          `json:"environment_id"`
	CommandID               string          `json:"command_id"`
	RunID                   string          `json:"run_id"`
	MatterID                string          `json:"matter_id"`
	TargetID                string          `json:"target_id"`
	RoleID                  string          `json:"role_id"`
	DispatchID              string          `json:"dispatch_id"`
	ClaimID                 string          `json:"claim_id"`
	ClaimEpoch              uint64          `json:"claim_epoch"`
	CandidateRevision       string          `json:"candidate_revision"`
	CandidateManifestSHA256 string          `json:"candidate_manifest_sha256"`
	ModelSHA256             string          `json:"model_sha256"`
	WorkplanSHA256          string          `json:"workplan_sha256"`
	ModelSections           []string        `json:"model_sections"`
	WorkplanSections        []string        `json:"workplan_sections"`
	DecisionIDs             []string        `json:"decision_ids"`
	AcceptancePhrases       []string        `json:"acceptance_phrases"`
	AdversarialAnalysis     string          `json:"adversarial_analysis"`
	Findings                []reviewFinding `json:"findings"`
	Conclusion              string          `json:"conclusion"`
}

type reviewFinding struct {
	ID         string `json:"id"`
	Finding    string `json:"finding"`
	Resolution string `json:"resolution"`
}

type verifierToolchain struct {
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
}

type verifierCheck struct {
	ID              string   `json:"id"`
	CheckSet        string   `json:"check_set"`
	Required        bool     `json:"required"`
	Diagnostic      bool     `json:"diagnostic,omitempty"`
	Argv            []string `json:"argv"`
	Environment     []string `json:"environment"`
	CleanStateID    string   `json:"clean_state_id"`
	ToolchainSHA256 string   `json:"toolchain_sha256"`
	StartedAt       string   `json:"started_at"`
	DurationNanos   int64    `json:"duration_nanos"`
	ExitCode        int      `json:"exit_code"`
	Skipped         bool     `json:"skipped"`
	StdoutBytes     int64    `json:"stdout_bytes"`
	StderrBytes     int64    `json:"stderr_bytes"`
	StdoutSHA256    string   `json:"stdout_sha256"`
	StderrSHA256    string   `json:"stderr_sha256"`
	StdoutTail      string   `json:"stdout_tail"`
	StderrTail      string   `json:"stderr_tail"`
	Problem         string   `json:"problem,omitempty"`
}

func validStep10VerifierInputs(inputs []PinnedBlob) bool {
	if len(inputs) != 5 {
		return false
	}
	for index, name := range []string{"model", "review", "snapshot", "tree-manifest", "workplan"} {
		input := inputs[index]
		if input.Name != name || !digestPattern.MatchString(input.Digest) || input.Size < 0 ||
			input.Size > maxVerifierBlobBytes || int64(len(input.Bytes)) != input.Size || blobDigest(input.Bytes) != input.Digest {
			return false
		}
		if name != "snapshot" && (!utf8.Valid(input.Bytes) || bytes.ContainsRune(input.Bytes, '\x00')) {
			return false
		}
	}
	return true
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func validateStep10Review(request Request, domainID string) (verifierReview, candidateManifest, sourceTreeManifest, error) {
	var review verifierReview
	var candidate candidateManifest
	var tree sourceTreeManifest
	if !validStep10VerifierInputs(request.Inputs) {
		fmt.Printf("step10 verifier input tuple invalid: count=%d\n", len(request.Inputs))
		return review, candidate, tree, ErrInvalidEvidence
	}
	input := func(name string) PinnedBlob {
		for _, blob := range request.Inputs {
			if blob.Name == name {
				return blob
			}
		}
		return PinnedBlob{}
	}
	model, workplan := input("model"), input("workplan")
	if model.Digest != step10VerifierModelSHA256 || workplan.Digest != step10VerifierPlanSHA256 ||
		!bytes.Contains(model.Bytes, []byte("| D106 |")) || !bytes.Contains(model.Bytes, []byte("| D110 |")) ||
		!bytes.Contains(workplan.Bytes, []byte("integrate role-owned gates/sweep without ordinary CLI control-plane verbs")) {
		fmt.Printf("step10 verifier pinned model/workplan mismatch: model=%s workplan=%s\n", model.Digest, workplan.Digest)
		return review, candidate, tree, ErrInvalidEvidence
	}
	if err := decodeStrictJSON(input("tree-manifest").Bytes, &tree); err != nil || tree.Schema != "wipd.step10-source-snapshot/1" ||
		!digestPattern.MatchString(tree.CandidateManifestSHA256) || !digestPattern.MatchString(tree.SnapshotSHA256) ||
		tree.CandidateManifestSHA256 != blobDigest([]byte(tree.CandidateManifest)) ||
		tree.SnapshotSHA256 != input("snapshot").Digest {
		fmt.Printf("step10 verifier tree manifest invalid: decode=%v schema=%q candidate=%s snapshot=%s input=%s\n", err, tree.Schema, tree.CandidateManifestSHA256, tree.SnapshotSHA256, input("snapshot").Digest)
		return review, candidate, tree, ErrInvalidEvidence
	}
	candidateBytes := []byte(tree.CandidateManifest)
	if err := decodeStrictJSON(candidateBytes, &candidate); err != nil || candidate.Format != "candidate-v1" ||
		candidate.Base != step10VerifierBase || candidate.Head != step10VerifierBase ||
		request.Revision != operation.SchedulerStep10VerifierRevision {
		fmt.Printf("step10 verifier candidate invalid: decode=%v format=%q base=%q head=%q revision=%q\n", err, candidate.Format, candidate.Base, candidate.Head, request.Revision)
		return review, candidate, tree, ErrInvalidEvidence
	}
	candidateRevision := "candidate-v1:" + strings.TrimPrefix(tree.CandidateManifestSHA256, "sha256:")
	canonical, err := canonicalCandidateManifest(candidate)
	if err != nil || !bytes.Equal(canonical, candidateBytes) {
		return review, candidate, tree, ErrInvalidEvidence
	}
	if err = validateSourceTreeManifest(candidate, tree); err != nil {
		fmt.Printf("step10 verifier source tree invalid: %v\n", err)
		return review, candidate, tree, err
	}
	if sourceFileSHA256(tree, "flake.nix") != step10VerifierFlakeSHA256 ||
		sourceFileSHA256(tree, "flake.lock") != step10VerifierLockSHA256 {
		fmt.Printf("step10 verifier flake pin mismatch: flake=%q lock=%q\n", sourceFileSHA256(tree, "flake.nix"), sourceFileSHA256(tree, "flake.lock"))
		return review, candidate, tree, ErrInvalidEvidence
	}
	if err = decodeStrictJSON(input("review").Bytes, &review); err != nil || review.Schema != "wipd.step10-contract-review/2" ||
		review.DomainID != domainID || review.EnvironmentID != request.EnvironmentID || review.CommandID != request.CommandID ||
		review.RunID != request.RunID || review.MatterID != request.MatterID ||
		review.TargetID != request.TargetID || review.RoleID != request.RoleID || review.DispatchID != request.DispatchID ||
		request.Claim == nil || review.ClaimID != request.Claim.ID || review.ClaimEpoch != request.Claim.Epoch ||
		review.CandidateRevision != candidateRevision || review.CandidateManifestSHA256 != tree.CandidateManifestSHA256 ||
		review.ModelSHA256 != step10VerifierModelSHA256 || review.WorkplanSHA256 != step10VerifierPlanSHA256 || review.Conclusion != "pass" ||
		len(strings.TrimSpace(review.AdversarialAnalysis)) < 512 || placeholderText(review.AdversarialAnalysis) ||
		!sameStrings(review.ModelSections, markdownHeadings(model.Bytes)) ||
		!sameStrings(review.WorkplanSections, markdownHeadings(workplan.Bytes)) ||
		!sameStrings(review.DecisionIDs, decisionIDs(model.Bytes)) ||
		!sameStrings(review.AcceptancePhrases, step10AcceptancePhrases) {
		fmt.Printf("step10 verifier review mismatch: decode=%v schema=%q domain=%q environment=%q command=%q run=%q matter=%q target=%q role=%q dispatch=%q claim=%+v revision=%q manifest=%q model=%q workplan=%q conclusion=%q analysis=%d placeholder=%t model_sections=%t plan_sections=%t decisions=%t phrases=%t\n",
			err, review.Schema, review.DomainID, review.EnvironmentID, review.CommandID, review.RunID, review.MatterID, review.TargetID, review.RoleID, review.DispatchID,
			review.ClaimID, review.CandidateRevision, review.CandidateManifestSHA256, review.ModelSHA256, review.WorkplanSHA256, review.Conclusion,
			len(strings.TrimSpace(review.AdversarialAnalysis)), placeholderText(review.AdversarialAnalysis),
			sameStrings(review.ModelSections, markdownHeadings(model.Bytes)), sameStrings(review.WorkplanSections, markdownHeadings(workplan.Bytes)),
			sameStrings(review.DecisionIDs, decisionIDs(model.Bytes)), sameStrings(review.AcceptancePhrases, step10AcceptancePhrases))
		return review, candidate, tree, ErrInvalidEvidence
	}
	for _, requiredDecision := range []string{"D106", "D107", "D108", "D109", "D110"} {
		if !containsString(review.DecisionIDs, requiredDecision) {
			return review, candidate, tree, ErrInvalidEvidence
		}
	}
	findingIDs := make(map[string]bool, len(review.Findings))
	for _, finding := range review.Findings {
		if strings.TrimSpace(finding.ID) == "" || strings.TrimSpace(finding.Finding) == "" ||
			strings.TrimSpace(finding.Resolution) == "" || placeholderText(finding.Finding) || placeholderText(finding.Resolution) {
			return review, candidate, tree, ErrInvalidEvidence
		}
		if findingIDs[finding.ID] {
			return review, candidate, tree, ErrInvalidEvidence
		}
		findingIDs[finding.ID] = true
	}
	for _, requiredFinding := range []string{"D106", "D107", "D108", "D109", "D110"} {
		if !findingIDs[requiredFinding] {
			return review, candidate, tree, ErrInvalidEvidence
		}
	}
	return review, candidate, tree, nil
}

func canonicalCandidateManifest(candidate candidateManifest) ([]byte, error) {
	files := make([]map[string]any, 0, len(candidate.Files))
	for _, file := range candidate.Files {
		entry := map[string]any{"kind": file.Kind, "path": file.Path}
		switch file.Kind {
		case "file":
			entry["executable"] = file.Executable
			entry["sha256"] = file.SHA256
			entry["size"] = file.Size
		case "symlink":
			entry["sha256"] = file.SHA256
			entry["size"] = file.Size
		}
		files = append(files, entry)
	}
	canonical, err := json.Marshal(map[string]any{
		"base": candidate.Base, "files": files, "format": candidate.Format, "head": candidate.Head,
	})
	if err != nil {
		return nil, err
	}
	return append(canonical, '\n'), nil
}

func validateSourceTreeManifest(candidate candidateManifest, tree sourceTreeManifest) error {
	if len(tree.Files) == 0 || len(tree.Files) > 20000 {
		return ErrInvalidEvidence
	}
	byPath := make(map[string]sourceFile, len(tree.Files))
	previous := ""
	for _, file := range tree.Files {
		if !safeSnapshotPath(file.Path) || previous != "" && previous >= file.Path || file.Size < 0 ||
			!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(file.SHA256) {
			return ErrInvalidEvidence
		}
		previous = file.Path
		byPath[file.Path] = file
	}
	previous = ""
	for _, entry := range candidate.Files {
		if !safeSnapshotPath(entry.Path) || previous != "" && previous >= entry.Path {
			return ErrInvalidEvidence
		}
		previous = entry.Path
		switch entry.Kind {
		case "deleted":
			if _, ok := byPath[entry.Path]; ok || entry.Size != 0 || entry.SHA256 != "" {
				return ErrInvalidEvidence
			}
		case "file":
			file, ok := byPath[entry.Path]
			if !ok || file.Size != entry.Size || file.SHA256 != entry.SHA256 || file.Executable != entry.Executable {
				return ErrInvalidEvidence
			}
		case "symlink":
			return ErrInvalidEvidence
		default:
			return ErrInvalidEvidence
		}
	}
	return validateCompleteSourceTree(candidate, byPath)
}

// SelectStep10SourceRevision verifies and derives the complete immutable
// source identity from the pinned Verifier inputs. Callers must not treat any
// digest supplied inside the request as authoritative without this check.
func SelectStep10SourceRevision(request Request, domainID string) (SelectedSourceRevision, error) {
	_, candidate, tree, err := validateStep10Review(request, domainID)
	if err != nil {
		return SelectedSourceRevision{}, err
	}
	return selectedStep10SourceRevision(request.Revision, candidate, tree, inputBlob(request.Inputs, "tree-manifest").Digest), nil
}

func selectedStep10SourceRevision(protocolRevision string, candidate candidateManifest, tree sourceTreeManifest, completeTreeSHA256 string) SelectedSourceRevision {
	return SelectedSourceRevision{
		ProtocolRevision:        protocolRevision,
		BaseRevision:            candidate.Base,
		CandidateRevision:       "candidate-v1:" + strings.TrimPrefix(tree.CandidateManifestSHA256, "sha256:"),
		CandidateManifestSHA256: tree.CandidateManifestSHA256,
		CompleteTreeSHA256:      completeTreeSHA256,
		SnapshotSHA256:          tree.SnapshotSHA256,
	}
}

func validateCompleteSourceTree(candidate candidateManifest, supplied map[string]sourceFile) error {
	if blobDigest(step10VerifierBaseTreeBytes) != "sha256:"+step10VerifierBaseTreeSHA256 {
		return ErrInvalidEvidence
	}
	var base verifierBaseTree
	if decodeStrictJSON(step10VerifierBaseTreeBytes, &base) != nil || base.Schema != "wipd.step10-authority-base-tree/1" ||
		base.Base != step10VerifierBase || len(base.Files) == 0 || len(base.Files) > 20000 {
		return ErrInvalidEvidence
	}
	expected := make(map[string]sourceFile, len(base.Files)+len(candidate.Files))
	previous := ""
	for _, file := range base.Files {
		if !safeSnapshotPath(file.Path) || previous != "" && previous >= file.Path || file.Size < 0 ||
			!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(file.SHA256) {
			return ErrInvalidEvidence
		}
		previous = file.Path
		expected[file.Path] = file
	}
	for _, entry := range candidate.Files {
		_, exists := expected[entry.Path]
		switch entry.Kind {
		case "deleted":
			if !exists {
				return ErrInvalidEvidence
			}
			delete(expected, entry.Path)
		case "file":
			expected[entry.Path] = sourceFile{
				Path: entry.Path, Executable: entry.Executable, Size: entry.Size, SHA256: entry.SHA256,
			}
		default:
			return ErrInvalidEvidence
		}
	}
	if len(expected) != len(supplied) {
		return ErrInvalidEvidence
	}
	for path, want := range expected {
		if got, ok := supplied[path]; !ok || got != want {
			return ErrInvalidEvidence
		}
	}
	return nil
}

func sourceFileSHA256(tree sourceTreeManifest, path string) string {
	for _, file := range tree.Files {
		if file.Path == path {
			return file.SHA256
		}
	}
	return ""
}

func markdownHeadings(content []byte) []string {
	matches := headingPattern.FindAllSubmatch(content, -1)
	result := make([]string, len(matches))
	for index, match := range matches {
		result[index] = strings.TrimSpace(string(match[1]))
	}
	return result
}

func decisionIDs(content []byte) []string {
	matches := decisionRowPattern.FindAllSubmatch(content, -1)
	seen := make(map[string]bool, len(matches))
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		id := "D" + string(match[1])
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func safeSnapshotPath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return false
		}
	}
	return true
}

func placeholderText(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"todo", "tbd", "placeholder", "not reviewed", "unreviewed", "template text"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func yamlDocumentsValid(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var document any
		err := decoder.Decode(&document)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func isMergeConflictLine(line string) bool { return conflictLine.MatchString(line) }
