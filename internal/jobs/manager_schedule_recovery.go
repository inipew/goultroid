package jobs

import (
	"context"
	"errors"
	"strings"
)

const maxPreparedScheduleRecoveryBatch = 256

type preparedScheduleLister interface {
	ListPreparedSchedules(context.Context, string, int) ([]JobSchedule, error)
}

type preparedScheduleDeleter interface {
	DeletePreparedSchedule(context.Context, string) error
}

// ListPreparedSchedules returns schedules that were durably prepared but never
// published. Stores that do not expose the optional recovery capability return
// no candidates so focused non-SQLite tests remain source-compatible.
func (m *Manager) ListPreparedSchedules(ctx context.Context, prefix string, limit int) ([]JobSchedule, error) {
	if m == nil || m.stores.Schedules == nil {
		return nil, nil
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.New("prepared schedule prefix is required")
	}
	if limit <= 0 || limit > maxPreparedScheduleRecoveryBatch {
		limit = maxPreparedScheduleRecoveryBatch
	}
	store, ok := m.stores.Schedules.(preparedScheduleLister)
	if !ok {
		return nil, nil
	}
	return store.ListPreparedSchedules(ctx, prefix, limit)
}

// DeletePreparedSchedule deletes only a disabled never-published schedule.
// This is registration compensation, not a general schedule-delete API.
func (m *Manager) DeletePreparedSchedule(ctx context.Context, scheduleID string) error {
	if m == nil || m.stores.Schedules == nil {
		return errors.New("job schedule store is not configured")
	}
	store, ok := m.stores.Schedules.(preparedScheduleDeleter)
	if !ok {
		return errors.New("job schedule store does not support prepared-schedule cleanup")
	}
	if err := store.DeletePreparedSchedule(ctx, scheduleID); err != nil {
		return err
	}
	m.signalSchedule()
	return nil
}
