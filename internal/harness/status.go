package harness

import (
	"errors"
	"fmt"
	"os"

	"github.com/procrastivity/wip/internal/manifest"
)

// State is one of the six drift states a harness target can be in (C4.5,
// T6). The string values are exactly duo's spellings, since they are the
// vocabulary doctor and the refusal codes surface to a human unchanged.
type State string

const (
	// Current means the tree matches its stamp, and the stamp matches what
	// the current binary would generate: nothing to write.
	Current State = "current"
	// Missing means no projection is present: the directory is absent, or
	// present but empty, and carries no stamp.
	Missing State = "missing"
	// Stale means the stamp is valid and the disk matches it, but the
	// current binary would generate something different (a verb added or
	// changed since install).
	Stale State = "stale"
	// Modified means the disk no longer matches the stamp: a human (or
	// something other than install) changed, added, or removed a
	// generated file.
	Modified State = "modified"
	// UnownedConflict means the directory holds files but carries no
	// stamp: nothing proves the tool wrote them.
	UnownedConflict State = "unowned_conflict"
	// Incompatible means the stamp cannot be trusted: its JSON does not
	// parse, or its schemaVersion differs from manifest.SchemaVersion
	// (including the zero value a stamp without the field decodes to).
	Incompatible State = "incompatible"
)

// Status derives dir's drift state against files, the binary's currently
// generated content for that target (C4.6's three comparisons, folded into
// C4.5's vocabulary). Conditions are checked in this order, first match
// wins:
//
//  1. The stamp exists but its JSON does not parse (manifest.
//     ErrStampUnparseable), or its schemaVersion differs from
//     manifest.SchemaVersion: Incompatible. A stamp this binary cannot
//     read is the same fact as a stamp from another schema — either way
//     the binary cannot tell what it owns.
//  2. The directory holds files and has no stamp: UnownedConflict —
//     nothing proves the tool wrote them.
//  3. The files on disk differ from the stamp: Modified. Checked before
//     staleness on purpose: a tree can be both, and --force on it
//     destroys a human edit, which is the fact the caller must see.
//  4. The binary's generated files (files) differ from the stamp: Stale.
//     Skipped entirely when files is nil — uninstall has no reason to
//     build a manifest just to ask this, and a stale tree then reads as
//     Current.
//  5. The directory is absent, or present but empty, and there is no
//     stamp: Missing (duo's "no projection present").
//  6. Otherwise: Current.
//
// The stamp's toolVersion is never compared: a version bump whose
// generated content is byte-identical is still Current (C4.6).
//
// A path that exists but is not a directory is reported as
// UnownedConflict: something the tool did not write occupies the target,
// which is the same fact as foreign content inside a directory, and no
// other state in the table fits a non-directory better.
func Status(dir string, files map[string][]byte) (State, error) {
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return Missing, nil
	case err != nil:
		return "", fmt.Errorf("harness: checking install dir %q: %w", dir, err)
	case !info.IsDir():
		return UnownedConflict, nil
	}

	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		if errors.Is(err, manifest.ErrStampUnparseable) {
			return Incompatible, nil
		}
		return "", err
	}
	if ok && stamp.SchemaVersion != manifest.SchemaVersion {
		return Incompatible, nil
	}

	actual, err := manifest.ChecksumTree(dir)
	if err != nil {
		return "", err
	}

	if !ok {
		if len(actual) > 0 {
			return UnownedConflict, nil
		}
		return Missing, nil
	}

	if drift := manifest.Drift(actual, stamp); len(drift) > 0 {
		return Modified, nil
	}

	if files != nil {
		want := manifest.ChecksumFiles(files)
		if drift := manifest.Drift(want, stamp); len(drift) > 0 {
			return Stale, nil
		}
	}

	return Current, nil
}
