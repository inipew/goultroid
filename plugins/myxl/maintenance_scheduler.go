package myxl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	maintenanceJobID      = "token_maintenance"
	maintenanceJobTimeout = 10 * time.Minute
)

// ErrMaintenanceAlreadyRunning indicates a maintenance cycle is currently active.
var ErrMaintenanceAlreadyRunning = errors.New("token maintenance cycle is already running")

// ErrScheduleSyncRollbackFailed indicates both schedule sync and config rollback failed.
var ErrScheduleSyncRollbackFailed = errors.New("schedule sync failed and config rollback also failed")

func (p *Plugin) initMaintenanceScheduling(pctx plugin.PluginContext) error {
	jobClient, err := pctx.Jobs()
	if err != nil {
		return fmt.Errorf("initialize MyXL jobs client: %w", err)
	}
	schedClient, err := pctx.Schedules()
	if err != nil {
		return fmt.Errorf("initialize MyXL schedule client: %w", err)
	}

	if err := p.registerMaintenanceJob(jobClient); err != nil {
		return err
	}

	p.maintMu.Lock()
	p.jobsClient = jobClient
	p.scheduleClient = schedClient
	p.maintMu.Unlock()

	// Never rewrite next_due_at on restart when maintenance is enabled; JobSchedule
	// in job_schedules is the sole authority for due timing and misfire catch-up.
	// If maintenance is disabled in MyXL config, ensure the schedule is disabled
	// defensively in case a previous disable failed after saving config.
	if p.repo != nil {
		cfg, cfgErr := p.repo.GetTokenMaintenanceConfig(pctx)
		if cfgErr == nil && cfg != nil && !cfg.Enabled {
			_ = schedClient.DisableSchedule(pctx, maintenanceJobID)
		}
	}
	return nil
}

func (p *Plugin) registerMaintenanceJob(jobClient plugin.JobClient) error {
	if err := jobClient.RegisterHandler(maintenanceJobID, p.handleTokenMaintenanceJob); err != nil {
		if !strings.Contains(err.Error(), "already registered") {
			return fmt.Errorf("register MyXL maintenance handler: %w", err)
		}
	}

	def := jobs.JobDefinition{
		ID:          maintenanceJobID,
		HandlerType: maintenanceJobID,
		Pool:        "scheduler",
		Class:       string(tasks.PriorityMaintenance),
		Timeout:     maintenanceJobTimeout,
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1},
		Enabled:     true,
	}
	if err := jobClient.Register(def); err != nil {
		if !strings.Contains(err.Error(), "already registered") {
			return fmt.Errorf("register MyXL maintenance job: %w", err)
		}
	}
	return nil
}

// computeNextAlignedDue computes the earliest future due timestamp strictly after
// now aligned to anchorTime (HH:MM) in timezone with step interval.
func computeNextAlignedDue(now time.Time, anchorTime, timezone string, interval time.Duration) (time.Time, error) {
	if interval <= 0 {
		return time.Time{}, errors.New("maintenance interval must be positive")
	}
	hour, minute, err := ParseAnchorTime(anchorTime)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}

	nowLoc := now.In(loc)
	anchorToday := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hour, minute, 0, 0, loc)
	if !anchorToday.After(now) {
		nextDue, err := jobs.NextIntervalDueAfter(anchorToday, now, interval)
		if err != nil {
			return time.Time{}, err
		}
		return nextDue.UTC(), nil
	}

	candidate := anchorToday
	for candidate.Add(-interval).After(now) {
		candidate = candidate.Add(-interval)
	}
	return candidate.UTC(), nil
}

func buildMaintenanceJobSchedule(cfg TokenMaintenanceConfig, now time.Time) (jobs.JobSchedule, error) {
	if err := ValidateTokenMaintenanceConfig(cfg); err != nil {
		return jobs.JobSchedule{}, err
	}
	nextDue, err := computeNextAlignedDue(now, cfg.AnchorTime, cfg.Timezone, cfg.Interval)
	if err != nil {
		return jobs.JobSchedule{}, err
	}
	return jobs.JobSchedule{
		ID:            maintenanceJobID,
		JobID:         maintenanceJobID,
		Recurrence:    "interval",
		Interval:      cfg.Interval,
		Timezone:      cfg.Timezone,
		NextDueAt:     nextDue,
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       cfg.Enabled,
	}, nil
}

