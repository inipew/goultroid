package mediaregistry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DiscoverReclamations converts explicit owner/lifecycle policies into durable
// intents for registered assets that satisfy the policy age and have no durable
// references. The policy is the authorization; zero references is only a
// last-mile safety condition. Discovery is bounded and never accepts legacy or
// retained lifecycle policies.
func (r *Reclaimer) DiscoverReclamations(
	ctx context.Context,
	policies []ReclamationPolicy,
	limit int,
) (int, error) {
	if r == nil || r.registry == nil || r.registry.db == nil {
		return 0, ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	limit = normalizeReclamationLimit(limit)
	if len(policies) == 0 {
		return 0, nil
	}

	normalized := make([]ReclamationPolicy, 0, len(policies))
	for _, policy := range policies {
		policy.Owner = strings.TrimSpace(policy.Owner)
		policy.Reason = strings.TrimSpace(policy.Reason)
		if len(policy.Reason) > maxReclamationReasonLen {
			policy.Reason = policy.Reason[:maxReclamationReasonLen]
		}
		if policy.Owner == "" || !policy.Lifecycle.valid() || policy.MinimumAge < 0 {
			return 0, ErrInvalidReclamation
		}
		if policy.Lifecycle == LifecycleLegacy || policy.Lifecycle == LifecycleRetained {
			return 0, ErrReclamationNotAllowed
		}
		normalized = append(normalized, policy)
	}

	discovered := 0
	quota := max(1, limit/len(normalized))
	for _, policy := range normalized {
		remaining := limit - discovered
		if remaining <= 0 {
			break
		}
		n, err := r.discoverPolicy(ctx, policy, min(quota, remaining))
		if err != nil {
			return discovered, err
		}
		discovered += n
	}
	for discovered < limit {
		progress := 0
		for _, policy := range normalized {
			remaining := limit - discovered
			if remaining <= 0 {
				break
			}
			n, err := r.discoverPolicy(ctx, policy, remaining)
			if err != nil {
				return discovered, err
			}
			discovered += n
			progress += n
		}
		if progress == 0 {
			break
		}
	}
	return discovered, nil
}

func (r *Reclaimer) discoverPolicy(ctx context.Context, policy ReclamationPolicy, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-policy.MinimumAge)
	rows, err := r.registry.db.QueryContext(ctx, `
		SELECT m.asset_id
		FROM media_assets m
		WHERE m.owner = ? AND m.lifecycle = ? AND m.registered_at <= ?
		  AND NOT EXISTS (
			SELECT 1 FROM media_asset_references mr WHERE mr.asset_id = m.asset_id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM media_reclamation_intents ri WHERE ri.asset_id = m.asset_id
		  )
		ORDER BY m.registered_at ASC, m.asset_id ASC
		LIMIT ?
	`, policy.Owner, policy.Lifecycle, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("media registry: discover %s/%s reclamation candidates: %w", policy.Owner, policy.Lifecycle, err)
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, assetID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	inserted := 0
	for _, assetID := range ids {
		err := r.RequestReclamation(ctx, ReclamationRequest{
			AssetID:   assetID,
			Owner:     policy.Owner,
			Lifecycle: policy.Lifecycle,
			Reason:    policy.Reason,
		})
		switch {
		case err == nil:
			inserted++
		case errors.Is(err, ErrAssetReferenced),
			errors.Is(err, ErrAssetNotRegistered),
			errors.Is(err, ErrReclamationInProgress),
			errors.Is(err, ErrReclamationNotAllowed):
			// A concurrent reference/ownership transition won the race. Fail
			// closed for this candidate and continue the bounded pass.
			continue
		default:
			return inserted, err
		}
	}
	return inserted, nil
}

// ReclaimNow records an immediate explicit deletion decision and processes only
// that asset. It is intended for owner paths that already decided to delete;
// crash-discovered assets should use prepared/startup reconciliation instead.
func (r *Reclaimer) ReclaimNow(ctx context.Context, req ReclamationRequest) error {
	req.Grace = 0
	if err := r.RequestReclamation(ctx, req); err != nil {
		return err
	}
	intent, err := r.Intent(ctx, req.AssetID)
	if err != nil {
		return err
	}
	result, err := r.processIntent(ctx, intent)
	if err != nil {
		return err
	}
	return result.cause
}

// ActivatePreparedAtStartup converts bounded leftover producer guards into
// pending intents after startup reference migration/backfill has completed.
// Startup is quiescent with respect to the process that created the guard, so
// the old in-process grace window is deliberately collapsed to now.
func (r *Reclaimer) ActivatePreparedAtStartup(ctx context.Context, limit int) (activated, cancelled int, err error) {
	if r == nil || r.registry == nil || r.registry.db == nil {
		return 0, 0, ErrNilDatabase
	}
	ctx = normalizeContext(ctx)
	limit = normalizeReclamationLimit(limit)
	rows, err := r.registry.db.QueryContext(ctx, `
		SELECT asset_id, expected_owner, expected_lifecycle
		FROM media_reclamation_intents
		WHERE state = ?
		ORDER BY created_at ASC, asset_id ASC
		LIMIT ?
	`, ReclamationPrepared, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("media registry: list prepared reclamation intents: %w", err)
	}
	type candidate struct {
		assetID   string
		owner     string
		lifecycle Lifecycle
	}
	items := make([]candidate, 0, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.assetID, &item.owner, &item.lifecycle); err != nil {
			_ = rows.Close()
			return activated, cancelled, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return activated, cancelled, err
	}
	if err := rows.Close(); err != nil {
		return activated, cancelled, err
	}

	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return activated, cancelled, err
		}
		now := time.Now().UTC()
		res, err := r.registry.db.ExecContext(ctx, `
			UPDATE media_reclamation_intents
			SET state = ?, not_before = ?, next_attempt_at = ?, updated_at = ?
			WHERE asset_id = ? AND state = ?
			  AND expected_owner = ? AND expected_lifecycle = ?
			  AND expected_lifecycle <> ?
			  AND expected_lifecycle <> ?
			  AND EXISTS (
				SELECT 1 FROM media_assets m
				WHERE m.asset_id = media_reclamation_intents.asset_id
				  AND m.owner = media_reclamation_intents.expected_owner
				  AND m.lifecycle = media_reclamation_intents.expected_lifecycle
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM media_asset_references mr
				WHERE mr.asset_id = media_reclamation_intents.asset_id
			  )
		`, ReclamationPending, now, now, now, item.assetID, ReclamationPrepared, item.owner, item.lifecycle, LifecycleLegacy, LifecycleRetained)
		if err != nil {
			return activated, cancelled, fmt.Errorf("media registry: activate prepared reclamation %q: %w", item.assetID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return activated, cancelled, err
		}
		if affected == 1 {
			activated++
			continue
		}
		removed, err := r.cancelUnsafeIntent(ctx, item.assetID, ReclamationPrepared)
		if err != nil {
			return activated, cancelled, err
		}
		if removed {
			cancelled++
		}
	}
	return activated, cancelled, nil
}

