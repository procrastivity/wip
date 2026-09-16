package manifest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/manifest"
)

func TestBuild_DeclaresContractVersion(t *testing.T) {
	m, err := manifest.Build(fakeRoot(t), buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if m.Contract != "toolsmith/v1" {
		t.Errorf("Contract = %q, want %q — conformance must be readable from the tool (C3.6)", m.Contract, "toolsmith/v1")
	}
}

func TestBuild_ManifestDigestSelfCommits(t *testing.T) {
	build := buildinfo.Info{Version: "1.2.3", Commit: "abc123", Date: "2026-07-29"}

	m, err := manifest.Build(fakeRoot(t), build)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !strings.HasPrefix(m.ManifestDigest, "sha256:") {
		t.Fatalf("ManifestDigest = %q, want a sha256:-prefixed digest (C3.4)", m.ManifestDigest)
	}

	// Recompute the way a drift check would: blank the field, marshal,
	// hash. A mismatch means the digest does not commit to the document the
	// caller received.
	blanked := m
	blanked.ManifestDigest = ""
	b, err := json.Marshal(blanked)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sum := sha256.Sum256(b)
	if want := "sha256:" + hex.EncodeToString(sum[:]); m.ManifestDigest != want {
		t.Errorf("ManifestDigest = %q, want %q (recomputed over the digest-blanked document)", m.ManifestDigest, want)
	}

	// Two builds of the same binary must agree, or the digest is useless
	// as a comparable identity.
	again, err := manifest.Build(fakeRoot(t), build)
	if err != nil {
		t.Fatalf("Build (second): %v", err)
	}
	if again.ManifestDigest != m.ManifestDigest {
		t.Errorf("second Build digest = %q, want %q — the digest must be deterministic", again.ManifestDigest, m.ManifestDigest)
	}
}
