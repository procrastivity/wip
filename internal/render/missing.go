package render

import (
	"fmt"
	"os"

	"github.com/procrastivity/wip/internal/wiperr"
)

// ReadGenerated implements step-08's resolved missing-file-recovery call:
// fail loudly, never silently re-render, never ask. `.wip/generated/` is
// disposable by design (D33), but an agent reading a specific rendered file
// may already be reasoning from content it read once — silently swapping in
// a fresh render behind its back is exactly the kind of guess MODEL §11's
// refusal-over-guessing posture refuses elsewhere. So a read against a
// missing expected file returns a wip-authored refusal naming the missing
// path and directing to `wip refresh <locator>` — an explicit, observable
// action, never a substitution the caller can't see.
//
// locator is what a human or agent should pass to `wip refresh` to
// regenerate path — ordinarily the Matter (or narrower node) locator whose
// render produced it.
func ReadGenerated(path, locator string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, missingGenerated(path, locator)
		}
		return nil, err
	}
	return data, nil
}

// missingGenerated is vocabulary's amended draft for this message (folded
// into workplans/vocabulary.md by this Matter, HANDOFF §1.6): the denser
// --json body, reused as the one Message wiperr.Error carries in both modes
// (the same convention tiers/errors.go documents for refusal.unknown-clone).
func missingGenerated(path, locator string) error {
	return wiperr.New("refusal.generated-missing",
		fmt.Sprintf("%s is missing; run `wip refresh %s` to regenerate it", path, locator))
}
