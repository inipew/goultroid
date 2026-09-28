package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestRuntimeExecutionE1_DeleteDefinitionCascadesOwnedSchedule(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	store := NewStore(db)
	ctx := context.Background()

	if err := store.SaveDefinition(ctx, &jobs.JobDefinition{
		ID:          "scheduler:job:1",
		ScopeOwner:  "scheduler:1",
		QuotaOwner:  "scheduler",
		HandlerType: "scheduler.action",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("save definition: %v", err)
	}
	if err := store.SaveSchedule(ctx, &jobs.JobSchedule{
		ID:            "sched:scheduled:1",
		JobID:         "scheduler:job:1",
		Recurrence:    "once",
		Timezone:      "UTC",
		NextDueAt:     time.Now().UTC().Add(time.Hour),
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       false,
		Revision:      1,
	}); err != nil {
		t.Fatalf("save disabled schedule: %v", err)
	}

	if err := store.DeleteDefinition(ctx, "scheduler:job:1"); err != nil {
		t.Fatalf("delete wrapper definition: %v", err)
	}
	var definitionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_definitions WHERE id = ?`, "scheduler:job:1").Scan(&definitionCount); err != nil {
		t.Fatal(err)
	}
	if definitionCount != 0 {
		t.Fatalf("wrapper definition count = %d, want 0", definitionCount)
	}
	var scheduleCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_schedules WHERE id = ?`, "sched:scheduled:1").Scan(&scheduleCount); err != nil {
		t.Fatal(err)
	}
	if scheduleCount != 0 {
		t.Fatalf("owned schedule count after wrapper deletion = %d, want 0", scheduleCount)
	}
}
