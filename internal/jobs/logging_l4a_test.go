package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/logging"
)

type l4aCaptured struct {
	level  string
	msg    string
	fields map[string]string
}

type l4aLogger struct {
	mu      sync.Mutex
	entries []l4aCaptured
}

func (l *l4aLogger) record(level, msg string, fields []logging.Field) {
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		rf := logging.RedactField(f)
		if rf.Kind == logging.KindString {
			m[rf.Key] = rf.Str
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, l4aCaptured{level: level, msg: logging.RedactString(msg), fields: m})
	l.mu.Unlock()
}

func (l *l4aLogger) Debug(msg string, fields ...logging.Field) { l.record("debug", msg, fields) }
func (l *l4aLogger) Info(msg string, fields ...logging.Field)  { l.record("info", msg, fields) }
func (l *l4aLogger) Warn(msg string, fields ...logging.Field)  { l.record("warn", msg, fields) }
func (l *l4aLogger) Error(msg string, fields ...logging.Field) { l.record("error", msg, fields) }
func (l *l4aLogger) With(...logging.Field) logging.Logger      { return l }
func (l *l4aLogger) Named(string) logging.Logger               { return l }

func TestManager_SetLoggerNilDefaultsToNopAndRecoveryRedacts(t *testing.T) {
	rec := &l4aLogger{}
	recStore := &failingRecoveryStore{err: errors.New("sqlite: disk error token=secret123")}

	mgr := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	mgr.accepting = true

	// Passing nil must safely reset to logging.Nop() without panicking.
	mgr.SetLogger(nil)
	mgr.runRecoveryPass(context.Background())

	mgr.SetLogger(rec)
	mgr.runRecoveryPass(context.Background())

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) != 1 {
		t.Fatalf("expected 1 recovery log entry, got %d", len(rec.entries))
	}
	e := rec.entries[0]
	if e.fields["component"] != "jobs" || e.fields["event"] != "job_recovery_failed" {
		t.Fatalf("unexpected structured fields: %+v", e.fields)
	}
	if errStr := e.fields["error"]; errStr == "" || errStr == "sqlite: disk error token=secret123" {
		t.Fatalf("expected redacted error string, got %q", errStr)
	}
}
