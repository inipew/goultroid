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
	if schedules.disabled != "plugin:alpha:plugin%3Abeta%3Aforeign" {
		t.Fatalf("foreign schedule id escaped scope: %q", schedules.disabled)
	}
}

type captureScopedJobBackend struct {
	triggered string
}

func (b *captureScopedJobBackend) RegisterHandler(string, jobs.Handler) error { return nil }
func (b *captureScopedJobBackend) Register(jobs.JobDefinition) error         { return nil }
func (b *captureScopedJobBackend) Trigger(_ context.Context, jobID string) error {
	b.triggered = jobID
	return nil
}

type captureScopedScheduleBackend struct {
	disabled string
}

func (b *captureScopedScheduleBackend) SaveSchedule(context.Context, jobs.JobSchedule) error {
	return nil
}

func (b *captureScopedScheduleBackend) DisableSchedule(_ context.Context, scheduleID string) error {
	b.disabled = scheduleID
	return nil
}

func TestPluginJobNamespaceSeparatesColonBoundaries(t *testing.T) {
	left := pluginScopedName("a", "b:x")
	right := pluginScopedName("a:b", "x")
	if left == right {
		t.Fatalf("ambiguous plugin job namespace: both encoded as %q", left)
	}
	if left != "plugin:a:b%3Ax" {
		t.Fatalf("owner a / local b:x = %q", left)
	}
	if right != "plugin:a%3Ab:x" {
		t.Fatalf("owner a:b / local x = %q", right)
	}

	// Percent itself is escaped first, so a literal escape-looking component
	// cannot collide with the encoding of a colon.
	if got, want := pluginScopedName("a", "b%3Ax"), "plugin:a:b%253Ax"; got != want {
		t.Fatalf("literal percent component = %q, want %q", got, want)
	}
}

func TestScopedJobTriggerCannotReachColonCollidingOwner(t *testing.T) {
	leftBackend := &captureScopedJobBackend{}
	rightBackend := &captureScopedJobBackend{}
	left := scopedJobClient{manager: leftBackend, owner: "a"}
	right := scopedJobClient{manager: rightBackend, owner: "a:b"}

	if err := left.Trigger(context.Background(), "b:x"); err != nil {
		t.Fatal(err)
	}
	if err := right.Trigger(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if leftBackend.triggered == rightBackend.triggered {
		t.Fatalf("Trigger collision: both routed to %q", leftBackend.triggered)
	}
	if leftBackend.triggered != "plugin:a:b%3Ax" || rightBackend.triggered != "plugin:a%3Ab:x" {
		t.Fatalf("unexpected trigger routing: left=%q right=%q", leftBackend.triggered, rightBackend.triggered)
	}
}

func TestScopedScheduleDisableCannotReachColonCollidingOwner(t *testing.T) {
	leftBackend := &captureScopedScheduleBackend{}
	rightBackend := &captureScopedScheduleBackend{}
	left := scopedScheduleClient{manager: leftBackend, owner: "a"}
	right := scopedScheduleClient{manager: rightBackend, owner: "a:b"}

	if err := left.DisableSchedule(context.Background(), "b:x"); err != nil {
		t.Fatal(err)
	}
	if err := right.DisableSchedule(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if leftBackend.disabled == rightBackend.disabled {
		t.Fatalf("DisableSchedule collision: both routed to %q", leftBackend.disabled)
	}
	if leftBackend.disabled != "plugin:a:b%3Ax" || rightBackend.disabled != "plugin:a%3Ab:x" {
		t.Fatalf("unexpected disable routing: left=%q right=%q", leftBackend.disabled, rightBackend.disabled)
	}
}

