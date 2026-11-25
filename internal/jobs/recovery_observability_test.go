package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type failingRecoveryStore struct {
	mu         sync.Mutex
	err        error
	candidates []RecoveryCandidate
}

func (s *failingRecoveryStore) ListRecoveryCandidates(ctx context.Context, limit int) ([]RecoveryCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.candidates, nil
}

func (s *failingRecoveryStore) ListUnresolvedOccurrences(ctx context.Context, limit int) ([]*JobOccurrence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, nil
}

func TestRecoveryFailureIsObservable(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	storeErr := errors.New("sqlite: disk I/O error during recovery candidate scan")
	recStore := &failingRecoveryStore{err: storeErr}

	manager := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	manager.accepting = true
	manager.SetLogger(logger)

	manager.runRecoveryPass(context.Background())

	diag := manager.RecoveryDiagnostics()
	if diag.PassCount != 1 {
		t.Fatalf("pass count = %d, want 1", diag.PassCount)
	}
	if diag.FailureCount != 1 {
		t.Fatalf("failure count = %d, want 1", diag.FailureCount)
	}
	if diag.LastFailureAt.IsZero() {
		t.Fatal("expected last failure timestamp to be set")
	}
	if !strings.Contains(diag.LastFailureMessage, "disk I/O error") {
		t.Fatalf("last failure message = %q, want disk I/O error", diag.LastFailureMessage)
	}
	if !diag.LastSuccessAt.IsZero() {
		t.Fatal("expected last success timestamp to remain zero on failure")
	}

	// Verify manager.Diagnostics() exposes the snapshot
	mgrDiag := manager.Diagnostics()
	if mgrDiag.Recovery.FailureCount != 1 {
		t.Fatalf("manager diagnostics failure count = %d, want 1", mgrDiag.Recovery.FailureCount)
	}

	// Verify structured warning log was emitted with operation context
	logEntries := logs.All()
	if len(logEntries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logEntries))
	}
	entry := logEntries[0]
	if entry.Message != "job recovery pass failed" {
		t.Fatalf("log message = %q, want 'job recovery pass failed'", entry.Message)
	}
	fields := entry.ContextMap()
	if fields["operation"] != "recover" {
		t.Fatalf("log field operation = %v, want 'recover'", fields["operation"])
	}
	if fields["scan_limit"] != int64(recoveryScanLimit) {
		t.Fatalf("log field scan_limit = %v, want %d", fields["scan_limit"], recoveryScanLimit)
	}
}

func TestRecoveryCancellationIsNotReportedAsFailure(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	recStore := &failingRecoveryStore{err: errors.New("some error")}

	manager := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	manager.accepting = true
	manager.SetLogger(logger)

	// Pre-cancelled context simulates shutdown
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	manager.runRecoveryPass(cancelledCtx)

	diag := manager.RecoveryDiagnostics()
	if diag.PassCount != 0 {
		t.Fatalf("pass count = %d, want 0 for cancelled pass", diag.PassCount)
	}
	if diag.FailureCount != 0 {
		t.Fatalf("failure count = %d, want 0 for cancelled pass", diag.FailureCount)
	}
	if !diag.LastFailureAt.IsZero() {
		t.Fatal("expected last failure timestamp to be zero for cancelled pass")
	}
	if len(logs.All()) != 0 {
		t.Fatalf("cancellation must not emit failure warning logs, got %d entries", len(logs.All()))
	}
}

func TestRecoveryInternalTimeoutIsReportedAsFailure(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	// Simulate store timing out during recovery pass while base context remains active
	recStore := &failingRecoveryStore{err: context.DeadlineExceeded}

	manager := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	manager.accepting = true
	manager.SetLogger(logger)

	// baseCtx is ACTIVE (e.g. context.Background())
	activeBaseCtx := context.Background()
	manager.runRecoveryPass(activeBaseCtx)

	diag := manager.RecoveryDiagnostics()
	if diag.PassCount != 1 {
		t.Fatalf("pass count = %d, want 1 for internal timeout", diag.PassCount)
	}
	if diag.FailureCount != 1 {
		t.Fatalf("failure count = %d, want 1 for internal timeout", diag.FailureCount)
	}
	if diag.LastFailureAt.IsZero() {
		t.Fatal("expected last failure timestamp to be set on internal timeout")
	}
	if !strings.Contains(diag.LastFailureMessage, "context deadline exceeded") {
		t.Fatalf("last failure message = %q, want context deadline exceeded", diag.LastFailureMessage)
	}

	logEntries := logs.All()
	if len(logEntries) != 1 {
		t.Fatalf("expected 1 warning log entry for internal timeout, got %d", len(logEntries))
	}
	if logEntries[0].Message != "job recovery pass failed" {
		t.Fatalf("log message = %q, want 'job recovery pass failed'", logEntries[0].Message)
	}
}

