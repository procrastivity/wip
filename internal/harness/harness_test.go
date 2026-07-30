package harness_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

func TestProjectable_OnlyPlumbing(t *testing.T) {
	verbs := []manifest.Verb{
		{Name: "step create", Kind: surface.Plumbing},
		{Name: "ask", Kind: surface.LLM},
		{Name: "watch", Kind: surface.ControlPlane},
		{Name: "status", Kind: surface.Plumbing},
	}

	got := harness.Projectable(verbs)

	if len(got) != 2 {
		t.Fatalf("Projectable returned %d verb(s), want 2 (plumbing only): %+v", len(got), got)
	}
	for _, v := range got {
		if v.Kind != surface.Plumbing {
			t.Errorf("Projectable included %q with kind %q, want plumbing only", v.Name, v.Kind)
		}
	}
}

func TestProjectable_EmptyInputEmptyOutput(t *testing.T) {
	got := harness.Projectable(nil)
	if len(got) != 0 {
		t.Fatalf("Projectable(nil) = %+v, want empty", got)
	}
}
