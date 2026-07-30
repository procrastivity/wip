package manifest_test

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

func fakeRoot(t *testing.T, extra ...*cobra.Command) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "wip"}
	plumbing := &cobra.Command{
		Use:   "widget",
		Short: "a synthetic plumbing verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(plumbing, surface.Plumbing)
	root.AddCommand(plumbing)
	for _, c := range extra {
		root.AddCommand(c)
	}
	return root
}

func TestBuild_ToolAndSchemaVersion(t *testing.T) {
	root := fakeRoot(t)
	build := buildinfo.Info{Version: "1.2.3", Commit: "abc123", Date: "2026-07-29"}

	m, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if m.Tool.Name != "wip" {
		t.Errorf("Tool.Name = %q, want %q", m.Tool.Name, "wip")
	}
	if m.Tool.Version != "1.2.3" || m.Tool.Commit != "abc123" || m.Tool.Date != "2026-07-29" {
		t.Errorf("Tool = %+v, want the build vars threaded through unchanged", m.Tool)
	}
	if m.SchemaVersion != manifest.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, manifest.SchemaVersion)
	}
}

func TestBuild_ListsEveryRegisteredVerbRegardlessOfKind(t *testing.T) {
	llmVerb := &cobra.Command{
		Use:   "judge",
		Short: "a synthetic llm-kind verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(llmVerb, surface.LLM)

	controlPlaneVerb := &cobra.Command{
		Use:   "watch",
		Short: "a synthetic control-plane-kind verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(controlPlaneVerb, surface.ControlPlane)

	root := fakeRoot(t, llmVerb, controlPlaneVerb)

	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	kinds := map[string]surface.Kind{}
	for _, v := range m.Verbs {
		kinds[v.Name] = v.Kind
	}
	want := map[string]surface.Kind{
		"widget": surface.Plumbing,
		"judge":  surface.LLM,
		"watch":  surface.ControlPlane,
	}
	for name, kind := range want {
		got, ok := kinds[name]
		if !ok {
			t.Errorf("verb %q missing from manifest — the manifest's own list must never pre-filter by kind", name)
			continue
		}
		if got != kind {
			t.Errorf("verb %q kind = %q, want %q", name, got, kind)
		}
	}
}

func TestBuild_UnannotatedVerbIsAnError(t *testing.T) {
	root := fakeRoot(t)
	unannotated := &cobra.Command{
		Use:  "oops",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	root.AddCommand(unannotated)

	if _, err := manifest.Build(root, buildinfo.Info{}); err == nil {
		t.Fatal("Build: want an error when a registered verb declares no surface kind (D53)")
	}
}

func TestBuild_ListsShippedAssetsWithChecksums(t *testing.T) {
	root := fakeRoot(t)
	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.Assets) == 0 {
		t.Fatal("Assets is empty, want at least the shipped fixture assets")
	}
	for _, a := range m.Assets {
		if a.Path == "" {
			t.Errorf("asset has an empty Path: %+v", a)
		}
		if len(a.SHA256) != 64 {
			t.Errorf("asset %q SHA256 = %q, want a 64-char hex digest", a.Path, a.SHA256)
		}
	}
}
