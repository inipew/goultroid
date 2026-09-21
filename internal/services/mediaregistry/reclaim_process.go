package mediaregistry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

// Reconcile processes a bounded set of due owner-authorized intents. Physical
// failures are durably deferred with backoff and do not fail the whole batch;
// database/claim bookkeeping failures do because they can invalidate safety.
func (r *Reclaimer) Reconcile(ctx context.Context, limit int) (ReclamationStats, error) {
	var stats ReclamationStats
	if r == nil || r.registry == nil || r.registry.db == nil {
		return stats, ErrNilDatabase
	}
	if r.store == nil {
		return stats, fmt.Errorf("media registry: reclamation storage is nil")
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	limit = normalizeReclamationLimit(limit)

	recovered, err := r.recoverExpiredClaims(ctx, limit)
	if err != nil {
		return stats, err
	}
	stats.RecoveredClaims = recovered

	intents, err := r.dueIntents(ctx, limit)
	if err != nil {
		return stats, err
	}
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.Scanned++
		result, err := r.processIntent(ctx, intent)
		if err != nil {
			return stats, err
		}
		if result.claimed {
			stats.Claimed++
		}
		if result.deleted {
			stats.Deleted++
		}
		if result.missing {
			stats.AlreadyMissing++
		}
		if result.deferred {
			stats.Deferred++
		}
		if result.cancelled {
			stats.UnsafeCancelled++
		}
		if errors.Is(result.cause, context.Canceled) || errors.Is(result.cause, context.DeadlineExceeded) {
			return stats, result.cause
		}
	}
	return stats, nil
}

