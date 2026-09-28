package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func prepareRuntimeExecutionPublishFixture(t *testing.T, store *ResourceStore, legacyID int64, scheduleID, definitionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO scheduled_jobs (id, status)
		VALUES (?, 'initializing')`, legacyID); err != nil {
		t.Fatalf("insert scheduled compatibility row: %v", err)
	}
	if err := store.SaveDefinition(ctx, &jobs.JobDefinition{
		ID:          definitionID,
		ScopeOwner:  definitionID,
		QuotaOwner:  "scheduler",
		HandlerType: "scheduler.action",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("save definition: %v", err)
	}
	if err := store.SaveSchedule(ctx, &jobs.JobSchedule{
		ID:            scheduleID,
		JobID:         definitionID,
		Recurrence:    "once",
		Timezone:      "UTC",
		NextDueAt:     time.Now().UTC().Add(time.Hour),
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       false,
		Revision:      1,
	}); err != nil {
		t.Fatalf("save prepared schedule: %v", err)
	}
}

func TestRuntimeExecutionE1_PublishScheduleRegistrationCommitsBothRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE scheduled_jobs (id INTEGER PRIMARY KEY, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	store := NewResourceStore(db)
	prepareRuntimeExecutionPublishFixture(t, store, 1, "sched:scheduled:1", "scheduler:job:1")

	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Schedules: store}, nil)
	supported, err := manager.PublishScheduleRegistration(context.Background(), "sched:scheduled:1", 1)
	if err != nil {
		t.Fatalf("publish schedule registration: %v", err)
	}
	if !supported {
		t.Fatal("production ResourceStore did not expose atomic schedule publication")
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM scheduled_jobs WHERE id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("compatibility status = %q, want pending", status)
	}
	schedule, err := store.GetSchedule(context.Background(), "sched:scheduled:1")
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.Enabled || schedule.Revision != 2 {
		t.Fatalf("published schedule = %+v, want enabled revision 2", schedule)
	}
}

func TestRuntimeExecutionE1_PublishScheduleRegistrationRollsBackBothRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE scheduled_jobs (id INTEGER PRIMARY KEY, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	store := NewResourceStore(db)
	prepareRuntimeExecutionPublishFixture(t, store, 2, "sched:scheduled:2", "scheduler:job:2")
	if _, err := db.Exec(`
		CREATE TRIGGER fail_runtime_execution_publish
		BEFORE UPDATE OF enabled ON job_schedules
		WHEN NEW.id = 'sched:scheduled:2' AND NEW.enabled = 1
		BEGIN
			SELECT RAISE(ABORT, 'forced publish failure');
		END`); err != nil {
		t.Fatal(err)
	}

	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Schedules: store}, nil)
	supported, err := manager.PublishScheduleRegistration(context.Background(), "sched:scheduled:2", 2)
	if !supported {
		t.Fatal("production ResourceStore did not expose atomic schedule publication")
	}
	if err == nil || !strings.Contains(err.Error(), "forced publish failure") {
		t.Fatalf("publish error = %v, want injected schedule update failure", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM scheduled_jobs WHERE id = 2`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "initializing" {
		t.Fatalf("compatibility status after rollback = %q, want initializing", status)
	}
	schedule, err := store.GetSchedule(context.Background(), "sched:scheduled:2")
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Enabled || schedule.Revision != 1 {
		t.Fatalf("schedule after rollback = %+v, want disabled revision 1", schedule)
	}
}