func TestSuccessfulEmptyRecoveryPassIsDistinctFromFailure(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	// Clean store with 0 candidates
	recStore := &failingRecoveryStore{candidates: nil}

	manager := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	manager.accepting = true
	manager.SetLogger(logger)

	manager.runRecoveryPass(context.Background())

	diag := manager.RecoveryDiagnostics()
	if diag.PassCount != 1 {
		t.Fatalf("pass count = %d, want 1", diag.PassCount)
	}
	if diag.FailureCount != 0 {
		t.Fatalf("failure count = %d, want 0 for clean empty pass", diag.FailureCount)
	}
	if diag.LastSuccessAt.IsZero() {
		t.Fatal("expected last success timestamp to be set on clean empty pass")
	}
	if !diag.LastFailureAt.IsZero() {
		t.Fatal("expected last failure timestamp to remain zero")
	}
	if diag.LastFailureMessage != "" {
		t.Fatalf("last failure message = %q, want empty", diag.LastFailureMessage)
	}
	if diag.LastReport.Scanned != 0 {
		t.Fatalf("report scanned = %d, want 0", diag.LastReport.Scanned)
	}
	if len(logs.All()) != 0 {
		t.Fatalf("successful empty pass must not emit warning logs, got %d", len(logs.All()))
	}
}

func TestRecoveryDiagnosticsRemainBounded(t *testing.T) {
	// Very long error string (>1000 characters)
	longErrorText := strings.Repeat("error_payload_fragment_", 50)
	recStore := &failingRecoveryStore{err: errors.New(longErrorText)}

	manager := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	manager.accepting = true

	// Run 10 consecutive failing passes
	for i := 0; i < 10; i++ {
		manager.runRecoveryPass(context.Background())
	}

	diag := manager.RecoveryDiagnostics()
	if diag.PassCount != 10 {
		t.Fatalf("pass count = %d, want 10", diag.PassCount)
	}
	if diag.FailureCount != 10 {
		t.Fatalf("failure count = %d, want 10", diag.FailureCount)
	}
	if len(diag.LastFailureMessage) > 256 {
		t.Fatalf("failure message length = %d, want <= 256", len(diag.LastFailureMessage))
	}
}

func TestRecoveryLoggingDoesNotExposeSensitivePayloads(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	sensitiveSecret := "super_secret_user_token_123456789"
	recStore := &failingRecoveryStore{
		candidates: []RecoveryCandidate{
			{
				Occurrence: JobOccurrence{
					ID:            "occ:leak:test",
					JobID:         "job:secret",
					OccurrenceKey: "key:secret",
					State:         OccurrenceReady,
				},
			},
		},
		err: errors.New("database scan failed"),
	}

	manager := NewManagerWithPorts(nil, StorePorts{
		RecoveryCandidates: recStore,
		Recovery:           recStore,
	}, nil)
	manager.accepting = true
	manager.SetLogger(logger)
	manager.definitions["job:secret"] = JobDefinition{
		ID:          "job:secret",
		HandlerType: "secret.handler",
		Payload:     []byte(sensitiveSecret),
		Enabled:     true,
	}

	manager.runRecoveryPass(context.Background())

	for _, entry := range logs.All() {
		// Verify message and context do not contain the raw payload
		if strings.Contains(entry.Message, sensitiveSecret) {
			t.Fatalf("log message leaked sensitive payload: %s", entry.Message)
		}
		for k, v := range entry.ContextMap() {
			valStr := fmt.Sprint(v)
			if strings.Contains(k, sensitiveSecret) || strings.Contains(valStr, sensitiveSecret) {
				t.Fatalf("log field %s=%v leaked sensitive payload", k, v)
			}
		}
	}
}
