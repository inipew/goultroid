package jobs

import (
	"context"
	"testing"
	"time"
)

type scheduleOnlyStore struct {
	saved    JobSchedule
	disabled string
}

func (s *scheduleOnlyStore) SaveSchedule(_ context.Context, schedule *JobSchedule) error {
	s.saved = *schedule
	return nil
}

func (s *scheduleOnlyStore) DisableSchedule(_ context.Context, id string) error {
	s.disabled = id
	return nil
}

func (s *scheduleOnlyStore) ListDueSchedules(context.Context, time.Time, int) ([]JobSchedule, error) {
	return nil, nil
}

func (s *scheduleOnlyStore) EarliestScheduleDue(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *scheduleOnlyStore) MaterializeDueSchedule(context.Context, string, time.Time) (*JobOccurrence, error) {
	return nil, nil
}

func (s *scheduleOnlyStore) SkipDueSchedule(context.Context, string, time.Time) error {
	return nil
}

func (s *scheduleOnlyStore) CutoverActive(context.Context) (bool, error) {
	return false, nil
}

func TestManagerScheduleUsesSchedulePortOnly(t *testing.T) {
	store := &scheduleOnlyStore{}
	manager := NewManagerWithPorts(nil, StorePorts{Schedules: store}, nil)
	due := time.Now().UTC().Add(time.Minute)
	schedule := JobSchedule{
		ID: "schedule-only", JobID: "job-only", Recurrence: "once",
		Timezone: "UTC", NextDueAt: due,
	}
	if err := manager.SaveSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	if store.saved.ID != schedule.ID || store.saved.JobID != schedule.JobID {
		t.Fatalf("saved schedule = %+v", store.saved)
	}
	if err := manager.DisableSchedule(context.Background(), schedule.ID); err != nil {
		t.Fatal(err)
	}
	if store.disabled != schedule.ID {
		t.Fatalf("disabled schedule = %q, want %q", store.disabled, schedule.ID)
	}
}
