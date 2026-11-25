package myxl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetTokenMaintenanceConfig returns the persisted maintenance configuration, or
// DefaultTokenMaintenanceConfig when no configuration has been saved yet.
func (r *SQLiteRepository) GetTokenMaintenanceConfig(ctx context.Context) (*TokenMaintenanceConfig, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository database is nil")
	}
	query := `
	SELECT enabled, interval_seconds, anchor_time, timezone, refresh_window_seconds,
	       account_scope, selected_msisdns_json, notify_on_completion, notify_on_partial_failure,
	       notify_on_failure, notify_on_cancelled, destinations_json, updated_at
	FROM myxl_token_maintenance_config
	WHERE id = 1
	LIMIT 1
	`
	var enabledInt, notifyCompletionInt, notifyPartialInt, notifyFailureInt, notifyCancelledInt int
	var intervalSec, windowSec int64
	var anchorTime, timezone, scope, selectedJSON, destinationsJSON string
	var updatedAt time.Time

	err := r.db.QueryRowContext(ctx, query).Scan(
		&enabledInt, &intervalSec, &anchorTime, &timezone, &windowSec,
		&scope, &selectedJSON, &notifyCompletionInt, &notifyPartialInt,
		&notifyFailureInt, &notifyCancelledInt, &destinationsJSON, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			def := DefaultTokenMaintenanceConfig()
			return &def, nil
		}
		return nil, fmt.Errorf("get token maintenance config: %w", err)
	}

	var selected []string
	if selectedJSON != "" {
		if err := json.Unmarshal([]byte(selectedJSON), &selected); err != nil {
			return nil, fmt.Errorf("decode selected msisdns: %w", err)
		}
	}

	destinations, err := DecodeNotificationDestinations(destinationsJSON)
	if err != nil {
		return nil, err
	}

	return &TokenMaintenanceConfig{
		Enabled:                enabledInt == 1,
		Interval:               time.Duration(intervalSec) * time.Second,
		AnchorTime:             anchorTime,
		Timezone:               timezone,
		RefreshWindow:          time.Duration(windowSec) * time.Second,
		AccountScope:           AccountScope(scope),
		SelectedMSISDNs:        selected,
		NotifyOnCompletion:     notifyCompletionInt == 1,
		NotifyOnPartialFailure: notifyPartialInt == 1,
		NotifyOnFailure:        notifyFailureInt == 1,
		NotifyOnCancelled:      notifyCancelledInt == 1,
		Destinations:           destinations,
		UpdatedAt:              updatedAt,
	}, nil
}

// SaveTokenMaintenanceConfig validates and atomically persists the singleton maintenance config.
func (r *SQLiteRepository) SaveTokenMaintenanceConfig(ctx context.Context, cfg *TokenMaintenanceConfig) error {
	if r == nil || r.db == nil {
		return errors.New("repository database is nil")
	}
	if cfg == nil {
		return errors.New("token maintenance config is required")
	}
	if err := ValidateTokenMaintenanceConfig(*cfg); err != nil {
		return err
	}

	selected := cfg.SelectedMSISDNs
	if cfg.AccountScope != AccountScopeSelected {
		selected = nil
	}
	if selected == nil {
		selected = []string{}
	}
	selectedJSON, err := json.Marshal(selected)
	if err != nil {
		return fmt.Errorf("encode selected msisdns: %w", err)
	}

	destinationsJSON, err := EncodeNotificationDestinations(cfg.Destinations)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	cfg.UpdatedAt = now

	enabledInt := 0
	if cfg.Enabled {
		enabledInt = 1
	}
	notifyCompletionInt := 0
	if cfg.NotifyOnCompletion {
		notifyCompletionInt = 1
	}
	notifyPartialInt := 0
	if cfg.NotifyOnPartialFailure {
		notifyPartialInt = 1
	}
	notifyFailureInt := 0
	if cfg.NotifyOnFailure {
		notifyFailureInt = 1
	}
	notifyCancelledInt := 0
	if cfg.NotifyOnCancelled {
		notifyCancelledInt = 1
	}

	query := `
	INSERT INTO myxl_token_maintenance_config (
		id, enabled, interval_seconds, anchor_time, timezone, refresh_window_seconds,
		account_scope, selected_msisdns_json, notify_on_completion, notify_on_partial_failure,
		notify_on_failure, notify_on_cancelled, destinations_json, updated_at
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		enabled = excluded.enabled,
		interval_seconds = excluded.interval_seconds,
		anchor_time = excluded.anchor_time,
		timezone = excluded.timezone,
		refresh_window_seconds = excluded.refresh_window_seconds,
		account_scope = excluded.account_scope,
		selected_msisdns_json = excluded.selected_msisdns_json,
		notify_on_completion = excluded.notify_on_completion,
		notify_on_partial_failure = excluded.notify_on_partial_failure,
		notify_on_failure = excluded.notify_on_failure,
		notify_on_cancelled = excluded.notify_on_cancelled,
		destinations_json = excluded.destinations_json,
		updated_at = excluded.updated_at
	`
	_, err = r.db.ExecContext(ctx, query,
		enabledInt, int64(cfg.Interval.Seconds()), cfg.AnchorTime, cfg.Timezone,
		int64(cfg.RefreshWindow.Seconds()), string(cfg.AccountScope), string(selectedJSON),
		notifyCompletionInt, notifyPartialInt, notifyFailureInt, notifyCancelledInt, destinationsJSON, now,
	)
	if err != nil {
		return fmt.Errorf("save token maintenance config: %w", err)
	}
	return nil
}

