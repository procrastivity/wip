// Package registry holds the one list of harness names that install and
// uninstall render from — help text, bare-invocation listings, and
// unknown-harness errors all read it, so the two verbs can never disagree
// about what is installable. It is a leaf: it imports each harness
// subpackage for its Name constant, and nothing imports it but the verbs.
// internal/harness itself cannot hold this list because every subpackage
// imports it (a cycle), and neither verb package should own a fact both
// read.
package registry

import (
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
)

// Names lists every harness wip can project itself into. claude-code
// leads as the reference install; the rest follow alphabetically.
var Names = []string{claudecode.Name, codex.Name, devin.Name, pi.Name, opencode.Name}