// Intent returns the current durable intent for diagnostics and owner-specific
// reclamation. It does not authorize deletion by itself.
func (r *Reclaimer) Intent(ctx context.Context, assetID string) (ReclamationIntent, error) {
	if r == nil || r.registry == nil || r.registry.db == nil {
		return ReclamationIntent{}, ErrNilDatabase
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return ReclamationIntent{}, ErrInvalidReclamation
	}
	ctx = normalizeContext(ctx)
	return scanReclamationIntent(r.registry.db.QueryRowContext(ctx, `
		SELECT asset_id, expected_owner, expected_lifecycle, reason, state,
		       attempts, last_error, not_before, next_attempt_at,
		       claim_token, claimed_at, created_at, updated_at
		FROM media_reclamation_intents WHERE asset_id = ?
	`, assetID))
}

func scanReclamationIntent(row *sql.Row) (ReclamationIntent, error) {
	var intent ReclamationIntent
	var claimed sql.NullTime
	err := row.Scan(
		&intent.AssetID,
		&intent.ExpectedOwner,
		&intent.ExpectedLifecycle,
		&intent.Reason,
		&intent.State,
		&intent.Attempts,
		&intent.LastError,
		&intent.NotBefore,
		&intent.NextAttemptAt,
		&intent.ClaimToken,
		&claimed,
		&intent.CreatedAt,
		&intent.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ReclamationIntent{}, ErrReclamationIntentGone
	}
	if err != nil {
		return ReclamationIntent{}, fmt.Errorf("media registry: load reclamation intent: %w", err)
	}
	if claimed.Valid {
		value := claimed.Time
		intent.ClaimedAt = &value
	}
	return intent, nil
}
