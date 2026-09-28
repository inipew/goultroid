package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestPluginContextJobsAndSchedulesAreCapabilitySeparated(t *testing.T) {
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{}, nil)
	gate := NewCapabilityGate()

	if err := gate.RegisterManifest(Manifest{
		ID: "jobs_only", Name: "Jobs Only", Version: "1.0.0",
		Capabilities: []string{CapJobs},
	}); err != nil {
		t.Fatal(err)
	}
	jobsCtx := NewPluginContext(context.Background(), ContextConfig{
		Owner: "jobs_only", Gate: gate, Jobs: manager,
	})
	jobClient, err := jobsCtx.Jobs()
	if err != nil {
		t.Fatalf("Jobs() error = %v", err)
	}
	if _, leaked := any(jobClient).(*jobs.Manager); leaked {
		t.Fatal("Jobs() leaked concrete *jobs.Manager")
	}
	if _, err := jobsCtx.Schedules(); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("Schedules() error = %v, want ErrCapabilityDenied", err)
	}

	if err := gate.RegisterManifest(Manifest{
		ID: "scheduler_only", Name: "Scheduler Only", Version: "1.0.0",
		Capabilities: []string{CapScheduler},
	}); err != nil {
		t.Fatal(err)
	}
	scheduleCtx := NewPluginContext(context.Background(), ContextConfig{
		Owner: "scheduler_only", Gate: gate, Jobs: manager,
	})
	scheduleClient, err := scheduleCtx.Schedules()
	if err != nil {
		t.Fatalf("Schedules() error = %v", err)
	}
	if _, leaked := any(scheduleClient).(*jobs.Manager); leaked {
		t.Fatal("Schedules() leaked concrete *jobs.Manager")
	}
	if _, err := scheduleCtx.Jobs(); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("Jobs() error = %v, want ErrCapabilityDenied", err)
	}
}
