package manifest_test

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

func TestBuild_ExcludesHiddenCommands(t *testing.T) {
	root := fakeRoot(t)
	hidden := &cobra.Command{
		Use:    "__hidden-fixture",
		Hidden: true,
		RunE:   func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(hidden, surface.Plumbing)
	root.AddCommand(hidden)

	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, v := range m.Verbs {
		if v.Name == "__hidden-fixture" {
			t.Fatal("Build: a Hidden command must never appear in the manifest walk")
		}
	}
}

func TestBuild_ExcludesCobraBuiltins(t *testing.T) {
	root := fakeRoot(t)
	// Force Cobra to initialize its default help/completion commands, the
	// same way ExecuteC would during a real run.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, v := range m.Verbs {
		if v.Name == "help" || v.Name == "completion" {
			t.Errorf("Build: Cobra's own %q command must never appear in the manifest walk", v.Name)
		}
	}
}

func TestBuild_NestedVerbNameJoinsWithASpace(t *testing.T) {
	root := fakeRoot(t)
	group := &cobra.Command{Use: "gate"}
	declare := &cobra.Command{
		Use:   "declare",
		Short: "a synthetic nested verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(declare, surface.Plumbing)
	group.AddCommand(declare)
	root.AddCommand(group)

	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, v := range m.Verbs {
		if v.Name == "gate declare" {
			return
		}
	}
	t.Fatal(`Build: want a verb named "gate declare" for a nested command`)
}

func TestBuild_ArgsReflectDeclaredFlagsOnly(t *testing.T) {
	root := fakeRoot(t)
	cmd := &cobra.Command{
		Use:   "thing",
		Short: "a synthetic verb with flags",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	cmd.Flags().String("name", "", "a required flag")
	if err := cmd.MarkFlagRequired("name"); err != nil {
		t.Fatal(err)
	}
	cmd.Flags().Bool("force", false, "an optional flag")
	surface.Annotate(cmd, surface.Plumbing)
	root.AddCommand(cmd)

	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var thing *manifest.Verb
	for i := range m.Verbs {
		if m.Verbs[i].Name == "thing" {
			thing = &m.Verbs[i]
		}
	}
	if thing == nil {
		t.Fatal(`verb "thing" not found in manifest`)
	}

	byName := map[string]manifest.Arg{}
	for _, a := range thing.Args {
		byName[a.Name] = a
	}
	if _, ok := byName["help"]; ok {
		t.Error(`Args includes Cobra's own "help" flag, want it excluded`)
	}
	nameArg, ok := byName["name"]
	if !ok {
		t.Fatal(`Args missing "name"`)
	}
	if nameArg.Type != "string" || !nameArg.Required {
		t.Errorf("name arg = %+v, want {Type: string, Required: true}", nameArg)
	}
	forceArg, ok := byName["force"]
	if !ok {
		t.Fatal(`Args missing "force"`)
	}
	if forceArg.Type != "bool" || forceArg.Required {
		t.Errorf("force arg = %+v, want {Type: bool, Required: false}", forceArg)
	}
}

func TestBuild_ArgsExcludeInheritedGlobalFlags(t *testing.T) {
	root := fakeRoot(t)
	root.PersistentFlags().Bool("json", false, "global --json")
	cmd := &cobra.Command{
		Use:   "thing",
		Short: "a synthetic verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(cmd, surface.Plumbing)
	root.AddCommand(cmd)

	m, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, v := range m.Verbs {
		if v.Name != "thing" {
			continue
		}
		for _, a := range v.Args {
			if a.Name == "json" {
				t.Fatal(`Args includes root's inherited --json flag, want only this verb's own LocalFlags`)
			}
		}
	}
}
