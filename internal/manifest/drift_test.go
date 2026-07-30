package manifest_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/manifest"
)

func TestDrift_Clean(t *testing.T) {
	files := map[string]string{"SKILL.md": "aaa", "plugin.json": "bbb"}
	stamp := manifest.Stamp{Files: files}
	if got := manifest.Drift(files, stamp); len(got) != 0 {
		t.Fatalf("Drift = %+v, want none when want and stamp agree exactly", got)
	}
}

func TestDrift_AddedSinceInstall(t *testing.T) {
	stamp := manifest.Stamp{Files: map[string]string{"SKILL.md": "aaa"}}
	want := map[string]string{"SKILL.md": "aaa", "NEW.md": "ccc"}

	got := manifest.Drift(want, stamp)
	if len(got) != 1 || got[0].Path != "NEW.md" || got[0].Reason != "added" {
		t.Fatalf("Drift = %+v, want one entry for NEW.md reason=added", got)
	}
}

func TestDrift_RemovedSinceInstall(t *testing.T) {
	stamp := manifest.Stamp{Files: map[string]string{"SKILL.md": "aaa", "OLD.md": "ccc"}}
	want := map[string]string{"SKILL.md": "aaa"}

	got := manifest.Drift(want, stamp)
	if len(got) != 1 || got[0].Path != "OLD.md" || got[0].Reason != "removed" {
		t.Fatalf("Drift = %+v, want one entry for OLD.md reason=removed", got)
	}
}

func TestDrift_ChangedSinceInstall(t *testing.T) {
	stamp := manifest.Stamp{Files: map[string]string{"SKILL.md": "aaa"}}
	want := map[string]string{"SKILL.md": "zzz"}

	got := manifest.Drift(want, stamp)
	if len(got) != 1 || got[0].Path != "SKILL.md" || got[0].Reason != "changed" {
		t.Fatalf("Drift = %+v, want one entry for SKILL.md reason=changed", got)
	}
}

func TestDrift_MultipleFindingsAllReported(t *testing.T) {
	stamp := manifest.Stamp{Files: map[string]string{"A": "1", "B": "2"}}
	want := map[string]string{"A": "1-changed", "C": "3"}

	got := manifest.Drift(want, stamp)
	if len(got) != 3 {
		t.Fatalf("Drift returned %d finding(s), want 3 (A changed, B removed, C added): %+v", len(got), got)
	}
}
