package jobs_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestManagerUsesExplicitJobResourcesNotPoolInference(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	pump := jobs.NewPersistencePump(1, 4)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer pump.Stop(context.Background())
	client := &capturedClient{}
	manager := jobs.NewManager(client, jobsqlite.NewResourceStore(db.DB), pump)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(context.Background())
	if err := manager.RegisterHandler("resource-test", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}

	explicit := []tasks.ResourceRequirement{{Name: "process", Amount: 2}, {Name: "media", Amount: 1}}
	if err := manager.Register(jobs.JobDefinition{
		ID: "job-explicit", ScopeOwner: "system", QuotaOwner: "system", HandlerType: "resource-test",
		Pool: "general", Resources: explicit,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-explicit", "explicit:1"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	got := append([]tasks.ResourceRequirement(nil), client.spec.Resources...)
	client.mu.Unlock()
	if !reflect.DeepEqual(got, explicit) {
		t.Fatalf("submitted resources = %#v, want %#v", got, explicit)
	}

	// A pool name no longer implies resource reservations. Definitions must
	// persist and declare the resources they actually consume.
	if err := manager.Register(jobs.JobDefinition{
		ID: "job-no-inference", ScopeOwner: "system", QuotaOwner: "system", HandlerType: "resource-test",
		Pool: "media-process",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.SubmitOccurrence(context.Background(), "job-no-inference", "explicit:2"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	got = append([]tasks.ResourceRequirement(nil), client.spec.Resources...)
	client.mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("pool inference still active: %#v", got)
	}
}
