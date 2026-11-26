package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/logging"
)

type l4dEntry struct {
	level  string
	fields map[string]string
}

type l4dLogger struct {
	mu      sync.Mutex
	entries []l4dEntry
}

func (l *l4dLogger) record(level string, fields []logging.Field) {
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		rf := logging.RedactField(f)
		if rf.Kind == logging.KindString {
			m[rf.Key] = rf.Str
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, l4dEntry{level: level, fields: m})
	l.mu.Unlock()
}

func (l *l4dLogger) Debug(_ string, fields ...logging.Field) { l.record("debug", fields) }
func (l *l4dLogger) Info(_ string, fields ...logging.Field)  { l.record("info", fields) }
func (l *l4dLogger) Warn(_ string, fields ...logging.Field)  { l.record("warn", fields) }
func (l *l4dLogger) Error(_ string, fields ...logging.Field) { l.record("error", fields) }
func (l *l4dLogger) With(...logging.Field) logging.Logger    { return l }
func (l *l4dLogger) Named(string) logging.Logger             { return l }

type noopSleeper struct{}

func (noopSleeper) Sleep(context.Context, time.Duration) error { return nil }

func TestRPCExecutor_LogsFloodWaitAndPermanentFailureWithCorrelation(t *testing.T) {
	rec := &l4dLogger{}
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Logger:  rec,
		Sleeper: noopSleeper{},
		DefaultPolicy: RetryPolicy{
			MaxAttempts:        2,
			BaseDelay:          time.Millisecond,
			MaxDelay:           time.Millisecond,
			InlineFloodWaitMax: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewRPCExecutor: %v", err)
	}

	ctx := logging.WithCorrelationID(context.Background(), "cid-rpc-1")

	// 1. Successful call must produce 0 logs.
	if err := exec.Do(ctx, RPCMeta{Method: "messages.sendMessage", Kind: RPCNonIdempotentMutation}, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec.mu.Lock()
	if len(rec.entries) != 0 {
		t.Fatalf("expected 0 logs on success, got %d", len(rec.entries))
	}
	rec.mu.Unlock()

	// 2. FloodWait followed by success logs rpc_flood_wait with correlation_id.
	calls := 0
	if err := exec.Do(ctx, RPCMeta{Method: "messages.getHistory", Kind: RPCReadOnly}, func(context.Context) error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return nil
	}); err != nil {
		t.Fatalf("unexpected error after flood wait retry: %v", err)
	}

	// 3. Permanent error with secret in message logs rpc_failed redacted.
	_ = exec.Do(ctx, RPCMeta{Method: "auth.signIn", Kind: RPCNonIdempotentMutation}, func(context.Context) error {
		return errors.New("RPC_ERROR: token=super-secret-rpc-tok")
	})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var sawFloodWait, sawFailed bool
	for _, e := range rec.entries {
		if e.fields["correlation_id"] != "cid-rpc-1" {
			t.Errorf("expected correlation_id cid-rpc-1, got %q", e.fields["correlation_id"])
		}
		if e.fields["event"] == "rpc_flood_wait" {
			sawFloodWait = true
		}
		if e.fields["event"] == "rpc_failed" {
			sawFailed = true
			if strings.Contains(e.fields["error"], "super-secret-rpc-tok") {
				t.Errorf("rpc_failed leaked secret: %q", e.fields["error"])
			}
		}
	}
	if !sawFloodWait || !sawFailed {
		t.Fatalf("expected rpc_flood_wait and rpc_failed, got %+v", rec.entries)
	}
}

func BenchmarkRPCExecutor_DoSuccess(b *testing.B) {
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Logger: logging.Nop(),
	})
	if err != nil {
		b.Fatalf("NewRPCExecutor: %v", err)
	}
	ctx := logging.WithCorrelationID(context.Background(), "cid-bench-1")
	meta := RPCMeta{Method: "messages.sendMessage", Kind: RPCReadOnly}
	b.ReportAllocs()
	for b.Loop() {
		_ = exec.Do(ctx, meta, func(context.Context) error { return nil })
	}
}
