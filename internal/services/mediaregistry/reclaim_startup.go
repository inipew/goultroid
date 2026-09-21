package mediaregistry

import (
	"context"
	"fmt"
	"time"
)

// RecoverClaimsAtStartup releases bounded delete claims left by a previous
// process. Unlike runtime lease recovery, startup is a process boundary: no
// previous-process worker can still own the physical delete. Recovery therefore
// does not wait for claimLease before making the intent retryable again.
func (r *Reclaimer) RecoverClaimsAtStartup(ctx context.Context, limit int) (int, error) {
	if r == nil || r.registry == nil || r.registry.db == nil {
		return 0, ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	limit = normalizeReclamationLimit(limit)

	rows, err := r.registry.db.QueryContext(ctx, `
		SELECT asset_id, claim_token, attempts
		FROM media_reclamation_intents
		WHERE state = ?
		ORDER BY COALESCE(claimed_at, updated_at) ASC, asset_id ASC
		LIMIT ?
	`, ReclamationDeleting, limit)
	if err != nil {
		return 0, fmt.Errorf("media registry: list startup reclamation claims: %w", err)
	}
	type claim struct {
		assetID  string
		token    string
		attempts int
	}
	items := make([]claim, 0, limit)
	for rows.Next() {
		var item claim
		if err := rows.Scan(&item.assetID, &item.token, &item.attempts); err != nil {
			_ = rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	recovered := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		now := time.Now().UTC()
		res, err := r.registry.db.ExecContext(ctx, `
			UPDATE media_reclamation_intents
			SET state = ?, attempts = ?, last_error = ?, next_attempt_at = ?,
			    claim_token = '', claimed_at = NULL, updated_at = ?
			WHERE asset_id = ? AND state = ? AND claim_token = ?
		`, ReclamationPending, item.attempts+1, "reclamation claim recovered at startup", now, now,
			item.assetID, ReclamationDeleting, item.token)
		if err != nil {
			return recovered, fmt.Errorf("media registry: recover startup reclamation claim %q: %w", item.assetID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return recovered, err
		}
		recovered += int(affected)
	}
	return recovered, nil
}
