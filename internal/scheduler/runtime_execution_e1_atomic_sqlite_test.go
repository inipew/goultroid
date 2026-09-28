package scheduler

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	_ "modernc.org/sqlite"
)

func newRuntimeExecutionE1SharedSQLite(t *testing.T) (*SQLiteRepository, *jobsqlite.ResourceStore, *jobs.Manager, *Engine, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := jobsqlite.NewResourceStore(db)
	manager := jobs.NewManagerWithPorts(nil, jobsqlite.ResourcePorts(store), nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	return repo, store, manager, engine, db
}

func TestRuntimeExecutionE1_SharedSQLiteAtomicallyPublishesRegistration(t *testing.T) {
	repo, store, _, engine, _ := newRuntimeExecutionE1SharedSQLite(t)
	job, err := engine.ScheduleOnce(context.Background(), 10, "chat", 0, time.Now().UTC().Add(time.Hour), ActionMessage, "hello")
	if err != nil {
		t.Fatalf("schedule once: %v", err)
	}
	row, err := repo.GetScheduledJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Status != JobStatusPending {
		t.Fatalf("compatibility row = %+v, want pending", row)
	}
	schedule, err := store.GetSchedule(context.Background(), redesignedScheduleID(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.Enabled || schedule.Revision != 2 {
		t.Fatalf("redesigned schedule = %+v, want enabled revision 2", schedule)
	}
}

func TestRuntimeExecutionE1_SharedSQLitePublishFailureRollsBackAndCompensates(t *testing.T) {
	repo, store, manager, engine, db := newRuntimeExecutionE1SharedSQLite(t)
	if _, err := db.Exec(`
		CREATE TRIGGER fail_scheduler_registration_publish
		BEFORE UPDATE OF enabled ON job_schedules
		WHEN NEW.enabled = 1
		BEGIN
			SELECT RAISE(ABORT, 'forced registration publish failure');
		END`); err != nil {
		t.Fatal(err)
	}

	_, err := engine.ScheduleOnce(context.Background(), 11, "chat", 0, time.Now().UTC().Add(time.Hour), ActionMessage, "hello")
	if err == nil {
		t.Fatal("schedule unexpectedly succeeded through injected publish failure")
	}
	rows, listErr := repo.ListScheduledJobs(context.Background(), 11)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("compatibility rows after failed publish = %+v, want none", rows)
	}
	if _, ok := manager.Definition(scheduledDefinitionID(1)); ok {
		t.Fatal("scheduler wrapper survived failed atomic publication")
	}
	if schedule, getErr := store.GetSchedule(context.Background(), redesignedScheduleID(1)); getErr == nil || schedule != nil {
		t.Fatalf("redesigned schedule survived compensation: schedule=%+v err=%v", schedule, getErr)
	}
}
