// Package tiers implements PLAN 1.1: the Repo/Clone/Worktree identity rules
// (MODEL §7) and the verbs that make them real — `init`, `clone list`,
// `clone relink`, `label`, the unknown-clone slice of `doctor`, and the
// tier-scoped stub of `status`. Everything here builds strictly against
// `workplans/tiers.md`'s Brief; a rule that isn't there doesn't belong here
// either.
//
// This package owns no storage of its own. It reads and writes exclusively
// through internal/store's data-access layer (schema's Brief) and shells out
// to `git` for the facts wip cannot derive any other way: the git-common-dir
// and git-dir a location resolves to, and the remotes configured there.
package tiers

import "github.com/procrastivity/wip/internal/store"

// OpenStore opens wip's one store at its usual path (store.DBPath — D35,
// D49; WIP_DB_PATH overrides it for tests). Every verb in this package opens
// the store this same way, so a test pointing WIP_DB_PATH at a scratch file
// never touches the real one on the host that ran it.
func OpenStore() (*store.Store, error) {
	path, err := store.DBPath()
	if err != nil {
		return nil, err
	}
	return store.Open(path)
}
