package schedulerhook

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerifierNixStoreBase32MatchesLockedFlakeHashes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "nixpkgs",
			raw:  "d7a2a481f758aa369e44605a62c36b84f45110db34ab3910554a28347b68cb6c",
			want: "0v6bd1xk8a2aal83karlvc853x44dg1n4nk08jg3dajqyy0s98np",
		},
		{
			name: "flake-utils",
			raw:  "9742858391e3aec7ec3bf2691beafb7d1aea9b5da4cc51f2507a87095a4c31b2",
			want: "1cii9id0k1vsa3r53k54bndyl6kxzgm1nsgj7gncgbp3j61qahlp",
		},
		{
			name: "systems",
			raw:  "572d6bab901a46e2f33b172df27cf84fac25832511ef32d4df4f64f66042efaf",
			want: "1bzg89hgcr2gvza35vqi4n1jbb2gz1yg4b8p7gry4ihsj2mnnbap",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := hex.DecodeString(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := nixStoreBase32(raw); got != test.want {
				t.Fatalf("Nix base32 hash = %q, want %q", got, test.want)
			}
		})
	}
}

func TestVerifierEnablesLoopbackOnlyForAllowlistedProcessChecks(t *testing.T) {
	want := map[string]bool{
		"focused-step10": true, "lifecycle-regression": true, "adapter-operation-remote-race": true,
		"changed-package-regressions": true, "repository-check": true,
		"candidate-build": false, "default-flake-build": false, "cross-compile": false,
		"snapshot-integrity": false, "gofumpt": false, "static-whitespace-yaml-conflicts": false,
		"shellcheck": false, "golangci-lint": false,
	}
	for _, spec := range verifierCheckSpecs() {
		loopback, ok := want[spec.id]
		if !ok || loopback != spec.loopback {
			t.Fatalf("check %q loopback setting = %t; want fixed allowlist value %t", spec.id, spec.loopback, loopback)
		}
		delete(want, spec.id)
	}
	if len(want) != 0 {
		t.Fatalf("loopback policy omitted fixed checks: %v", want)
	}
}

func TestVerifierNixConfigScopesSandboxOverrideToChecksThatNeedNestedNix(t *testing.T) {
	for _, spec := range verifierCheckSpecs() {
		config := verifierCheckNixConfig(spec)
		usesNixCommand := len(spec.argv) >= 2 && spec.argv[0] == "nix" &&
			(spec.argv[1] == "develop" || spec.argv[1] == "build")
		usesNixDevelop := len(spec.argv) >= 2 && spec.argv[0] == "nix" && spec.argv[1] == "develop"
		needsSandboxOverride := usesNixDevelop || spec.id == "candidate-build" || spec.id == "default-flake-build"
		if hasSandboxOverride := strings.Contains(config, "sandbox = false\n"); hasSandboxOverride != needsSandboxOverride {
			t.Errorf("check %q sandbox override = %t; required = %t; config=%q",
				spec.id, hasSandboxOverride, needsSandboxOverride, config)
		}
		if hasGroupOverride := strings.Contains(config, "require-drop-supplementary-groups = false\n"); hasGroupOverride != usesNixCommand {
			t.Errorf("check %q supplementary-group override = %t; Nix command = %t; config=%q",
				spec.id, hasGroupOverride, usesNixCommand, config)
		}
	}
}

