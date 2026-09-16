package harness_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

// TestProjectable_KeysOnThePlumbingNamespacePrefix pins the D53/D112
// reading: the filter is structural (the "plumbing " name prefix, D112),
// not kind-only (D53). A bare-named verb is dropped even when it is
// kind=plumbing (every porcelain verb but init/doctor/install/uninstall/
// version/manifest is exactly this shape), and a namespaced verb is kept
// only when it is also kind=plumbing — the kind test stays a conjunct.
func TestProjectable_KeysOnThePlumbingNamespacePrefix(t *testing.T) {
	verbs := []manifest.Verb{
		{Name: "plumbing step create", Kind: surface.Plumbing},
		{Name: "status", Kind: surface.Plumbing},
		{Name: "ask", Kind: surface.LLM},
		{Name: "watch", Kind: surface.ControlPlane},
	}

	got := harness.Projectable(verbs)

	if len(got) != 1 || got[0].Name != "plumbing step create" {
		t.Fatalf("Projectable returned %+v, want exactly [{plumbing step create}]", got)
	}
}

// TestProjectable_KindStaysAConjunct confirms D53 survives inside the
// namespace: a "plumbing "-named verb that is NOT kind=plumbing (an
// llm-kind verb landing inside the namespace, hypothetically) still does
// not project.
func TestProjectable_KindStaysAConjunct(t *testing.T) {
	verbs := []manifest.Verb{
		{Name: "plumbing step create", Kind: surface.Plumbing},
		{Name: "plumbing ask", Kind: surface.LLM},
	}

	got := harness.Projectable(verbs)

	if len(got) != 1 || got[0].Name != "plumbing step create" {
		t.Fatalf("Projectable returned %+v, want exactly [{plumbing step create}]", got)
	}
}

func TestProjectable_EmptyInputEmptyOutput(t *testing.T) {
	got := harness.Projectable(nil)
	if len(got) != 0 {
		t.Fatalf("Projectable(nil) = %+v, want empty", got)
	}
}
