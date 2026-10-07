package authoritystore

import (
	"context"
	"database/sql"
	"sort"

	"github.com/procrastivity/wip/internal/operation"
)

func targetClaimsForInput(input operation.Input) []operation.TargetClaim {
	switch input := input.(type) {
	case operation.DependencyAddV2Input:
		return input.TargetClaims
	case operation.DependencyRemoveV2Input:
		return input.TargetClaims
	case operation.ReferenceBindV2Input:
		return input.TargetClaims
	case operation.ReferenceUnbindV2Input:
		return input.TargetClaims
	case operation.ReferenceRebindV2Input:
		return input.TargetClaims
	default:
		return nil
	}
}

// validateTargetClaimsTx fences the complete authority-derived set in the same
// transaction that appends the command event and retained terminal receipt.
func validateTargetClaimsTx(ctx context.Context, tx *sql.Tx, command operation.Command, targets []string) (bool, error) {
	targets = append([]string(nil), targets...)
	sort.Strings(targets)
	claims := targetClaimsForInput(command.Request.Input)
	if len(claims) != len(targets) {
		return false, nil
	}
	for index, target := range targets {
		proof := claims[index]
		if proof.MatterID != target {
			return false, nil
		}
		var matter, owner, state string
		var claimEpoch, authorityEpoch uint64
		var closed sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT c.matter_id,c.owner_environment_id,c.claim_epoch,c.authority_epoch,c.close_command_id,j.state
			FROM claims c JOIN claim_journals j USING(domain_id,claim_id) JOIN domains d USING(domain_id)
			WHERE c.domain_id=? AND c.claim_id=? AND d.active_epoch=?`,
			command.AuthorityDomainID, proof.ClaimID, command.ExpectedAuthorityEpoch).
			Scan(&matter, &owner, &claimEpoch, &authorityEpoch, &closed, &state)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if matter != target || owner != command.EnvironmentID || proof.ClaimEpoch != claimEpoch ||
			authorityEpoch != command.ExpectedAuthorityEpoch || closed.Valid || state != "open" {
			return false, nil
		}
	}
	return true, nil
}