func TestSourceTreeManifestBindsEveryPathToPinnedBaseAndCandidateDelta(t *testing.T) {
	var base verifierBaseTree
	if err := decodeStrictJSON(step10VerifierBaseTreeBytes, &base); err != nil ||
		base.Base != step10VerifierBase || len(base.Files) != 732 {
		t.Fatalf("decode authority-selected base tree: files=%d base=%q error=%v", len(base.Files), base.Base, err)
	}
	baseFiles := make(map[string]sourceFile, len(base.Files))
	for _, file := range base.Files {
		baseFiles[file.Path] = file
	}
	candidate := candidateManifest{Format: "candidate-v1", Base: step10VerifierBase, Head: step10VerifierBase}
	if err := validateCompleteSourceTree(candidate, baseFiles); err != nil {
		t.Fatalf("exact pinned base tree refused: %v", err)
	}

	altered := make(map[string]sourceFile, len(baseFiles))
	for path, file := range baseFiles {
		altered[path] = file
	}
	readme := altered["README.md"]
	readme.SHA256 = strings.Repeat("0", 64)
	altered["README.md"] = readme
	if err := validateCompleteSourceTree(candidate, altered); err == nil {
		t.Fatal("accepted changed unlisted baseline README.md under the unchanged candidate manifest")
	}

	readme.SHA256 = strings.Repeat("1", 64)
	readme.Size++
	altered["README.md"] = readme
	delete(altered, "LICENSE")
	added := sourceFile{Path: "zz-step10-new.txt", Size: 3, SHA256: strings.Repeat("2", 64)}
	altered[added.Path] = added
	candidate.Files = []candidateEntry{
		{Path: "LICENSE", Kind: "deleted"},
		{Path: "README.md", Kind: "file", Size: readme.Size, SHA256: readme.SHA256},
		{Path: added.Path, Kind: "file", Size: added.Size, SHA256: added.SHA256},
	}
	if err := validateCompleteSourceTree(candidate, altered); err != nil {
		t.Fatalf("exact base plus asymmetric replacement/deletion/addition refused: %v", err)
	}
	modeMismatch := make(map[string]sourceFile, len(altered))
	for path, file := range altered {
		modeMismatch[path] = file
	}
	modeMismatch["README.md"] = sourceFile{Path: "README.md", Size: readme.Size, SHA256: readme.SHA256, Executable: true}
	if err := validateCompleteSourceTree(candidate, modeMismatch); err == nil {
		t.Fatal("accepted an executable-bit substitution absent from the candidate manifest")
	}
}

func TestVerifierCheckToolchainDigestBindsNixConfig(t *testing.T) {
	toolchain := verifierToolchain{NixSHA256: strings.Repeat("a", 64)}
	byID := make(map[string]verifierCheckSpec)
	for _, spec := range verifierCheckSpecs() {
		byID[spec.id] = spec
	}
	loopbackDevelop := []string{"NIX_CONFIG=" + verifierCheckNixConfig(byID["focused-step10"])}
	nonLoopbackDevelop := []string{"NIX_CONFIG=" + verifierCheckNixConfig(byID["cross-compile"])}
	candidateBuild := []string{
		"WIPD_STEP10_VERIFIER_CHECK=candidate-build",
		"NIX_CONFIG=" + verifierCheckNixConfig(byID["candidate-build"]),
	}
	defaultFlakeBuild := []string{
		"WIPD_STEP10_VERIFIER_CHECK=default-flake-build",
		"NIX_CONFIG=" + verifierCheckNixConfig(byID["default-flake-build"]),
		"source_selection=immutable-path-snapshot-without-git-metadata",
	}
	if candidateBuild[1] != defaultFlakeBuild[1] {
		t.Fatal("both Nix build checks must bind the same scoped NIX_CONFIG")
	}
	if !reflect.DeepEqual(loopbackDevelop, nonLoopbackDevelop) {
		t.Fatal("Nix develop configuration changed based on loopback authorization")
	}
	if !strings.Contains(candidateBuild[1], "sandbox = false\n") || !strings.Contains(defaultFlakeBuild[1], "sandbox = false\n") {
		t.Fatal("sandbox = false must be bound to both fixed Nix build checks that require nested Nix")
	}
	if verifierCheckToolchainDigest(toolchain, candidateBuild) == verifierCheckToolchainDigest(toolchain, defaultFlakeBuild) {
		t.Fatal("check identity did not bind the exact per-check environment for the Nix build checks")
	}
}

func verifierNetworkProofFixture(loopback string) string {
	setupCaps := "CapInh:\t0000000000001000\nCapPrm:\t0000000000001000\nCapEff:\t0000000000001000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000001000\nNoNewPrivs:\t1"
	postCaps := "CapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000001000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1"
	state := func(label, flags, caps string) string {
		return strings.Join([]string{
			label + " uid/gid: uid=0 gid=0 groups=0,65534",
			label + " parent user namespace: user:[4026531837]",
			label + " parent network namespace: net:[4026531840]",
			label + " user namespace: user:[4026532560]",
			label + " network namespace: net:[4026532565]",
			label + " uid_map:", "         0       1000          1",
			label + " gid_map:", "         0       1000          1",
			label + " links:", "1: lo: " + flags + " mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000",
			label + " ipv4 routes:", label + " ipv6 routes:", label + " capability sets:", caps,
		}, "\n")
	}
	preLoopback := "<LOOPBACK,UP,LOWER_UP>"
	postLoopback := "<LOOPBACK,UP,LOWER_UP>"
	if loopback == "down" {
		postLoopback = "<LOOPBACK>"
	}
	return strings.Join([]string{
		"loopback setup command: ip link set dev lo " + loopback + " exit=0",
		state("pre-setup", preLoopback, setupCaps),
		state("post-loopback-setup", postLoopback, setupCaps),
		state("post-drop-before-check", postLoopback, postCaps),
		state("post-check", postLoopback, postCaps),
	}, "\n")
}

