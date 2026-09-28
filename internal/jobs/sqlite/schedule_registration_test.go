package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestRuntimeExecutionE1_PreparedScheduleRecoverySelectsOnlyNeverPublishedRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	for _, id := range []string{"wrapper-prepared", "wrapper-published", "wrapper-compensated"} {
		if err := store.SaveDefinition(ctx, &jobs.JobDefinition{
			ID:          id,
			ScopeOwner:  id,
			QuotaOwner:  "scheduler",
			HandlerType: "scheduler.action",
			Enabled:     true,
		}); err != nil {
			t.Fatalf("save definition %s: %v", id, err)
		}
	}
	nextDue := time.Now().UTC().Add(time.Hour)
	prepared := &jobs.JobSchedule{
		ID:            "sched:scheduled:1",
		JobID:         "wrapper-prepared",
		Recurrence:    "once",
		Timezone:      "UTC",
		NextDueAt:     nextDue,
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       false,
		Revision:      1,
	}
	if err := store.SaveSchedule(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	published := *prepared
	published.ID = "sched:scheduled:2"
	published.JobID = "wrapper-published"
	if err := store.SaveSchedule(ctx, &published); err != nil {
		t.Fatal(err)
	}
	published.Enabled = true
	if err := store.SaveSchedule(ctx, &published); err != nil {
		t.Fatal(err)
	}
	compensated := *prepared
	compensated.ID = "sched:scheduled:3"
	compensated.JobID = "wrapper-compensated"
	if err := store.SaveSchedule(ctx, &compensated); err != nil {
		t.Fatal(err)
	}
	compensated.Enabled = true
	if err := store.SaveSchedule(ctx, &compensated); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableSchedule(ctx, compensated.ID); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListPreparedSchedules(ctx, "sched:scheduled:", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != prepared.ID || candidates[0].Revision != 1 || candidates[0].Enabled {
		t.Fatalf("prepared candidates = %+v, want only revision-1 disabled row", candidates)
	}
	if err := store.DeletePreparedSchedule(ctx, prepared.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.GetSchedule(ctx, prepared.ID)
	if err == nil || remaining != nil {
		t.Fatalf("prepared schedule still exists: schedule=%+v err=%v", remaining, err)
	}
	if err := store.DeletePreparedSchedule(ctx, compensated.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err = store.GetSchedule(ctx, compensated.ID)
	if err != nil || remaining == nil || remaining.Revision <= 1 || remaining.Enabled {
		t.Fatalf("higher-revision disabled schedule was deleted or changed: schedule=%+v err=%v", remaining, err)
	}
}
