package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"github.com/inipew/goultroid/internal/logging"
	"github.com/inipew/goultroid/internal/taskengine"
)

type l4bEntry struct {
	level  string
	event  string
	comp   string
	fields map[string]string
}

type l4bLogger struct {
	mu      sync.Mutex
	entries []l4bEntry
}

func (l *l4bLogger) record(level string, fields []logging.Field) {
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		rf := logging.RedactField(f)
		if rf.Kind == logging.KindString {
			m[rf.Key] = rf.Str
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, l4bEntry{
		level:  level,
		event:  m["event"],
		comp:   m["component"],
		fields: m,
	})
	l.mu.Unlock()
}

func (l *l4bLogger) Debug(_ string, fields ...logging.Field) { l.record("debug", fields) }
func (l *l4bLogger) Info(_ string, fields ...logging.Field)  { l.record("info", fields) }
func (l *l4bLogger) Warn(_ string, fields ...logging.Field)  { l.record("warn", fields) }
func (l *l4bLogger) Error(_ string, fields ...logging.Field) { l.record("error", fields) }
func (l *l4bLogger) With(...logging.Field) logging.Logger    { return l }
func (l *l4bLogger) Named(string) logging.Logger             { return l }

func TestSchedulerEngine_LogsLifecycleEventsWithStandardFields(t *testing.T) {
	rec := &l4bLogger{}
	db, err := database.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(context.Background(), db.DB); err != nil {
		t.Fatalf("init job schema: %v", err)
	}
	te := taskengine.NewEngine(taskengine.NewDefaultConfig())
	pump := jobs.NewPersistencePump(2, 64)
	te.SetCommitPump(pump)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatalf("start pump: %v", err)
	}
	defer func() { _ = pump.Stop(context.Background()) }()
	if err := te.Start(context.Background()); err != nil {
		t.Fatalf("start taskengine: %v", err)
	}
	defer func() { _ = te.Stop(context.Background()) }()
	jobsMgr := jobs.NewManagerWithPorts(te, jobsqlite.ResourcePorts(jobsqlite.NewResourceStore(db.DB)), pump)
	if err := jobsMgr.Start(context.Background()); err != nil {
		t.Fatalf("start jobs manager: %v", err)
	}
	defer func() { _ = jobsMgr.Stop(context.Background()) }()

	repo := NewSQLiteRepository(db.DB)
	eng := NewEngine(repo, rec)
	eng.SetTasks(te)
	eng.SetJobsManager(jobsMgr)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var sawStarted, sawStopped bool
	for _, e := range rec.entries {
		if e.comp != "scheduler" {
			t.Errorf("expected component=scheduler, got %q", e.comp)
		}
		if e.event == "scheduler_started" {
			sawStarted = true
		}
		if e.event == "scheduler_stopped" {
			sawStopped = true
		}
	}
	if !sawStarted || !sawStopped {
		t.Fatalf("expected scheduler_started and scheduler_stopped, got %+v", rec.entries)
	}
}