func TestVerifierNetworkProofRequiresExpectedIsolatedState(t *testing.T) {
	for _, loopback := range []string{"up", "down"} {
		t.Run(loopback, func(t *testing.T) {
			proof := verifierNetworkProofFixture(loopback)
			if !validVerifierNetworkProof(proof, loopback) {
				t.Fatal("valid isolated namespace proof was rejected")
			}
			if validVerifierNetworkProof(proof, map[string]string{"up": "down", "down": "up"}[loopback]) {
				t.Fatal("accepted proof for the wrong loopback policy")
			}
			mutations := map[string]string{
				"extra-interface":           strings.Replace(proof, "post-drop-before-check ipv4 routes:", "2: eth0: <BROADCAST,UP>\npost-drop-before-check ipv4 routes:", 1),
				"external-route":            strings.Replace(proof, "post-check ipv4 routes:", "post-check ipv4 routes:\ndefault via 192.0.2.1\n", 1),
				"setup-capability-retained": strings.Replace(proof, "post-drop-before-check capability sets:\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000", "post-drop-before-check capability sets:\nCapInh:\t0000000000001000\nCapPrm:\t0000000000001000\nCapEff:\t0000000000001000", 1),
				"identity-changed":          strings.Replace(proof, "post-check uid/gid: uid=0 gid=0 groups=0,65534", "post-check uid/gid: uid=0 gid=0 groups=0", 1),
				"namespace-changed":         strings.Replace(proof, "post-check network namespace: net:[4026532565]", "post-check network namespace: net:[4026532600]", 1),
			}
			for name, mutated := range mutations {
				t.Run(name, func(t *testing.T) {
					if mutated == proof || validVerifierNetworkProof(mutated, loopback) {
						t.Fatalf("accepted altered network proof %q", name)
					}
				})
			}
		})
	}
}

func TestVerifierCheckRecordRejectsSubstitutedNixConfig(t *testing.T) {
	for _, spec := range verifierCheckSpecs() {
		t.Run(spec.id, func(t *testing.T) {
			environment := []string{
				"HOME=/home/verifier", "TMPDIR=/tmp", "GOCACHE=/tmp/go-cache", "GOMODCACHE=/tmp/go-mod-cache",
				"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", verifierNetworkLabel(spec),
				"WIPD_STEP10_VERIFIER_CHECK=" + spec.id, "NIX_REMOTE=local",
				"NIX_CONFIG=" + verifierCheckNixConfig(spec),
			}
			if spec.id == "default-flake-build" {
				environment = append(environment, "source_selection=immutable-path-snapshot-without-git-metadata")
			}
			check := verifierCheck{
				ID: spec.id, CheckSet: step10VerifierCheckSet, Required: spec.required, Diagnostic: spec.diagnostic,
				Argv: append([]string(nil), spec.argv...), Environment: environment, CleanStateID: strings.Repeat("a", 32),
				ToolchainSHA256: verifierCheckToolchainDigest(verifierToolchain{}, environment),
				StartedAt:       time.Now().UTC().Format(time.RFC3339Nano), ExitCode: 0,
				StdoutSHA256: strings.Repeat("0", 64), StderrSHA256: strings.Repeat("0", 64),
			}
			if spec.id != "snapshot-integrity" && spec.id != "static-whitespace-yaml-conflicts" {
				proof := "WIPD-VERIFIER-NETWORK-PROOF-BEGIN\n" + verifierNetworkProofFixture(map[bool]string{true: "up", false: "down"}[spec.loopback]) +
					"\nWIPD-VERIFIER-NETWORK-PROOF-END"
				check.StderrTail = proof
				check.StderrBytes = int64(len(proof))
			}
			if !validVerifierCheckRecord(check, spec) {
				t.Fatal("verifier rejected the exact per-check NIX_CONFIG")
			}
			config := verifierCheckNixConfig(spec)
			if strings.Contains(config, "sandbox = false\n") {
				config = strings.Replace(config, "sandbox = false\n", "", 1)
			} else {
				config += "sandbox = false\n"
			}
			for index, value := range check.Environment {
				if strings.HasPrefix(value, "NIX_CONFIG=") {
					check.Environment[index] = "NIX_CONFIG=" + config
					break
				}
			}
			if validVerifierCheckRecord(check, spec) {
				t.Fatal("verifier accepted NIX_CONFIG with a missing or out-of-scope sandbox override")
			}
		})
	}
}

