package authoritystore

import (
	"context"
	"database/sql"
	"errors"
)

var (
	// ErrRouteUnbound means the authority has no binding for the normalized key.
	ErrRouteUnbound = errors.New("authoritystore: normalized Repo key is unbound")
	// ErrRouteDomainMismatch is a deliberately opaque refusal: a key owned by
	// another domain is not disclosed to the selected route.
	ErrRouteDomainMismatch = errors.New("authoritystore: normalized Repo key does not belong to selected domain")
	// ErrRouteKeyBoundElsewhere means another Repo already owns the key.
	ErrRouteKeyBoundElsewhere = errors.New("authoritystore: normalized Repo key belongs to another Repo")
	// ErrRouteRepoHasKey means this Repo already owns another normalized key.
	ErrRouteRepoHasKey = errors.New("authoritystore: Repo already has another normalized key")
)

type routeIndexQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// RepoRouteBinding is the authority-derived route identity. Domain and active
// epoch are joined from membership and domain records, never duplicated in the
// normalized-key index.
type RepoRouteBinding struct {
	NormalizedKey string
	RepoID        string
	DomainID      string
	ActiveEpoch   uint64
}

// LookupRepoRoute is a read-only authority lookup. It returns membership and
// active epoch from their authoritative tables in the same snapshot.
func (s *Store) LookupRepoRoute(ctx context.Context, normalizedKey string) (RepoRouteBinding, error) {
	return s.LookupRepoRouteForDomain(ctx, normalizedKey, "", 0)
}

// LookupRepoRouteForDomain performs the route-index, membership, and active
// epoch read in one SQLite snapshot. When a target domain is supplied, foreign
// key ownership is returned as the same opaque error as any other mismatch.
func (s *Store) LookupRepoRouteForDomain(ctx context.Context, normalizedKey, targetDomain string, expectedEpoch uint64) (RepoRouteBinding, error) {
	binding, _, err := s.LookupRepoRouteSnapshotForDomain(ctx, normalizedKey, targetDomain, expectedEpoch)
	return binding, err
}

// LookupRepoRouteSnapshotForDomain returns the route result and its authority
// prefix anchor from one read-only transaction. For an unbound key it still
// returns the selected domain's anchor, allowing a miss response to prove the
// exact authority snapshot without exposing any Repo identity.
func (s *Store) LookupRepoRouteSnapshotForDomain(ctx context.Context, normalizedKey, targetDomain string, expectedEpoch uint64) (RepoRouteBinding, PrefixAnchor, error) {
	var emptyAnchor PrefixAnchor
	if !validNormalizedRepoKey(normalizedKey) {
		return RepoRouteBinding{}, emptyAnchor, ErrInvalidProof
	}
	if targetDomain != "" && (!ulid.MatchString(targetDomain) || expectedEpoch == 0) {
		return RepoRouteBinding{}, emptyAnchor, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return RepoRouteBinding{}, emptyAnchor, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RepoRouteBinding{}, emptyAnchor, err
	}
	defer func() { _ = tx.Rollback() }()
	binding, err := lookupRepoRouteTx(ctx, tx, normalizedKey)
	if errors.Is(err, ErrRouteUnbound) {
		if targetDomain == "" {
			return RepoRouteBinding{}, emptyAnchor, err
		}
		anchor, anchorErr := currentAnchor(ctx, tx, targetDomain)
		if anchorErr != nil {
			return RepoRouteBinding{}, emptyAnchor, anchorErr
		}
		if anchorErr = tx.Commit(); anchorErr != nil {
			return RepoRouteBinding{}, emptyAnchor, anchorErr
		}
		return RepoRouteBinding{}, anchor, err
	}
	if err != nil {
		return RepoRouteBinding{}, emptyAnchor, err
	}
	if targetDomain != "" && (binding.DomainID != targetDomain || binding.ActiveEpoch != expectedEpoch) {
		return RepoRouteBinding{}, emptyAnchor, ErrRouteDomainMismatch
	}
	anchor, err := currentAnchor(ctx, tx, binding.DomainID)
	if err != nil {
		return RepoRouteBinding{}, emptyAnchor, err
	}
	if err = tx.Commit(); err != nil {
		return RepoRouteBinding{}, emptyAnchor, err
	}
	return binding, anchor, nil
}

func lookupRepoRouteTx(ctx context.Context, queryer routeIndexQueryer, normalizedKey string) (RepoRouteBinding, error) {
	var binding RepoRouteBinding
	if !validNormalizedRepoKey(normalizedKey) {
		return binding, ErrInvalidProof
	}
	err := queryer.QueryRowContext(ctx, `SELECT r.normalized_key,r.repo_id,m.domain_id,d.active_epoch
		FROM repo_route_index r
		JOIN repo_memberships m ON m.repo_id=r.repo_id
		JOIN domains d ON d.domain_id=m.domain_id
		WHERE r.normalized_key=?`, normalizedKey).Scan(
		&binding.NormalizedKey, &binding.RepoID, &binding.DomainID, &binding.ActiveEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return RepoRouteBinding{}, ErrRouteUnbound
	}
	if err != nil {
		return RepoRouteBinding{}, err
	}
	if !ulid.MatchString(binding.RepoID) || !ulid.MatchString(binding.DomainID) || binding.ActiveEpoch == 0 {
		return RepoRouteBinding{}, ErrInvalidStore
	}
	return binding, nil
}

// bindRepoRouteTx is called only inside the authority command's transaction,
// so the unique index effect and terminal receipt commit or roll back together.
func bindRepoRouteTx(ctx context.Context, tx *sql.Tx, normalizedKey, repoID, domainID string) error {
	if !validNormalizedRepoKey(normalizedKey) || !ulid.MatchString(repoID) || !ulid.MatchString(domainID) {
		return ErrInvalidProof
	}
	var member int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE repo_id=? AND domain_id=?`, repoID, domainID).Scan(&member); err != nil {
		return err
	}
	if member != 1 {
		return ErrFenced
	}
	var repoForKey string
	err := tx.QueryRowContext(ctx, `SELECT repo_id FROM repo_route_index WHERE normalized_key=?`, normalizedKey).Scan(&repoForKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && repoForKey != repoID {
		return ErrRouteKeyBoundElsewhere
	}
	var keyForRepo string
	err = tx.QueryRowContext(ctx, `SELECT normalized_key FROM repo_route_index WHERE repo_id=?`, repoID).Scan(&keyForRepo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && keyForRepo != normalizedKey {
		return ErrRouteRepoHasKey
	}
	if repoForKey == repoID && keyForRepo == normalizedKey {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO repo_route_index(normalized_key,repo_id) VALUES(?,?)`, normalizedKey, repoID)
	return writeError(err)
}

// adoptRepoRouteTx is stricter than ordinary route binding: adoption is only
// valid while the existing Repo has no remote key. Exact retries are resolved
// by the command receipt before this transaction is reached.
func adoptRepoRouteTx(ctx context.Context, tx *sql.Tx, normalizedKey, repoID, domainID string) error {
	if !validNormalizedRepoKey(normalizedKey) || !ulid.MatchString(repoID) || !ulid.MatchString(domainID) {
		return ErrInvalidProof
	}
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT normalized_key FROM repo_route_index WHERE repo_id=?`, repoID).Scan(&existing)
	if err == nil {
		return ErrRouteRepoHasKey
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return bindRepoRouteTx(ctx, tx, normalizedKey, repoID, domainID)
}
