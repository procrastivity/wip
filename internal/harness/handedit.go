package harness

import (
	"fmt"
	"os"

	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/wiperr"
)

// RefuseHandEdited reports, as a refusal error, whether installing into
// dir would overwrite content a human put there or changed by hand. It
// returns nil when dir does not exist, is empty, or matches its stamp
// byte for byte (the normal upgrade path — an older wip's tree that was
// never touched installs quietly). It returns a
// refusal.unstamped-harness-target error when dir holds files but no
// stamp (foreign content), or when the tree on disk differs from what its
// stamp recorded (a file changed, added, or removed since the last
// install). harnessName is used only in the message, which names
// `wip install <harness> --force` as the override. The doctor's
// stale-artifact check compares the *binary* against the stamp; this
// compares the *disk* against the stamp — the two are deliberately
// different questions.
func RefuseHandEdited(harnessName, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("harness: checking install dir %q: %w", dir, err)
	}

	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		return err
	}
	if !ok {
		actual, err := manifest.ChecksumTree(dir)
		if err != nil {
			return err
		}
		if len(actual) == 0 {
			return nil
		}
		return wiperr.New("refusal.unstamped-harness-target",
			fmt.Sprintf("refused — %s holds content with no install stamp; it was not written by `wip install %s` and will not be overwritten — re-run with `wip install %s --force` to replace it", dir, harnessName, harnessName))
	}

	actual, err := manifest.ChecksumTree(dir)
	if err != nil {
		return err
	}
	if drift := manifest.Drift(actual, stamp); len(drift) > 0 {
		return wiperr.New("refusal.unstamped-harness-target",
			fmt.Sprintf("refused — %s no longer matches what `wip install %s` last wrote (%d file(s) changed since); re-run with `wip install %s --force` to overwrite it", dir, harnessName, len(drift), harnessName))
	}

	return nil
}
