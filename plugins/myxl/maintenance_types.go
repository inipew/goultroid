package myxl

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// MinMaintenanceInterval prevents excessive polling of CIAM endpoints.
	MinMaintenanceInterval = 1 * time.Hour
	// MaxMaintenanceInterval caps the maximum scheduled check interval.
	MaxMaintenanceInterval = 24 * time.Hour

	// MinMaintenanceRefreshWindow is the minimum lead time before expiry.
	MinMaintenanceRefreshWindow = 5 * time.Minute
	// MaxMaintenanceRefreshWindow is the maximum lead time before expiry.
	MaxMaintenanceRefreshWindow = 120 * time.Minute

	// DefaultMaintenanceTimezone is the default anchor timezone.
	DefaultMaintenanceTimezone = "Asia/Jakarta"
	// DefaultMaintenanceAnchorTime is the default daily phase anchor (HH:MM).
	DefaultMaintenanceAnchorTime = "03:00"
	// DefaultMaintenanceInterval is the default schedule check interval.
	DefaultMaintenanceInterval = 6 * time.Hour
	// DefaultMaintenanceRefreshWindow is the default pre-expiry refresh window.
	DefaultMaintenanceRefreshWindow = 30 * time.Minute

	// MaxMaintenanceRunHistory caps persisted run summaries in SQLite.
	MaxMaintenanceRunHistory = 20
	// MaxAccountsPerCycle bounds sequential refresh attempts in one run.
	MaxAccountsPerCycle = 50
)

// AccountScope selects which accounts are eligible for token maintenance.
type AccountScope string

const (
	AccountScopeAll      AccountScope = "all"
	AccountScopeActive   AccountScope = "active"
	AccountScopeSelected AccountScope = "selected"
)

// TokenMaintenanceConfig holds durable user preferences for MyXL token maintenance.
// NextDueAt is owned exclusively by jobs.JobSchedule and is intentionally omitted here.
type TokenMaintenanceConfig struct {
	Enabled                bool                      `json:"enabled"`
	Interval               time.Duration             `json:"interval"`
	AnchorTime             string                    `json:"anchor_time"`
	Timezone               string                    `json:"timezone"`
	RefreshWindow          time.Duration             `json:"refresh_window"`
	AccountScope           AccountScope              `json:"account_scope"`
	SelectedMSISDNs        []string                  `json:"selected_msisdns,omitempty"`
	NotifyOnCompletion     bool                      `json:"notify_on_completion"`
	NotifyOnPartialFailure bool                      `json:"notify_on_partial_failure"`
	NotifyOnFailure        bool                      `json:"notify_on_failure"`
	NotifyOnCancelled      bool                      `json:"notify_on_cancelled"`
	Destinations           []NotificationDestination `json:"destinations,omitempty"`
	UpdatedAt              time.Time                 `json:"updated_at"`
}

// DefaultTokenMaintenanceConfig returns the safe default maintenance configuration.
func DefaultTokenMaintenanceConfig() TokenMaintenanceConfig {
	return TokenMaintenanceConfig{
		Enabled:                false,
		Interval:               DefaultMaintenanceInterval,
		AnchorTime:             DefaultMaintenanceAnchorTime,
		Timezone:               DefaultMaintenanceTimezone,
		RefreshWindow:          DefaultMaintenanceRefreshWindow,
		AccountScope:           AccountScopeAll,
		NotifyOnCompletion:     false,
		NotifyOnPartialFailure: true,
		NotifyOnFailure:        true,
	}
}

// ParseAnchorTime parses a 24-hour HH:MM string into hour and minute.
func ParseAnchorTime(raw string) (int, int, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != 5 || trimmed[2] != ':' {
		return 0, 0, fmt.Errorf("invalid anchor time %q: expected HH:MM", raw)
	}
	t, err := time.Parse("15:04", trimmed)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid anchor time %q: %w", raw, err)
	}
	return t.Hour(), t.Minute(), nil
}