func (r *Reclaimer) dueIntents(ctx context.Context, limit int) ([]ReclamationIntent, error) {
	rows, err := r.registry.db.QueryContext(ctx, `
		SELECT asset_id, expected_owner, expected_lifecycle, reason, state,
		       attempts, last_error, not_before, next_attempt_at,
		       claim_token, claimed_at, created_at, updated_at
		FROM media_reclamation_intents
		WHERE state = ? AND next_attempt_at <= ?
		ORDER BY next_attempt_at ASC, created_at ASC, asset_id ASC
		LIMIT ?
	`, ReclamationPending, time.Now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("media registry: list due reclamation intents: %w", err)
	}
	defer rows.Close()
	items := make([]ReclamationIntent, 0, limit)
	for rows.Next() {
		var item ReclamationIntent
		var claimed sql.NullTime
		if err := rows.Scan(
			&item.AssetID,
			&item.ExpectedOwner,
			&item.ExpectedLifecycle,
			&item.Reason,
			&item.State,
			&item.Attempts,
			&item.LastError,
			&item.NotBefore,
			&item.NextAttemptAt,
			&item.ClaimToken,
			&claimed,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if claimed.Valid {
			value := claimed.Time
			item.ClaimedAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *Reclaimer) recoverExpiredClaims(ctx context.Context, limit int) (int, error) {
	lease := r.claimLease
	if lease <= 0 {
		lease = reclamationClaimLease
	}
	cutoff := time.Now().UTC().Add(-lease)
	rows, err := r.registry.db.QueryContext(ctx, `
		SELECT asset_id, claim_token, attempts
		FROM media_reclamation_intents
		WHERE state = ? AND claimed_at IS NOT NULL AND claimed_at <= ?
		ORDER BY claimed_at ASC, asset_id ASC
		LIMIT ?
	`, ReclamationDeleting, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("media registry: list expired reclamation claims: %w", err)
	}
	type expired struct {
		assetID  string
		token    string
		attempts int
	}
	items := make([]expired, 0, limit)
	for rows.Next() {
		var item expired
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
		now := time.Now().UTC()
		res, err := r.registry.db.ExecContext(ctx, `
			UPDATE media_reclamation_intents
			SET state = ?, attempts = ?, last_error = ?, next_attempt_at = ?,
			    claim_token = '', claimed_at = NULL, updated_at = ?
			WHERE asset_id = ? AND state = ? AND claim_token = ?
		`, ReclamationPending, item.attempts+1, "reclamation claim lease expired", now, now,
			item.assetID, ReclamationDeleting, item.token)
		if err != nil {
			return recovered, fmt.Errorf("media registry: recover reclamation claim %q: %w", item.assetID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return recovered, err
		}
		recovered += int(affected)
	}
	return recovered, nil
}

type reclamationProcessResult struct {
	claimed   bool
	deleted   bool
	missing   bool
	deferred  bool
	cancelled bool
	cause     error
}

func (r *Reclaimer) processIntent(ctx context.Context, intent ReclamationIntent) (reclamationProcessResult, error) {
	var result reclamationProcessResult
	claimToken, err := newClaimToken()
	if err != nil {
		return result, err
	}
	claimed, cancelled, err := r.claim(ctx, intent, claimToken)
	if err != nil {
		return result, err
	}
	if cancelled {
		result.cancelled = true
		return result, nil
	}
	if !claimed {
		result.deferred = true
		return result, nil
	}
	result.claimed = true

	deleteTimeout := r.deleteTimeout
	if deleteTimeout <= 0 {
		deleteTimeout = reclamationDeleteTimeout
	}
	deleteCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	deleteErr := r.store.Delete(deleteCtx, intent.AssetID)
	cancel()

	finishCtx, finishCancel := detachedReclamationContext(ctx)
	defer finishCancel()

	if deleteErr == nil || errors.Is(deleteErr, storage.ErrNotFound) {
		if err := r.finalizeClaim(finishCtx, intent, claimToken); err != nil {
			if releaseErr := r.releaseClaimFailure(finishCtx, intent, claimToken, err); releaseErr != nil {
				return result, errors.Join(err, releaseErr)
			}
			result.deferred = true
			return result, err
		}
		if errors.Is(deleteErr, storage.ErrNotFound) {
			result.missing = true
		} else {
			result.deleted = true
		}
		return result, nil
	}

	if err := r.releaseClaimFailure(finishCtx, intent, claimToken, deleteErr); err != nil {
		return result, errors.Join(deleteErr, err)
	}
	result.deferred = true
	result.cause = deleteErr
	return result, nil
}

func (r *Reclaimer) claim(ctx context.Context, intent ReclamationIntent, token string) (claimed, cancelled bool, err error) {
	now := time.Now().UTC()
	res, err := r.registry.db.ExecContext(ctx, `
		UPDATE media_reclamation_intents
		SET state = ?, claim_token = ?, claimed_at = ?, updated_at = ?
		WHERE asset_id = ?
		  AND state = ?
		  AND next_attempt_at <= ?
		  AND expected_owner = ?
		  AND expected_lifecycle = ?
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
	`, ReclamationDeleting, token, now, now, intent.AssetID, ReclamationPending, now,
		intent.ExpectedOwner, intent.ExpectedLifecycle, LifecycleLegacy)
	if err != nil {
		return false, false, fmt.Errorf("media registry: claim reclamation %q: %w", intent.AssetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, false, err
	}
	if affected == 1 {
		return true, false, nil
	}
	cancelled, err = r.cancelUnsafeIntent(ctx, intent.AssetID, ReclamationPending)
	return false, cancelled, err
}

func (r *Reclaimer) cancelUnsafeIntent(ctx context.Context, assetID string, state ReclamationState) (bool, error) {
	res, err := r.registry.db.ExecContext(ctx, `
		DELETE FROM media_reclamation_intents
		WHERE asset_id = ? AND state = ?
		  AND (
			expected_lifecycle = ?
			OR (state = ? AND expected_lifecycle = ?)
			OR NOT EXISTS (
				SELECT 1 FROM media_assets m
				WHERE m.asset_id = media_reclamation_intents.asset_id
				  AND m.owner = media_reclamation_intents.expected_owner
				  AND m.lifecycle = media_reclamation_intents.expected_lifecycle
			)
			OR EXISTS (
				SELECT 1 FROM media_asset_references mr
				WHERE mr.asset_id = media_reclamation_intents.asset_id
			)
		  )
	`, assetID, state, LifecycleLegacy, ReclamationPrepared, LifecycleRetained)
	if err != nil {
		return false, fmt.Errorf("media registry: cancel unsafe reclamation %q: %w", assetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (r *Reclaimer) finalizeClaim(ctx context.Context, intent ReclamationIntent, token string) error {
	tx, err := r.registry.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("media registry: begin reclamation finalize %q: %w", intent.AssetID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentToken string
	var currentState ReclamationState
	err = tx.QueryRowContext(ctx, `
		SELECT state, claim_token FROM media_reclamation_intents WHERE asset_id = ?
	`, intent.AssetID).Scan(&currentState, &currentToken)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: asset %q", ErrReclamationIntentGone, intent.AssetID)
	}
	if err != nil {
		return err
	}
	if currentState != ReclamationDeleting || currentToken != token {
		return fmt.Errorf("%w: claim for asset %q changed", ErrReclamationInProgress, intent.AssetID)
	}

	var owner string
	var lifecycle Lifecycle
	err = tx.QueryRowContext(ctx, `SELECT owner, lifecycle FROM media_assets WHERE asset_id = ?`, intent.AssetID).Scan(&owner, &lifecycle)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if owner != intent.ExpectedOwner || lifecycle != intent.ExpectedLifecycle || lifecycle == LifecycleLegacy {
			return fmt.Errorf("%w: asset %q authority changed during delete", ErrReclamationNotAllowed, intent.AssetID)
		}
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM media_asset_references WHERE asset_id = ?`, intent.AssetID).Scan(&references); err != nil {
			return err
		}
		if references != 0 {
			return fmt.Errorf("%w: asset %q was re-referenced during delete", ErrAssetReferenced, intent.AssetID)
		}
	}
	res, err := tx.ExecContext(ctx, `
		DELETE FROM media_reclamation_intents
		WHERE asset_id = ? AND state = ? AND claim_token = ?
	`, intent.AssetID, ReclamationDeleting, token)
	if err != nil {
		return fmt.Errorf("media registry: remove completed reclamation intent %q: %w", intent.AssetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: asset %q finalize", ErrReclamationIntentGone, intent.AssetID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_assets WHERE asset_id = ?`, intent.AssetID); err != nil {
		return fmt.Errorf("media registry: remove reclaimed asset metadata %q: %w", intent.AssetID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("media registry: commit reclamation finalize %q: %w", intent.AssetID, err)
	}
	return nil
}

func (r *Reclaimer) releaseClaimFailure(ctx context.Context, intent ReclamationIntent, token string, cause error) error {
	attempts := intent.Attempts + 1
	message := ""
	if cause != nil {
		message = cause.Error()
		if len(message) > maxReclamationErrorLen {
			message = message[:maxReclamationErrorLen]
		}
	}
	now := time.Now().UTC()
	next := now.Add(reclamationRetryDelay(attempts))
	res, err := r.registry.db.ExecContext(ctx, `
		UPDATE media_reclamation_intents
		SET state = ?, attempts = ?, last_error = ?, next_attempt_at = ?,
		    claim_token = '', claimed_at = NULL, updated_at = ?
		WHERE asset_id = ? AND state = ? AND claim_token = ?
	`, ReclamationPending, attempts, message, next, now, intent.AssetID, ReclamationDeleting, token)
	if err != nil {
		return fmt.Errorf("media registry: release failed reclamation claim %q: %w", intent.AssetID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: asset %q failure release", ErrReclamationIntentGone, intent.AssetID)
	}
	return nil
}

func reclamationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, 9)
	delay := 5 * time.Second * time.Duration(1<<shift)
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func detachedReclamationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), reclamationFinalizeTimeout)
}

func newClaimToken() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("media registry: generate reclamation claim token: %w", err)
	}
	return hex.EncodeToString(data), nil
}