func TestVerifierNetworkLauncherScopesLoopbackAndDropsSetupCapability(t *testing.T) {
	args := verifierNetworkNamespaceArgs()
	wantArgs := []string{"--uid", "0", "--gid", "0", "--cap-add", "CAP_NET_ADMIN"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("network namespace setup args = %v, want %v", args, wantArgs)
	}
	for _, arg := range args {
		if arg == "--share-net" {
			t.Fatal("network namespace configuration must never share the host network")
		}
	}

	toolchain := verifierToolchain{
		IPPath: "/nix/store/test-iproute2/bin/ip", CapshPath: "/nix/store/test-libcap/bin/capsh",
		BashPath: "/nix/store/test-bash/bin/bash", EnvPath: "/nix/store/test-coreutils/bin/env",
	}
	for _, test := range []struct {
		name     string
		loopback bool
		state    string
	}{
		{name: "process", loopback: true, state: "up"},
		{name: "non-process", state: "down"},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := verifierNetworkSetupScript(toolchain, "user:[4026532560]", "net:[4026532565]", test.loopback)
			for _, required := range []string{
				"link set dev lo \"$LOOPBACK_ACTION\"", "LOOPBACK_ACTION='" + test.state + "'",
				"--caps=cap_net_admin-eip", "--noamb", "--no-new-privs",
				"CapEff:\\t0000000000001000", "CapAmb:\\t0000000000001000",
				"CapEff:\\t0000000000000000", "NoNewPrivs:\\t1",
				"pre-setup", "post-loopback-setup", "post-drop-before-check", "post-check",
				"assert_isolated_network", "WIPD-VERIFIER-NETWORK-PROOF-BEGIN",
				`"$HOST_NET_NS" "$LOOPBACK_ACTION" "$setup_caps"`,
				`"$precheck_state" >&2`, `printf '%s\n' "$pre_setup_state" >&2`,
			} {
				if !strings.Contains(script, required) {
					t.Errorf("%s launcher script omitted %q", test.name, required)
				}
			}
			definition := strings.Index(script, "assert_isolated_interfaces() {")
			invocation := strings.Index(script, "assert_isolated_interfaces\n")
			if definition < 0 || invocation < definition {
				t.Fatal("namespace launcher calls assert_isolated_interfaces without defining it first")
			}
			if strings.Contains(script, "--share-net") || strings.Contains(script, "host networking") {
				t.Fatal("network launcher script contains a host-network fallback")
			}
		})
	}
}

func TestCanonicalCandidateManifestMatchesHandoffEncoding(t *testing.T) {
	candidate := candidateManifest{
		Format: "candidate-v1", Base: "base-revision", Head: "head-revision",
		Files: []candidateEntry{
			{Path: "internal/check.go", Kind: "file", Executable: false, Size: 7, SHA256: strings.Repeat("a", 64)},
			{Path: "internal/removed.go", Kind: "deleted"},
		},
	}
	want := "{\"base\":\"base-revision\",\"files\":[{" +
		"\"executable\":false,\"kind\":\"file\",\"path\":\"internal/check.go\",\"sha256\":\"" + strings.Repeat("a", 64) + "\",\"size\":7}," +
		"{\"kind\":\"deleted\",\"path\":\"internal/removed.go\"}],\"format\":\"candidate-v1\",\"head\":\"head-revision\"}\n"
	got, err := canonicalCandidateManifest(candidate)
	if err != nil || string(got) != want {
		t.Fatalf("canonical candidate manifest = %q, error=%v; want %q", got, err, want)
	}
}

