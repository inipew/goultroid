package myxl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaintenanceStatusView is the read-only snapshot returned by MaintenanceService.
// It intentionally omits NextDueAt because ScheduleClient does not expose a read API.
type MaintenanceStatusView struct {
	Config  TokenMaintenanceConfig
	Running bool
	LastRun *MaintenanceRunSummary
}

// MaintenanceService is the single configuration and trigger authority shared by
// Telegram commands and interactive a2 menus.
type MaintenanceService struct {
	plugin *Plugin
}

// NewMaintenanceService constructs a MaintenanceService bound to plugin p.
func NewMaintenanceService(p *Plugin) *MaintenanceService {
	return &MaintenanceService{plugin: p}
}

func (s *MaintenanceService) ensureReady() error {
	if s == nil || s.plugin == nil || s.plugin.repo == nil {
		return errors.New("myxl maintenance service is not configured")
	}
	return nil
}

// GetStatus returns the persisted config, in-process running flag, and latest run summary.
func (s *MaintenanceService) GetStatus(ctx context.Context) (*MaintenanceStatusView, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	cfg, err := s.plugin.repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := s.plugin.repo.ListTokenMaintenanceRuns(ctx, 1)
	if err != nil {
		return nil, err
	}
	var lastRun *MaintenanceRunSummary
	if len(runs) > 0 {
		lastRun = runs[0]
	}
	return &MaintenanceStatusView{
		Config:  *cfg,
		Running: s.plugin.maintRunning.Load(),
		LastRun: lastRun,
	}, nil
}

func (s *MaintenanceService) updateConfig(ctx context.Context, mutate func(*TokenMaintenanceConfig) error) (*TokenMaintenanceConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	s.plugin.maintConfigMu.Lock()
	defer s.plugin.maintConfigMu.Unlock()

	cfg, err := s.plugin.repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		return nil, err
	}
	updated := *cfg
	if updated.SelectedMSISDNs != nil {
		updated.SelectedMSISDNs = append([]string(nil), updated.SelectedMSISDNs...)
	}
	if err := mutate(&updated); err != nil {
		return nil, err
	}
	if err := ValidateTokenMaintenanceConfig(updated); err != nil {
		return nil, err
	}
	if err := s.plugin.ApplyMaintenanceConfig(ctx, updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// SetEnabled enables or disables scheduled maintenance without discarding user preferences.
func (s *MaintenanceService) SetEnabled(ctx context.Context, enabled bool) (*TokenMaintenanceConfig, error) {
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		c.Enabled = enabled
		return nil
	})
}

// SetInterval updates the maintenance check interval while preserving all other fields.
func (s *MaintenanceService) SetInterval(ctx context.Context, interval time.Duration) (*TokenMaintenanceConfig, error) {
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		c.Interval = interval
		return nil
	})
}

// SetAnchorTime updates the HH:MM phase anchor while preserving all other fields.
func (s *MaintenanceService) SetAnchorTime(ctx context.Context, anchor string) (*TokenMaintenanceConfig, error) {
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		c.AnchorTime = strings.TrimSpace(anchor)
		return nil
	})
}

// SetTimezone updates the IANA timezone while preserving all other fields.
func (s *MaintenanceService) SetTimezone(ctx context.Context, tz string) (*TokenMaintenanceConfig, error) {
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		c.Timezone = strings.TrimSpace(tz)
		return nil
	})
}

// SetRefreshWindow updates the pre-expiry refresh window while preserving all other fields.
func (s *MaintenanceService) SetRefreshWindow(ctx context.Context, window time.Duration) (*TokenMaintenanceConfig, error) {
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		c.RefreshWindow = window
		return nil
	})
}

// SetAccountScope updates the target account scope. When scope is AccountScopeSelected,
// all identifiers are resolved and verified against saved accounts in the repository.
func (s *MaintenanceService) SetAccountScope(ctx context.Context, scope AccountScope, selected []string) (*TokenMaintenanceConfig, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	var resolved []string
	if scope == AccountScopeSelected {
		if len(selected) == 0 {
			return nil, errors.New("selected account scope requires at least one account")
		}
		seen := make(map[string]struct{}, len(selected))
		for _, raw := range selected {
			id := strings.TrimSpace(raw)
			if norm, err := NormalizeMSISDN(id); err == nil {
				id = norm
			}
			acc, err := s.plugin.repo.GetByMSISDN(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("lookup account: %w", err)
			}
			if acc == nil {
				return nil, fmt.Errorf("akun %s tidak terdaftar", MaskMSISDN(id))
			}
			if _, exists := seen[acc.MSISDN]; !exists {
				seen[acc.MSISDN] = struct{}{}
				resolved = append(resolved, acc.MSISDN)
			}
		}
	}
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		c.AccountScope = scope
		c.SelectedMSISDNs = resolved
		return nil
	})
}

// SetNotifyPreferences updates owner notification preferences while preserving all other fields.
func (s *MaintenanceService) SetNotifyPreferences(ctx context.Context, onCompletion, onFailure *bool) (*TokenMaintenanceConfig, error) {
	return s.updateConfig(ctx, func(c *TokenMaintenanceConfig) error {
		if onCompletion != nil {
			c.NotifyOnCompletion = *onCompletion
		}
		if onFailure != nil {
			c.NotifyOnFailure = *onFailure
		}
		return nil
	})
}

// TriggerRun requests an immediate maintenance execution through JobClient.Trigger.
func (s *MaintenanceService) TriggerRun(ctx context.Context) error {
	if err := s.ensureReady(); err != nil {
		return err
	}
	return s.plugin.TriggerMaintenanceNow(ctx)
}

// GetHistory returns up to limit bounded maintenance run summaries.
func (s *MaintenanceService) GetHistory(ctx context.Context, limit int) ([]*MaintenanceRunSummary, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	return s.plugin.repo.ListTokenMaintenanceRuns(ctx, limit)
}