// ValidateTokenMaintenanceConfig validates all fields strictly without falling back to defaults.
func ValidateTokenMaintenanceConfig(cfg TokenMaintenanceConfig) error {
	if cfg.Interval <= 0 {
		return errors.New("maintenance interval must be positive")
	}
	if cfg.Interval < MinMaintenanceInterval || cfg.Interval > MaxMaintenanceInterval {
		return fmt.Errorf("maintenance interval %s must be between %s and %s", cfg.Interval, MinMaintenanceInterval, MaxMaintenanceInterval)
	}
	if cfg.Interval%time.Minute != 0 {
		return errors.New("maintenance interval must be a whole number of minutes")
	}
	if _, _, err := ParseAnchorTime(cfg.AnchorTime); err != nil {
		return err
	}
	tz := strings.TrimSpace(cfg.Timezone)
	if tz == "" {
		return errors.New("maintenance timezone is required")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid maintenance timezone %q: %w", tz, err)
	}
	if cfg.RefreshWindow <= 0 {
		return errors.New("refresh window must be positive")
	}
	if cfg.RefreshWindow < MinMaintenanceRefreshWindow || cfg.RefreshWindow > MaxMaintenanceRefreshWindow {
		return fmt.Errorf("refresh window %s must be between %s and %s", cfg.RefreshWindow, MinMaintenanceRefreshWindow, MaxMaintenanceRefreshWindow)
	}
	if cfg.RefreshWindow >= cfg.Interval {
		return fmt.Errorf("refresh window (%s) must be smaller than interval (%s)", cfg.RefreshWindow, cfg.Interval)
	}
	switch cfg.AccountScope {
	case AccountScopeAll, AccountScopeActive:
	case AccountScopeSelected:
		if len(cfg.SelectedMSISDNs) == 0 {
			return errors.New("selected account scope requires at least one account")
		}
		for _, msisdn := range cfg.SelectedMSISDNs {
			if strings.TrimSpace(msisdn) == "" {
				return errors.New("selected account list contains an empty identifier")
			}
		}
	default:
		return fmt.Errorf("invalid account scope %q (must be all, active, or selected)", cfg.AccountScope)
	}
	if _, err := NormalizeAndValidateDestinations(cfg.Destinations); err != nil {
		return err
	}
	return nil
}

// AccountMaintenanceStatus classifies the explicit outcome for a single account.
type AccountMaintenanceStatus string

const (
	StatusRefreshed              AccountMaintenanceStatus = "refreshed"
	StatusAlreadyFresh           AccountMaintenanceStatus = "already_fresh"
	StatusSkipped                AccountMaintenanceStatus = "skipped"
	StatusMissingRefreshToken    AccountMaintenanceStatus = "missing_refresh_token"
	StatusUnknownExpiry          AccountMaintenanceStatus = "unknown_expiry"
	StatusAuthenticationRejected AccountMaintenanceStatus = "authentication_rejected"
	StatusNetworkError           AccountMaintenanceStatus = "network_error"
	StatusRateLimited            AccountMaintenanceStatus = "rate_limited"
	StatusPersistenceError       AccountMaintenanceStatus = "persistence_error"
	StatusCancelled              AccountMaintenanceStatus = "cancelled"
)

// AccountMaintenanceOutcome captures the non-secret per-account result of a cycle.
type AccountMaintenanceOutcome struct {
	MSISDN string
	Status AccountMaintenanceStatus
	Detail string
}

// MaintenanceRunStatus summarizes the overall outcome of a maintenance cycle.
type MaintenanceRunStatus string

const (
	RunStatusCompleted      MaintenanceRunStatus = "completed"
	RunStatusPartialFailure MaintenanceRunStatus = "partial_failure"
	RunStatusFailed         MaintenanceRunStatus = "failed"
	RunStatusCancelled      MaintenanceRunStatus = "cancelled"
)

// MaintenanceRunSummary records a bounded, non-secret summary of one maintenance execution.
type MaintenanceRunSummary struct {
	ID                int64                `json:"id"`
	TriggerSource     string               `json:"trigger_source"`
	Status            MaintenanceRunStatus `json:"status"`
	StartedAt         time.Time            `json:"started_at"`
	CompletedAt       time.Time            `json:"completed_at"`
	Duration          time.Duration        `json:"duration"`
	TotalAccounts     int                  `json:"total_accounts"`
	RefreshedCount    int                  `json:"refreshed_count"`
	AlreadyFreshCount int                  `json:"already_fresh_count"`
	SkippedCount      int                  `json:"skipped_count"`
	FailedCount       int                  `json:"failed_count"`
	CancelledCount    int                  `json:"cancelled_count"`
	ErrorSummary      string               `json:"error_summary,omitempty"`
}