// RecordTokenMaintenanceRun inserts a bounded run summary and prunes history beyond MaxMaintenanceRunHistory.
func (r *SQLiteRepository) RecordTokenMaintenanceRun(ctx context.Context, run *MaintenanceRunSummary) error {
	if r == nil || r.db == nil {
		return errors.New("repository database is nil")
	}
	if run == nil {
		return errors.New("token maintenance run summary is required")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record maintenance run tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO myxl_token_maintenance_runs (
			trigger_source, status, started_at, completed_at, duration_ms,
			total_accounts, refreshed_count, already_fresh_count, skipped_count,
			failed_count, cancelled_count, error_summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		run.TriggerSource, string(run.Status), run.StartedAt.UTC(), run.CompletedAt.UTC(),
		run.Duration.Milliseconds(), run.TotalAccounts, run.RefreshedCount,
		run.AlreadyFreshCount, run.SkippedCount, run.FailedCount, run.CancelledCount,
		run.ErrorSummary,
	)
	if err != nil {
		return fmt.Errorf("insert maintenance run: %w", err)
	}
	if id, idErr := res.LastInsertId(); idErr == nil {
		run.ID = id
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM myxl_token_maintenance_runs
		WHERE id NOT IN (
			SELECT id FROM myxl_token_maintenance_runs
			ORDER BY id DESC
			LIMIT ?
		)
	`, MaxMaintenanceRunHistory); err != nil {
		return fmt.Errorf("prune maintenance run history: %w", err)
	}

	return tx.Commit()
}

// ListTokenMaintenanceRuns returns up to limit most recent maintenance run summaries.
func (r *SQLiteRepository) ListTokenMaintenanceRuns(ctx context.Context, limit int) ([]*MaintenanceRunSummary, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository database is nil")
	}
	if limit <= 0 || limit > MaxMaintenanceRunHistory {
		limit = MaxMaintenanceRunHistory
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, trigger_source, status, started_at, completed_at, duration_ms,
		       total_accounts, refreshed_count, already_fresh_count, skipped_count,
		       failed_count, cancelled_count, error_summary
		FROM myxl_token_maintenance_runs
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list maintenance runs: %w", err)
	}
	defer rows.Close()

	var runs []*MaintenanceRunSummary
	for rows.Next() {
		var item MaintenanceRunSummary
		var statusStr string
		var durationMS int64
		if err := rows.Scan(
			&item.ID, &item.TriggerSource, &statusStr, &item.StartedAt, &item.CompletedAt,
			&durationMS, &item.TotalAccounts, &item.RefreshedCount, &item.AlreadyFreshCount,
			&item.SkippedCount, &item.FailedCount, &item.CancelledCount, &item.ErrorSummary,
		); err != nil {
			return nil, fmt.Errorf("scan maintenance run: %w", err)
		}
		item.Status = MaintenanceRunStatus(statusStr)
		item.Duration = time.Duration(durationMS) * time.Millisecond
		runs = append(runs, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maintenance runs: %w", err)
	}
	return runs, nil
}