func TestSnapshotIntegrityRechecksPinnedBytesAndModes(t *testing.T) {
	root := t.TempDir()
	files := []struct {
		name       string
		content    []byte
		executable bool
	}{
		{name: "docs/review.md", content: []byte("# Review\n"), executable: false},
		{name: "scripts/check", content: []byte("#!/bin/sh\nexit 0\n"), executable: true},
	}
	tree := sourceTreeManifest{
		Schema:                  "wipd.step10-source-tree/1",
		CandidateManifest:       "{}",
		CandidateManifestSHA256: blobDigest([]byte("candidate")),
		SnapshotSHA256:          blobDigest([]byte("snapshot")),
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if file.executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, file.content, mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(file.content)
		tree.Files = append(tree.Files, sourceFile{
			Path: file.name, Executable: file.executable, Size: int64(len(file.content)), SHA256: hex.EncodeToString(sum[:]),
		})
	}

	check := func(wantCode int) string {
		t.Helper()
		code, problem, byteCount, digest, output := runSnapshotIntegrityCheck(root, tree, []byte("immutable snapshot"))
		if code != wantCode {
			t.Fatalf("snapshot integrity exit = %d, want %d (problem=%q)", code, wantCode, problem)
		}
		if wantCode == 0 {
			if problem != "" || byteCount != int64(len(output)) || digest != verifierRawSHA256([]byte(output)) ||
				!strings.Contains(output, `"file_count":2`) || !strings.Contains(output, `"outcome":"verified"`) {
				t.Fatalf("snapshot integrity report did not match the verified bytes: output=%q problem=%q bytes=%d digest=%q", output, problem, byteCount, digest)
			}
		} else if problem == "" {
			t.Fatal("failed snapshot integrity check had no concrete problem")
		}
		return problem
	}
	check(0)

	changed := filepath.Join(root, "docs/review.md")
	if err := os.WriteFile(changed, []byte("# Altered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if problem := check(1); !strings.Contains(problem, "metadata mismatch") && !strings.Contains(problem, "content mismatch") {
		t.Fatalf("tampered candidate content was not identified: %q", problem)
	}
}

func TestVerifierClosureProjectionIsReadOnlyAndUsesFreshCanonicalStoreOverlay(t *testing.T) {
	const name = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-toolchain"
	storePath := "/nix/store/" + name
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, name)
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(source, "bin", "tool")
	if err := os.WriteFile(tool, []byte("pinned-tool\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("pinned-data\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin/tool", filepath.Join(source, "tool")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(source, "bin"), 0o700)
		_ = os.Chmod(source, 0o700)
	})
	if err := os.Chmod(filepath.Join(source, "bin"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o555); err != nil {
		t.Fatal(err)
	}

	projection := filepath.Join(t.TempDir(), "closure")
	t.Cleanup(func() {
		_ = filepath.WalkDir(projection, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	digest, err := materializeVerifierClosure([]string{storePath}, map[string]string{storePath: source}, projection)
	if err != nil || !validSHA256(digest) {
		t.Fatalf("materialize immutable closure: digest=%q error=%v", digest, err)
	}
	projectedData, err := os.ReadFile(filepath.Join(projection, name, "data"))
	if err != nil || string(projectedData) != "pinned-data\n" {
		t.Fatalf("projected closure data = %q, error=%v", projectedData, err)
	}
	projectedLink, err := os.Readlink(filepath.Join(projection, name, "tool"))
	if err != nil || projectedLink != "bin/tool" {
		t.Fatalf("projected closure symlink = %q, error=%v", projectedLink, err)
	}
	for _, path := range []string{
		projection, filepath.Join(projection, name), filepath.Join(projection, name, "bin"),
		filepath.Join(projection, name, "bin", "tool"), filepath.Join(projection, name, "data"),
	} {
		info, statErr := os.Stat(path)
		mode := os.FileMode(0)
		if info != nil {
			mode = info.Mode()
		}
		if statErr != nil || mode.Perm()&0o222 != 0 {
			t.Fatalf("projected dependency %q is writable: mode=%v error=%v", path, mode, statErr)
		}
	}

	scratch := t.TempDir()
	upper, work := filepath.Join(scratch, "store-upper"), filepath.Join(scratch, "store-work")
	if err = os.Mkdir(upper, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if args := verifierNixStoreOverlayArgs(projection, upper, work); !reflect.DeepEqual(args,
		[]string{"--overlay-src", projection, "--overlay", upper, work, "/nix/store"}) {
		t.Fatalf("fresh canonical Nix store overlay arguments = %q", args)
	}
	if err = os.Mkdir(filepath.Join(upper, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-check-output"), 0o755); err != nil {
		t.Fatalf("fresh scratch overlay cannot hold Nix command outputs: %v", err)
	}
}

func TestRemoveVerifierScratchTreeHandlesReadOnlyProjectionDirectories(t *testing.T) {
	root, err := os.MkdirTemp(verifierScratchDir, verifierScratchPrefix+"cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "closure", "source")
	if err = os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(nested, "pinned-file"), []byte("immutable\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(nested, 0o700)
		_ = os.Chmod(filepath.Dir(nested), 0o700)
		_ = os.Chmod(root, 0o700)
		_ = os.RemoveAll(root)
	})
	if err = os.Chmod(nested, 0o555); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Dir(nested), 0o555); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}

	if err = removeVerifierScratchTree(root); err != nil {
		t.Fatalf("remove read-only verifier projection: %v", err)
	}
	if _, err = os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("verifier scratch remains after cleanup: error=%v", err)
	}
}
