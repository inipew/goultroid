package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SetScheduleWake connects durable schedule mutations and timing-owned
// occurrence settlement to the timing owner. The callback must be non-blocking;
// Scheduler uses coalescing wake channels.
func (m *Manager) SetScheduleWake(wake func()) {
	m.mu.Lock()
	m.scheduleWake = wake
	m.mu.Unlock()
}

func (m *Manager) signalSchedule() {
	m.mu.RLock()
	wake := m.scheduleWake
	m.mu.RUnlock()
	if wake != nil {
		wake()
	}
}

func (m *Manager) SaveSchedule(ctx context.Context, schedule JobSchedule) error {
	if err := validateSchedulePolicy(schedule); err != nil {
		return err
	}
	store := m.stores.Schedules
	if store == nil {
		return errors.New("job schedule store is not configured")
	}
	if err := store.SaveSchedule(ctx, &schedule); err != nil {
		return err
	}
	m.signalSchedule()
	return nil
}

func validateSchedulePolicy(schedule JobSchedule) error {
	if strings.TrimSpace(schedule.ID) == "" || strings.TrimSpace(schedule.JobID) == "" {
		return errors.New("schedule id and job id are required")
	}
	switch schedule.Recurrence {
	case "once":
		if schedule.Interval != 0 {
			return errors.New("one-shot schedule interval must be zero")
		}
	case "interval":
		if schedule.Interval < time.Second {
			return errors.New("recurring schedule interval must be at least one second")
		}
	default:
		return fmt.Errorf("unsupported recurrence %q", schedule.Recurrence)
	}
	if schedule.NextDueAt.IsZero() {
		return errors.New("schedule next due time is required")
	}
	tz := schedule.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid schedule timezone %q: %w", tz, err)
	}
	switch schedule.MisfirePolicy {
	case "", MisfireRunOnce, MisfireSkip:
	case MisfireCatchUpBounded:
		return errors.New("catch_up_bounded misfire policy is not supported")
	default:
		return fmt.Errorf("invalid misfire policy %q", schedule.MisfirePolicy)
	}
	switch schedule.OverlapPolicy {
	case "", OverlapForbid:
	case OverlapReplace:
		return errors.New("replace overlap policy is not supported")
	case OverlapAllowBounded:
		return errors.New("allow_bounded overlap policy is not supported")
	default:
		return fmt.Errorf("invalid overlap policy %q", schedule.OverlapPolicy)
	}
	return nil
}

// DisableSchedule atomically removes a schedule from timing ownership while
// retaining its durable definition and occurrence history for diagnostics.
func (m *Manager) DisableSchedule(ctx context.Context, scheduleID string) error {
	store := m.stores.Schedules
	if store == nil {
		return errors.New("job schedule store is not configured")
	}
	if err := store.DisableSchedule(ctx, scheduleID); err != nil {
		return err
	}
	m.signalSchedule()
	return nil
}

func (m *Manager) ScheduleCutoverActive(ctx context.Context) (bool, error) {
	store := m.stores.Schedules
	if store == nil {
		return false, nil
	}
	return store.CutoverActive(ctx)
}

func (m *Manager) EarliestScheduleDue(ctx context.Context) (time.Time, bool, error) {
	store := m.stores.Schedules
	if store == nil {
		return time.Time{}, false, nil
	}
	return store.EarliestScheduleDue(ctx)
}

// NextIntervalDueAfter advances one interval schedule in constant time while
// preserving its phase. The returned time is always strictly after now. Very
// large spans that saturate time.Duration are rejected instead of falling back
// to a work-proportional catch-up loop.
func NextIntervalDueAfter(nextDue, now time.Time, interval time.Duration) (time.Time, error) {
	if interval <= 0 {
		return time.Time{}, errors.New("schedule interval must be positive")
	}
	if nextDue.After(now) {
		return nextDue, nil
	}
	const maxDuration = time.Duration(1<<63 - 1)
	delta := now.Sub(nextDue)
	if now.After(nextDue) && delta == maxDuration {
		return time.Time{}, errors.New("schedule catch-up span exceeds time.Duration range")
	}
	steps := int64(delta/interval) + 1
	if steps <= 0 || steps > int64(maxDuration)/int64(interval) {
		return time.Time{}, errors.New("schedule catch-up arithmetic overflow")
	}
	candidate := nextDue.Add(time.Duration(steps * int64(interval)))
	if !candidate.After(now) {
		return time.Time{}, errors.New("schedule catch-up did not advance strictly after now")
	}
	return candidate, nil
}

// ProcessDueSchedules materializes a bounded batch and delegates every
// physical attempt to TaskEngine. Scheduler calls this timing-only API.
func (m *Manager) ProcessDueSchedules(ctx context.Context, now time.Time, limit int) (int, error) {
	store := m.stores.Schedules
	if store == nil {
		return 0, errors.New("job schedule store is not configured")
	}
	schedules, err := store.ListDueSchedules(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, schedule := range schedules {
		if err := validateSchedulePolicy(schedule); err != nil {
			// Quarantine persisted policies this binary cannot honor. Leaving the
			// row due would turn a configuration error into a tight scheduler loop.
			if disableErr := store.DisableSchedule(ctx, schedule.ID); disableErr != nil {
				return processed, fmt.Errorf("schedule %s policy invalid (%v), disable: %w", schedule.ID, err, disableErr)
			}
			return processed, fmt.Errorf("schedule %s: %w", schedule.ID, err)
		}
		nextDue := schedule.NextDueAt
		if schedule.Recurrence != "once" {
			interval := schedule.Interval
			if interval <= 0 {
				interval = time.Minute
			}
			nextDue, err = NextIntervalDueAfter(schedule.NextDueAt, now, interval)
			if err != nil {
				if disableErr := store.DisableSchedule(ctx, schedule.ID); disableErr != nil {
					return processed, fmt.Errorf("schedule %s catch-up invalid (%v), disable: %w", schedule.ID, err, disableErr)
				}
				return processed, fmt.Errorf("schedule %s catch-up: %w", schedule.ID, err)
			}
			// A recurring slot is a misfire only after at least one complete
			// interval has elapsed. Skip advances timing ownership atomically
			// without consuming occurrence or attempt capacity.
			if schedule.MisfirePolicy == MisfireSkip && !now.Before(schedule.NextDueAt.Add(interval)) {
				if err := store.SkipDueSchedule(ctx, schedule.ID, nextDue); err != nil {
					return processed, err
				}
				processed++
				continue
			}
		}
		occurrence, err := store.MaterializeDueSchedule(ctx, schedule.ID, nextDue)
		if err != nil {
			return processed, err
		}
		processed++
		if occurrence == nil {
			continue
		}
		m.mu.RLock()
		definition, found := m.definitions[occurrence.JobID]
		handler := m.handlers[definition.HandlerType]
		m.mu.RUnlock()
		if !found || handler == nil {
			m.signalRecovery()
			continue
		}
		if err := m.driveAttempt(ctx, occurrence.ID, definition, handler, timingOwnedOccurrence(occurrence)); err != nil {
			m.signalRecovery()
		}
	}
	return processed, nil
}