// ApplyMaintenanceConfig validates and persists cfg, then updates or disables the
// durable JobSchedule. If updating the schedule fails, it rolls back cfg best-effort
// so configuration and schedule state do not silently diverge.
func (p *Plugin) ApplyMaintenanceConfig(ctx context.Context, cfg TokenMaintenanceConfig) error {
	if p == nil || p.repo == nil {
		return errors.New("myxl repository is not configured")
	}
	if err := ValidateTokenMaintenanceConfig(cfg); err != nil {
		return err
	}

	p.maintMu.RLock()
	schedClient := p.scheduleClient
	p.maintMu.RUnlock()
	if schedClient == nil {
		return errors.New("myxl schedule client is not initialized")
	}

	var sched jobs.JobSchedule
	if cfg.Enabled {
		var err error
		sched, err = buildMaintenanceJobSchedule(cfg, time.Now().UTC())
		if err != nil {
			return err
		}
	}

	prevCfg, err := p.repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		return err
	}
	if err := p.repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
		return err
	}

	var schedErr error
	if cfg.Enabled {
		schedErr = schedClient.SaveSchedule(ctx, sched)
	} else {
		schedErr = schedClient.DisableSchedule(ctx, maintenanceJobID)
	}
	if schedErr != nil {
		if prevCfg != nil {
			if rollbackErr := p.repo.SaveTokenMaintenanceConfig(ctx, prevCfg); rollbackErr != nil {
				return fmt.Errorf("%w: %v (rollback: %v)", ErrScheduleSyncRollbackFailed, schedErr, rollbackErr)
			}
		}
		return fmt.Errorf("sync maintenance schedule: %w", schedErr)
	}
	return nil
}

// TriggerMaintenanceNow requests an immediate maintenance execution through
// JobClient.Trigger without bypassing JobManager or TaskEngine.
func (p *Plugin) TriggerMaintenanceNow(ctx context.Context) error {
	if p == nil {
		return errors.New("myxl plugin is nil")
	}
	if p.maintRunning.Load() {
		return ErrMaintenanceAlreadyRunning
	}
	p.maintMu.RLock()
	jobClient := p.jobsClient
	p.maintMu.RUnlock()
	if jobClient == nil {
		return errors.New("myxl jobs client is not initialized")
	}
	return jobClient.Trigger(ctx, maintenanceJobID)
}

func (p *Plugin) handleTokenMaintenanceJob(ctx context.Context, _ jobs.JobDefinition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.repo == nil || p.client == nil {
		return errors.New("myxl maintenance dependencies are not configured")
	}
	if !p.maintRunning.CompareAndSwap(false, true) {
		return nil
	}
	defer p.maintRunning.Store(false)

	cfg, err := p.repo.GetTokenMaintenanceConfig(ctx)
	if err != nil {
		return fmt.Errorf("load maintenance config: %w", err)
	}
	if err := ValidateTokenMaintenanceConfig(*cfg); err != nil {
		return fmt.Errorf("invalid maintenance config: %w", err)
	}

	p.logMaintenanceStarted("scheduled")
	summary, outcomes, runErr := runTokenMaintenanceCycle(ctx, p.repo, p.client, *cfg, "scheduled")
	if summary != nil {
		p.recordMaintenanceDiagnostics(summary)
	}
	p.logMaintenanceFinished(summary, outcomes, runErr)

	if summary != nil && summary.Status == RunStatusCancelled {
		// Use a fresh bounded context inside maybeNotifyDestinations because ctx is already cancelled.
		p.maybeNotifyDestinations(context.Background(), *cfg, summary, outcomes)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return context.Canceled
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if runErr != nil {
		return runErr
	}
	p.maybeNotifyDestinations(ctx, *cfg, summary, outcomes)
	return nil
}
