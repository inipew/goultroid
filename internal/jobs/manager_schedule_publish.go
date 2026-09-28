package jobs

import "context"

type scheduleRegistrationPublisher interface {
	PublishScheduleRegistration(context.Context, string, int64) error
}

// PublishScheduleRegistration uses the production store's shared-database
// transaction when available. The bool reports whether the store owns this
// capability; alternate/split stores can keep the conservative staged path.
func (m *Manager) PublishScheduleRegistration(ctx context.Context, scheduleID string, scheduledJobID int64) (bool, error) {
	if m == nil || m.stores.Schedules == nil {
		return false, nil
	}
	publisher, ok := m.stores.Schedules.(scheduleRegistrationPublisher)
	if !ok {
		return false, nil
	}
	if err := publisher.PublishScheduleRegistration(ctx, scheduleID, scheduledJobID); err != nil {
		return true, err
	}
	m.signalSchedule()
	return true, nil
}
