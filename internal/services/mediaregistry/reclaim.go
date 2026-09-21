package mediaregistry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

const (
	DefaultReclamationGrace    = time.Minute
	DefaultReclamationBatch    = 16
	MaxReclamationBatch        = 128
	reclamationClaimLease      = 2 * time.Minute
	reclamationDeleteTimeout   = 5 * time.Second
	reclamationFinalizeTimeout = 2 * time.Second
	maxReclamationErrorLen     = 1024
	maxReclamationReasonLen    = 256
	reclamationBlockedMessage  = "media reclamation in progress"
)

// Reclaimer executes only explicit, durable owner-authorized delete intents.
// Physical enumeration and a zero durable-reference count are never sufficient
// authorization to delete an asset.
type Reclaimer struct {
	registry      *Registry
	store         storage.Storage
	claimLease    time.Duration
	deleteTimeout time.Duration
}

func NewReclaimer(registry *Registry, store storage.Storage) *Reclaimer {
	return &Reclaimer{
		registry:      registry,
		store:         store,
		claimLease:    reclamationClaimLease,
		deleteTimeout: reclamationDeleteTimeout,
	}
}

func normalizeReclamationRequest(req ReclamationRequest) (ReclamationRequest, error) {
	req.AssetID = strings.TrimSpace(req.AssetID)
	req.Owner = strings.TrimSpace(req.Owner)
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > maxReclamationReasonLen {
		req.Reason = req.Reason[:maxReclamationReasonLen]
	}
	if req.AssetID == "" || req.Owner == "" || !req.Lifecycle.valid() || req.Grace < 0 {
		return ReclamationRequest{}, ErrInvalidReclamation
	}
	if req.Lifecycle == LifecycleLegacy {
		return ReclamationRequest{}, ErrReclamationNotAllowed
	}
	return req, nil
}

func normalizeReclamationLimit(limit int) int {
	if limit <= 0 {
		return DefaultReclamationBatch
	}
	return min(limit, MaxReclamationBatch)
}

func reclamationWriteError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), reclamationBlockedMessage) {
		return fmt.Errorf("%w: durable reference or ownership mutation blocked", ErrReclamationInProgress)
	}
	return err
}

// PrepareReclamation installs a durable crash-recovery guard for newly-created
// bytes. Prepared intents are not executed by normal reconciliation. A durable
// reference insertion cancels them automatically. Startup may explicitly
// activate leftover prepared intents once all reference backfills have run.
func (r *Reclaimer) PrepareReclamation(ctx context.Context, req ReclamationRequest) error {
	return r.schedule(ctx, req, ReclamationPrepared)
}

// RequestReclamation records an explicit owner deletion decision. Grace delays
// eligibility, but does not weaken the owner/lifecycle/reference checks made
// again immediately before the physical delete claim.
func (r *Reclaimer) RequestReclamation(ctx context.Context, req ReclamationRequest) error {
	return r.schedule(ctx, req, ReclamationPending)
}

func (r *Reclaimer) schedule(ctx context.Context, req ReclamationRequest, state ReclamationState) error {
	if r == nil || r.registry == nil || r.registry.db == nil {
		return ErrNilDatabase
	}
	var err error
	req, err = normalizeReclamationRequest(req)
	if err != nil {
		return err
	}
	if state == ReclamationPrepared && req.Lifecycle == LifecycleRetained {
		return ErrReclamationNotAllowed
	}
	ctx = normalizeContext(ctx)

	tx, err := r.registry.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("media registry: begin reclamation authorization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := validateReclamationAuthority(ctx, tx, req); err != nil {
		return err
	}

	var existingState ReclamationState
	var existingOwner string
	var existingLifecycle Lifecycle
	err = tx.QueryRowContext(ctx, `
		SELECT state, expected_owner, expected_lifecycle
		FROM media_reclamation_intents WHERE asset_id = ?
	`, req.AssetID).Scan(&existingState, &existingOwner, &existingLifecycle)
	switch {
	case err == nil:
		if existingState == ReclamationDeleting {
			return fmt.Errorf("%w: asset %q", ErrReclamationInProgress, req.AssetID)
		}
		if existingOwner != req.Owner || existingLifecycle != req.Lifecycle {
			return fmt.Errorf("%w: reclamation intent for asset %q has different authority", ErrReclamationNotAllowed, req.AssetID)
		}
		// An explicit pending request may activate an earlier prepared guard.
		// Existing pending retries keep their attempt/backoff state unless the
		// owner explicitly supplies a new grace deadline earlier than that state.
		if state == ReclamationPending && existingState == ReclamationPrepared {
			now := time.Now().UTC()
			next := now.Add(req.Grace)
			if _, err := tx.ExecContext(ctx, `
				UPDATE media_reclamation_intents
				SET state = ?, reason = ?, not_before = ?, next_attempt_at = ?, updated_at = ?
				WHERE asset_id = ? AND state = ?
			`, ReclamationPending, req.Reason, next, next, now, req.AssetID, ReclamationPrepared); err != nil {
				return fmt.Errorf("media registry: activate prepared reclamation %q: %w", req.AssetID, err)
			}
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("media registry: inspect reclamation intent %q: %w", req.AssetID, err)
	}

	now := time.Now().UTC()
	next := now.Add(req.Grace)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO media_reclamation_intents (
			asset_id, expected_owner, expected_lifecycle, reason, state,
			attempts, last_error, not_before, next_attempt_at,
			claim_token, claimed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, '', ?, ?, '', NULL, ?, ?)
	`, req.AssetID, req.Owner, req.Lifecycle, req.Reason, state, next, next, now, now); err != nil {
		return fmt.Errorf("media registry: create reclamation intent %q: %w", req.AssetID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("media registry: commit reclamation intent %q: %w", req.AssetID, err)
	}
	return nil
}

func validateReclamationAuthority(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, req ReclamationRequest) error {
	var owner string
	var lifecycle Lifecycle
	err := tx.QueryRowContext(ctx, `
		SELECT owner, lifecycle FROM media_assets WHERE asset_id = ?
	`, req.AssetID).Scan(&owner, &lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrAssetNotRegistered, req.AssetID)
	}
	if err != nil {
		return fmt.Errorf("media registry: inspect reclamation authority for asset %q: %w", req.AssetID, err)
	}
	if owner != req.Owner || lifecycle != req.Lifecycle || lifecycle == LifecycleLegacy {
		return fmt.Errorf("%w: asset %q owner=%q lifecycle=%q", ErrReclamationNotAllowed, req.AssetID, owner, lifecycle)
	}
	var references int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM media_asset_references WHERE asset_id = ?
	`, req.AssetID).Scan(&references); err != nil {
		return fmt.Errorf("media registry: inspect references for reclamation %q: %w", req.AssetID, err)
	}
	if references != 0 {
		return fmt.Errorf("%w: asset %q has %d reference(s)", ErrAssetReferenced, req.AssetID, references)
	}
	return nil
}
