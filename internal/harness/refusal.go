package harness

import (
	"fmt"

	"github.com/procrastivity/wip/internal/wiperr"
)

// Refusal codes, one per refusing state (C4.5, C4.6). One shared code
// (refusal.unstamped-harness-target) used to cover UnownedConflict and
// Modified, with only the message telling them apart; a caller that reads
// codes can now tell them apart too.
const (
	CodeUnownedConflict = "refusal.unowned-harness-target"
	CodeModified        = "refusal.modified-harness-target"
	CodeIncompatible    = "refusal.incompatible-harness-target"
)

// RefusalCode returns the refusal code for a refusing state, or "" for
// Current, Missing and Stale, which install and uninstall act on.
func RefusalCode(s State) string {
	switch s {
	case UnownedConflict:
		return CodeUnownedConflict
	case Modified:
		return CodeModified
	case Incompatible:
		return CodeIncompatible
	default:
		return ""
	}
}

// Risk states the fact about dir that makes s unsafe to overwrite or
// remove, or "" for a state that is not a refusal. Refusal and doctor both
// build their messages from it, so the fact is worded once.
func Risk(harnessName, dir string, s State) string {
	switch s {
	case UnownedConflict:
		return fmt.Sprintf("%s holds content with no install stamp; it was not written by `wip install %s`", dir, harnessName)
	case Modified:
		return fmt.Sprintf("%s no longer matches what `wip install %s` last wrote", dir, harnessName)
	case Incompatible:
		return fmt.Sprintf("%s carries a stamp `wip install %s` cannot use (unparseable, or from a schema version this binary does not recognize)", dir, harnessName)
	default:
		return ""
	}
}

// Refusal returns the refusal error for dir's state s, or nil when s does
// not refuse (C4.6, C4.7). remedy is the caller's closing clause: install
// names --force (ForceRemedy), and uninstall, which has no --force, names
// removing the tree by hand.
func Refusal(harnessName, dir string, s State, remedy string) error {
	code := RefusalCode(s)
	if code == "" {
		return nil
	}
	return wiperr.New(code, fmt.Sprintf("refused — %s — %s", Risk(harnessName, dir, s), remedy))
}

// ForceRemedy names --force and, for a refusing state s, what --force
// would do to harnessName's tree (C4.5, C4.6): overwrite content the tool
// never wrote, destroy a human's edits, or replace an unreadable or
// foreign-schema stamp. install and doctor share it. Any other state
// returns "".
func ForceRemedy(harnessName string, s State) string {
	var consequence string
	switch s {
	case UnownedConflict:
		consequence = "which overwrites files the tool never wrote"
	case Modified:
		consequence = "which destroys the edits"
	case Incompatible:
		consequence = "which replaces the stamp"
	default:
		return ""
	}
	return fmt.Sprintf("re-run with `wip install %s --force`, %s", harnessName, consequence)
}
