package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/logging"
)

type capturedLog struct {
	level  string
	msg    string
	fields []logging.Field
}

type recordingLogger struct {
	mu      sync.Mutex
	entries []capturedLog
}

func (l *recordingLogger) record(level, msg string, fields []logging.Field) {
	redacted := make([]logging.Field, len(fields))
	for i, f := range fields {
		redacted[i] = logging.RedactField(f)
	}
	l.mu.Lock()
	l.entries = append(l.entries, capturedLog{level: level, msg: logging.RedactString(msg), fields: redacted})
	l.mu.Unlock()
}

func (l *recordingLogger) Debug(msg string, fields ...logging.Field) { l.record("debug", msg, fields) }
func (l *recordingLogger) Info(msg string, fields ...logging.Field)  { l.record("info", msg, fields) }
func (l *recordingLogger) Warn(msg string, fields ...logging.Field)  { l.record("warn", msg, fields) }
func (l *recordingLogger) Error(msg string, fields ...logging.Field) { l.record("error", msg, fields) }
func (l *recordingLogger) With(...logging.Field) logging.Logger      { return l }
func (l *recordingLogger) Named(string) logging.Logger               { return l }

type testComp struct {
	name string
}

func (c testComp) Name() string                { return c.name }
func (c testComp) Dependencies() []string      { return nil }
func (c testComp) Start(context.Context) error { return nil }
func (c testComp) Stop(context.Context) error  { return nil }
func (c testComp) Health(context.Context) ComponentHealth {
	return ComponentHealth{Status: HealthHealthy}
}

func TestRuntime_LogsLifecycleTransitions(t *testing.T) {
	rec := &recordingLogger{}
	rt := New(WithLogger(rec))
	if err := rt.Register(testComp{name: "comp-a"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.StopWithin(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) < 4 {
		t.Fatalf("expected lifecycle start/stop logs, got %d", len(rec.entries))
	}
}

func TestSupervisor_LogsRestartsAndRedactsPanic(t *testing.T) {
	rec := &recordingLogger{}
	sup := NewSupervisor(
		WithSupervisorLogger(rec),
		WithBaseBackoff(5*time.Millisecond),
	)
	attempts := 0
	if err := sup.Register(WorkerSpec{
		Name:        "panic-worker",
		Restart:     RestartTransient,
		MaxRestarts: 1,
		Run: func(ctx context.Context) error {
			attempts++
			if attempts == 1 {
				panic("secret=super-secret-panic-token")
			}
			return errors.New("fatal worker failure")
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snaps := sup.Snapshot()
		if len(snaps) == 1 && snaps[0].State == WorkerStateDegraded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = sup.Stop(context.Background())

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, entry := range rec.entries {
		if strings.Contains(entry.msg, "super-secret-panic-token") {
			t.Fatalf("log message leaked panic secret: %q", entry.msg)
		}
		for _, f := range entry.fields {
			if rp, ok := f.Any.(logging.RedactedPanic); ok {
				if strings.Contains(rp.Summary, "super-secret-panic-token") {
					t.Fatalf("panic summary leaked secret: %q", rp.Summary)
				}
			}
		}
	}
	if len(rec.entries) < 2 {
		t.Fatalf("expected panic/restart/exhaustion logs, got %d", len(rec.entries))
	}
}

func TestSupervisor_SinglePanicProducesSingleOperationalEvent(t *testing.T) {
	rec := &recordingLogger{}
	sup := NewSupervisor(WithSupervisorLogger(rec))
	_ = sup.Register(WorkerSpec{
		Name:    "single-panic",
		Restart: NeverRestart,
		Run: func(context.Context) error {
			panic("boom")
		},
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if snaps := sup.Snapshot(); len(snaps) == 1 && snaps[0].State == WorkerStateFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = sup.Stop(context.Background())

	rec.mu.Lock()
	defer rec.mu.Unlock()
	panicEvents := 0
	for _, entry := range rec.entries {
		for _, f := range entry.fields {
			if f.Key == "event" && f.Str == "worker_panic_recovered" {
				panicEvents++
			}
		}
	}
	if panicEvents != 1 {
		t.Fatalf("expected exactly 1 worker_panic_recovered event, got %d", panicEvents)
	}
}
