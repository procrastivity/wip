package tiers

import (
	"context"
	"fmt"
	"sort"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// DoctorReport is the outcome of the unknown-clone check (tiers Brief, "Move
// detection") — one of doctor's eventual five checks; `guards` adds the
// other four to this same `wip doctor` command later, not a new one.
type DoctorReport struct {
	// Known is true when the current directory's common-dir already matches
	// a Clone row wip knows — nothing to report from this check.
	Known bool
	Clone store.Clone
	Repo  store.Repo

	// The unknown-clone, remote-known case: offer relink-or-new, naming the
	// matched Repo and its existing Clones as relink candidates. Set only
	// when Known is false and the check did not refuse outright.
	OfferRepo   store.Repo
	OfferClones []store.Clone
}

// CheckUnknownClone implements the whole of "Move detection": a known clone
// reports clean (after checking for remote adoption, same as every
// tier-resolving verb); an unknown common-dir whose current remote matches a
// known Repo gets the relink-or-new-clone offer; an unknown common-dir whose
// remote also matches nothing falls through to the standard unknown-clone
// hard failure.
func CheckUnknownClone(ctx context.Context, s *store.Store, actor store.Actor, dir string) (DoctorReport, error) {
	commonDir, err := gitCommonDir(ctx, dir)
	if err != nil {
		return DoctorReport{}, notAGitRepo(dir, err)
	}

	clone, found, err := s.CloneByCommonDir(ctx, commonDir)
	if err != nil {
		return DoctorReport{}, err
	}
	if found {
		if _, err := AdoptIfPossible(ctx, s, actor, clone, dir); err != nil {
			return DoctorReport{}, err
		}
		repo, err := s.Repo(ctx, clone.Repo)
		if err != nil {
			return DoctorReport{}, err
		}
		return DoctorReport{Known: true, Clone: clone, Repo: repo}, nil
	}

	// Doctor is a diagnostic — "is this place known under any name" — which
	// is a different question from init's "which remote names this Repo's
	// identity", and there is no committed Repo yet here to have recorded an
	// identity_remote against. So every current remote is checked, not just
	// the identity-remote convention, in deterministic (sorted) name order.
	repo, offered, err := repoOfAnyRemote(ctx, s, dir)
	if err != nil {
		return DoctorReport{}, err
	}
	if offered {
		clones, err := s.ClonesOfRepo(ctx, repo.ID)
		if err != nil {
			return DoctorReport{}, err
		}
		return DoctorReport{OfferRepo: repo, OfferClones: clones}, nil
	}

	return DoctorReport{}, unknownClone()
}

// repoOfAnyRemote checks every remote configured at dir, in sorted name
// order, against the known Repos, and reports the first match.
func repoOfAnyRemote(ctx context.Context, s *store.Store, dir string) (store.Repo, bool, error) {
	remotes, err := gitRemotes(ctx, dir)
	if err != nil {
		return store.Repo{}, false, err
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		normalized, err := NormalizeRemote(remotes[name])
		if err != nil {
			continue
		}
		if repo, ok, err := s.RepoByRemote(ctx, normalized); err != nil {
			return store.Repo{}, false, err
		} else if ok {
			return repo, true, nil
		}
	}
	return store.Repo{}, false, nil
}

// Relink implements `wip clone relink <clone-locator>`: the accepted half of
// the move-detection offer above, run from the new location. A ULID locator
// resolves directly; a label locator resolves scoped to whichever Repo the
// current directory's own remote matches — the same "remote known" situation
// CheckUnknownClone offers relink against in the first place, since a label
// is only ever unique within its Repo.
func Relink(ctx context.Context, s *store.Store, actor store.Actor, dir, locator string) (store.Clone, error) {
	commonDir, err := gitCommonDir(ctx, dir)
	if err != nil {
		return store.Clone{}, notAGitRepo(dir, err)
	}

	var clone store.Clone
	if store.IsIdentityShaped(locator) {
		clone, err = s.Clone(ctx, locator)
		if err != nil {
			return store.Clone{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no clone %s", locator))
		}
	} else {
		repo, offered, err := repoOfAnyRemote(ctx, s, dir)
		if err != nil {
			return store.Clone{}, err
		}
		if !offered {
			return store.Clone{}, wiperr.New("validation.no-repo-match",
				"this directory's remotes match no known repo; relink by ULID, or run this from a directory whose remote matches the target repo")
		}
		clone, err = ResolveClone(ctx, s.View, repo.ID, locator)
		if err != nil {
			return store.Clone{}, err
		}
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: clone.Repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeCloneRelinked,
			Subject: clone.ID,
			Payload: store.CloneRelinked{GitCommonDir: commonDir},
		}}, nil
	}); err != nil {
		return store.Clone{}, err
	}
	return s.Clone(ctx, clone.ID)
}
