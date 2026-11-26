package taskengine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/logging"
	"github.com/inipew/goultroid/internal/tasks"
)

type l4cEntry struct {
	level  string
	fields map[string]string
}

type l4cLogger struct {
	mu      sync.Mutex
	entries []l4cEntry
}

func (l *l4cLogger) record(level string, fields []logging.Field) {
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		rf := logging.RedactField(f)
		if rf.Kind == logging.KindString {
			m[rf.Key] = rf.Str
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, l4cEntry{level: level, fields: m})
	l.mu.Unlock()
}

func (l *l4cLogger) Debug(_ string, fields ...logging.Field) { l.record("debug", fields) }
func (l *l4cLogger) Info(_ string, fields ...logging.Field)  { l.record("info", fields) }
func (l *l4cLogger) Warn(_ string, fields ...logging.Field)  { l.record("warn", fields) }
func (l *l4cLogger) Error(_ string, fields ...logging.Field) { l.record("error", fields) }
func (l *l4cLogger) With(...logging.Field) logging.Logger    { return l }
func (l *l4cLogger) Named(string) logging.Logger             { return l }

func TestTaskEngine_LogsAbnormalitiesAndSkipsNormalSuccess(t *testing.T) {
	rec := &l4cLogger{}
	cfg := NewDefaultConfig()
	cfg.Logger = rec
	eng := NewEngine(cfg)

	// 1. Admission before Start must log task_admission_rejected with correlation_id.
	ctx := logging.WithCorrelationID(context.Background(), "cid-l4c-1")
	_, err := eng.Submit(ctx, tasks.WorkSpec{
		ID:         "t-reject",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Class:      tasks.PriorityInteractive,
		Handler:    func(context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("expected admission error before Start")
	}

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	// 2. Normal completion must NOT emit operational logs.
	tkOk, err := eng.Submit(ctx, tasks.WorkSpec{
		ID:         "t-ok",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Class:      tasks.PriorityInteractive,
		Handler:    func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Submit ok: %v", err)
	}
	if _, err := tkOk.Wait(context.Background()); err != nil {
		t.Fatalf("Wait ok: %v", err)
	}

	// 3. Panic and internal error must emit structured logs.
	tkPanic, err := eng.Submit(ctx, tasks.WorkSpec{
		ID:         "t-panic",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Class:      tasks.PriorityInteractive,
		Handler:    func(context.Context) error { panic("secret-task-panic") },
	})
	if err != nil {
		t.Fatalf("Submit panic: %v", err)
	}
	_, _ = tkPanic.Wait(context.Background())

	tkFail, err := eng.Submit(ctx, tasks.WorkSpec{
		ID:         "t-fail",
		QuotaOwner: "test-owner",
		Pool:       "general",
		Class:      tasks.PriorityInteractive,
		Handler:    func(context.Context) error { return errors.New("boom") },
	})
	if err != nil {
		t.Fatalf("Submit fail: %v", err)
	}
	_, _ = tkFail.Wait(context.Background())

	rec.mu.Lock()
	defer rec.mu.Unlock()
	events := map[string]bool{}
	for _, e := range rec.entries {
		events[e.fields["event"]] = true
		if e.fields["event"] == "task_admission_rejected" && e.fields["correlation_id"] != "cid-l4c-1" {
			t.Errorf("expected correlation_id cid-l4c-1, got %q", e.fields["correlation_id"])
		}
	}
	for _, want := range []string{"task_admission_rejected", "task_panic_recovered", "task_execution_failed"} {
		if !events[want] {
			t.Errorf("expected event %q, got %+v", want, events)
		}
	}
}
