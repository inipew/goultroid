package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

type scopedDefinitionStore struct {
	saved jobs.JobDefinition
}

func (s *scopedDefinitionStore) SaveDefinition(_ context.Context, def *jobs.JobDefinition) error {
	s.saved = *def
	return nil
}

func (s *scopedDefinitionStore) UpdateDefinitionCAS(context.Context, *jobs.JobDefinition, uint64) error {
	return nil
}

type scopedScheduleStore struct {
	saved    jobs.JobSchedule
	disabled string
}

func (s *scopedScheduleStore) SaveSchedule(_ context.Context, schedule *jobs.JobSchedule) error {
	s.saved = *schedule
	return nil
}

func (s *scopedScheduleStore) DisableSchedule(_ context.Context, id string) error {
	s.disabled = id
	return nil
}

func (s *scopedScheduleStore) ListDueSchedules(context.Context, time.Time, int) ([]jobs.JobSchedule, error) {
	return nil, nil
}

func (s *scopedScheduleStore) EarliestScheduleDue(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *scopedScheduleStore) MaterializeDueSchedule(context.Context, string, time.Time) (*jobs.JobOccurrence, error) {
	return nil, nil
}

func (s *scopedScheduleStore) SkipDueSchedule(context.Context, string, time.Time) error {
	return nil
}

func (s *scopedScheduleStore) CutoverActive(context.Context) (bool, error) {
	return false, nil
}

func TestPluginContextJobsAndSchedulesEnforcePluginOwnership(t *testing.T) {
	definitions := &scopedDefinitionStore{}
	schedules := &scopedScheduleStore{}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Definitions: definitions,
		Schedules:   schedules,
	}, nil)

	gate := NewCapabilityGate()
	if err := gate.RegisterManifest(Manifest{
		ID: "alpha", Name: "Alpha", Version: "1.0.0",
		Capabilities: []string{CapJobs, CapScheduler},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := NewPluginContext(context.Background(), ContextConfig{
		Owner: "alpha", Gate: gate, Jobs: manager,
	})

	jobClient, err := ctx.Jobs()
	if err != nil {
		t.Fatal(err)
	}
	if err := jobClient.RegisterHandler("worker", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := jobClient.Register(jobs.JobDefinition{
		ID: "daily", HandlerType: "worker",
		ScopeOwner: "foreign", QuotaOwner: "foreign",
	}); err != nil {
		t.Fatal(err)
	}
	if definitions.saved.ID != "plugin:alpha:daily" {
		t.Fatalf("stored job id = %q", definitions.saved.ID)
	}
	if definitions.saved.ScopeOwner != "plugin:alpha" || definitions.saved.QuotaOwner != "plugin:alpha" {
		t.Fatalf("stored ownership = scope:%q quota:%q", definitions.saved.ScopeOwner, definitions.saved.QuotaOwner)
	}
	if definitions.saved.HandlerType != "plugin:alpha:worker" {
		t.Fatalf("stored handler type = %q", definitions.saved.HandlerType)
	}
	if _, ok := manager.Definition("plugin:alpha:daily"); !ok {
		t.Fatal("scoped definition was not registered")
	}

	scheduleClient, err := ctx.Schedules()
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduleClient.SaveSchedule(context.Background(), jobs.JobSchedule{
		ID: "morning", JobID: "daily", Recurrence: "once",
		Timezone: "UTC", NextDueAt: time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if schedules.saved.ID != "plugin:alpha:morning" || schedules.saved.JobID != "plugin:alpha:daily" {
		t.Fatalf("stored schedule = %+v", schedules.saved)
	}

	if err := scheduleClient.DisableSchedule(context.Background(), "plugin:beta:foreign"); err != nil {
		t.Fatal(err)
	}
	if schedules.disabled != "plugin:alpha:plugin:beta:foreign" {
		t.Fatalf("foreign schedule id escaped scope: %q", schedules.disabled)
	}
}
