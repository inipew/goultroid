package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type u6CommitFaultMode uint8

const (
	u6FaultNone u6CommitFaultMode = iota
	u6FaultBeforeWrite
	u6FaultAfterWrite
)

// u6FaultAttemptStore wraps a jobs.AttemptStore to deterministically inject
// pre-write or post-write CommitAttemptResult failures in a concurrency-safe way.
type u6FaultAttemptStore struct {
	inner jobs.AttemptStore

	mu            sync.Mutex
	mode          u6CommitFaultMode
	faultErr      error
	commitCalls   int
	faultEpoch    uint64
	faultSeq      uint64
	faultNotifyCh chan struct{}
}

var _ jobs.AttemptStore = (*u6FaultAttemptStore)(nil)

func newU6FaultAttemptStore(inner jobs.AttemptStore) *u6FaultAttemptStore {
	return &u6FaultAttemptStore{
		inner:         inner,
		faultNotifyCh: make(chan struct{}),
	}
}

func u6WrapPortsWithFaultAttemptStore(ports jobs.StorePorts) (jobs.StorePorts, *u6FaultAttemptStore) {
	wrapped := newU6FaultAttemptStore(ports.Attempts)
	ports.Attempts = wrapped
	return ports, wrapped
}

func (s *u6FaultAttemptStore) broadcastFaultStateLocked() {
	close(s.faultNotifyCh)
	s.faultNotifyCh = make(chan struct{})
}

func (s *u6FaultAttemptStore) SetCommitFault(mode u6CommitFaultMode, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
	s.faultErr = err
	s.faultEpoch++
	s.faultSeq = 0
	s.broadcastFaultStateLocked()
}

func (s *u6FaultAttemptStore) ClearCommitFault() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = u6FaultNone
	s.faultErr = nil
	s.faultEpoch++
	s.faultSeq = 0
	s.broadcastFaultStateLocked()
}

func (s *u6FaultAttemptStore) CommitCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitCalls
}

// FaultToken returns the current fault epoch and sequence number so callers
// can wait deterministically for subsequent faults without stale false-positives.
func (s *u6FaultAttemptStore) FaultToken() (epoch uint64, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.faultEpoch, s.faultSeq
}

// WaitNextFault blocks until a fault occurs after (afterEpoch, afterSeq) in the
// same epoch, the epoch is cleared/changed, or ctx is canceled. When multiple
// faults occur before the waiter resumes, it returns afterSeq+1 so callers
// consume every fault sequence in order without skipping.
func (s *u6FaultAttemptStore) WaitNextFault(ctx context.Context, afterEpoch, afterSeq uint64) (uint64, error) {
	for {
		s.mu.Lock()
		if s.faultEpoch != afterEpoch {
			s.mu.Unlock()
			return 0, errors.New("fault epoch changed or cleared")
		}
		if s.faultSeq > afterSeq {
			s.mu.Unlock()
			return afterSeq + 1, nil
		}
		ch := s.faultNotifyCh
		s.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (s *u6FaultAttemptStore) notifyFault(expectedEpoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.faultEpoch != expectedEpoch || s.mode == u6FaultNone {
		return
	}
	s.faultSeq++
	s.broadcastFaultStateLocked()
}

func (s *u6FaultAttemptStore) PrepareAttemptLease(ctx context.Context, occurrenceID, taskID string, lease time.Duration) (*jobs.JobAttempt, error) {
	return s.inner.PrepareAttemptLease(ctx, occurrenceID, taskID, lease)
}

func (s *u6FaultAttemptStore) CommitAttemptResult(ctx context.Context, attemptID string, leaseEpoch uint64, state jobs.AttemptState, result []byte, errMsg string) error {
	s.mu.Lock()
	s.commitCalls++
	mode := s.mode
	faultErr := s.faultErr
	epoch := s.faultEpoch
	s.mu.Unlock()

	if mode == u6FaultBeforeWrite && faultErr != nil {
		s.notifyFault(epoch)
		return faultErr
	}

	err := s.inner.CommitAttemptResult(ctx, attemptID, leaseEpoch, state, result, errMsg)
	if err != nil {
		return err
	}

	if mode == u6FaultAfterWrite && faultErr != nil {
		s.notifyFault(epoch)
		return faultErr
	}
	return nil
}

func (s *u6FaultAttemptStore) CommitAttemptDeferred(ctx context.Context, attemptID string, leaseEpoch uint64, readyAt time.Time, reason string) error {
	return s.inner.CommitAttemptDeferred(ctx, attemptID, leaseEpoch, readyAt, reason)
}

func (s *u6FaultAttemptStore) CountAttempts(ctx context.Context, occurrenceID string) (int, error) {
	return s.inner.CountAttempts(ctx, occurrenceID)
}

func (s *u6FaultAttemptStore) CountRetryBudgetUses(ctx context.Context, occurrenceID string) (int, error) {
	return s.inner.CountRetryBudgetUses(ctx, occurrenceID)
}

func (s *u6FaultAttemptStore) CountDeferrals(ctx context.Context, occurrenceID string) (int, error) {
	return s.inner.CountDeferrals(ctx, occurrenceID)
}

func (s *u6FaultAttemptStore) LatestAttempt(ctx context.Context, occurrenceID string) (*jobs.JobAttempt, error) {
	return s.inner.LatestAttempt(ctx, occurrenceID)
}

// u6SQLiteLockHolder holds an exclusive SQLite transaction on a separate
// connection until Release() is called or the test finishes.
type u6SQLiteLockHolder struct {
	releaseOnce sync.Once
	releaseCh   chan struct{}
	doneCh      chan struct{}
	mu          sync.Mutex
	releaseErr  error
}

func acquireU6SQLiteExclusiveLock(t *testing.T, dbPath string, timeout time.Duration) *u6SQLiteLockHolder {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatalf("open lock holder sqlite db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("acquire sqlite conn for exclusive lock: %v", err)
	}

	holder := &u6SQLiteLockHolder{
		releaseCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	acquiredCh := make(chan error, 1)

	go func() {
		defer close(holder.doneCh)
		defer func() { _ = db.Close() }()
		defer func() { _ = conn.Close() }()

		lockCtx, lockCancel := context.WithTimeout(context.Background(), timeout)
		_, execErr := conn.ExecContext(lockCtx, "BEGIN EXCLUSIVE")
		lockCancel()
		if execErr != nil {
			acquiredCh <- execErr
			return
		}
		acquiredCh <- nil

		<-holder.releaseCh

		rbCtx, rbCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, rbErr := conn.ExecContext(rbCtx, "ROLLBACK")
		rbCancel()
		if rbErr != nil && !errors.Is(rbErr, sql.ErrConnDone) {
			holder.mu.Lock()
			holder.releaseErr = fmt.Errorf("rollback exclusive lock: %w", rbErr)
			holder.mu.Unlock()
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case acqErr := <-acquiredCh:
		if acqErr != nil {
			holder.Release(t)
			t.Fatalf("BEGIN EXCLUSIVE failed: %v", acqErr)
		}
	case <-timer.C:
		holder.Release(t)
		t.Fatalf("timed out after %v acquiring BEGIN EXCLUSIVE", timeout)
	}

	t.Cleanup(func() {
		holder.Release(t)
	})

	return holder
}

func (h *u6SQLiteLockHolder) Release(t *testing.T) {
	t.Helper()
	h.releaseOnce.Do(func() {
		close(h.releaseCh)
	})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-h.doneCh:
	case <-timer.C:
		t.Fatalf("timed out waiting for SQLite lock holder goroutine to exit")
	}

	h.mu.Lock()
	err := h.releaseErr
	h.mu.Unlock()
	if err != nil {
		t.Fatalf("SQLite lock holder release error: %v", err)
	}
}

func TestU6Fixtures_FaultAttemptStoreModesAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	basePorts := jobsqlite.Ports(jobsqlite.NewStore(db.DB))
	ports, faultStore := u6WrapPortsWithFaultAttemptStore(basePorts)

	if err := ports.Definitions.SaveDefinition(ctx, &jobs.JobDefinition{
		ID:          "fixture-job",
		Enabled:     true,
		HandlerType: "noop",
	}); err != nil {
		t.Fatalf("SaveDefinition: %v", err)
	}
	if err := ports.Occurrences.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
		ID:            "occ-1",
		JobID:         "fixture-job",
		OccurrenceKey: "k1",
		State:         jobs.OccurrenceReady,
	}); err != nil {
		t.Fatalf("MaterializeOccurrence: %v", err)
	}

	att1, err := ports.Attempts.PrepareAttemptLease(ctx, "occ-1", "task-1", time.Minute)
	if err != nil {
		t.Fatalf("PrepareAttemptLease 1: %v", err)
	}

	// Pre-write fault: underlying DB must NOT record completion, and fault notification fires.
	injectedErr := errors.New("injected pre-write failure")
	faultStore.SetCommitFault(u6FaultBeforeWrite, injectedErr)
	ep1, seq1 := faultStore.FaultToken()
	err = ports.Attempts.CommitAttemptResult(ctx, att1.ID, att1.LeaseEpoch, jobs.AttemptCompleted, nil, "")
	if !errors.Is(err, injectedErr) {
		t.Fatalf("expected pre-write fault error, got: %v", err)
	}
	nextSeq1, err := faultStore.WaitNextFault(ctx, ep1, seq1)
	if err != nil || nextSeq1 != seq1+1 {
		t.Fatalf("WaitNextFault pre-write: seq=%d err=%v", nextSeq1, err)
	}
	latest, err := ports.Attempts.LatestAttempt(ctx, "occ-1")
	if err != nil {
		t.Fatalf("LatestAttempt after pre-write fault: %v", err)
	}
	if latest.State == jobs.AttemptCompleted {
		t.Fatalf("pre-write fault must not persist AttemptCompleted")
	}

	// Clear fault and verify recovery works normally, and stale epoch does not false-positive.
	faultStore.ClearCommitFault()
	if err := ports.Attempts.CommitAttemptResult(ctx, att1.ID, att1.LeaseEpoch, jobs.AttemptFailed, nil, "retry"); err != nil {
		t.Fatalf("CommitAttemptResult after ClearCommitFault: %v", err)
	}
	if _, err := faultStore.WaitNextFault(ctx, ep1, seq1); err == nil {
		t.Fatalf("expected stale epoch WaitNextFault to fail after ClearCommitFault")
	}

	// Post-write fault: underlying DB DOES persist, while caller receives injected error and notification fires.
	att2, err := ports.Attempts.PrepareAttemptLease(ctx, "occ-1", "task-2", time.Minute)
	if err != nil {
		t.Fatalf("PrepareAttemptLease 2: %v", err)
	}
	postErr := errors.New("injected post-write ack loss")
	faultStore.SetCommitFault(u6FaultAfterWrite, postErr)
	ep2, seq2 := faultStore.FaultToken()
	err = ports.Attempts.CommitAttemptResult(ctx, att2.ID, att2.LeaseEpoch, jobs.AttemptCompleted, nil, "")
	if !errors.Is(err, postErr) {
		t.Fatalf("expected post-write fault error, got: %v", err)
	}
	nextSeq2, err := faultStore.WaitNextFault(ctx, ep2, seq2)
	if err != nil || nextSeq2 != seq2+1 {
		t.Fatalf("WaitNextFault post-write: seq=%d err=%v", nextSeq2, err)
	}
	// Waiting for a second fault in the same epoch without another fault must block until ctx timeout.
	shortCtx, shortCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer shortCancel()
	if _, err := faultStore.WaitNextFault(shortCtx, ep2, nextSeq2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded waiting for non-existent fault, got: %v", err)
	}
	latest2, err := ports.Attempts.LatestAttempt(ctx, "occ-1")
	if err != nil {
		t.Fatalf("LatestAttempt after post-write fault: %v", err)
	}
	if latest2.State != jobs.AttemptCompleted {
		t.Fatalf("post-write fault expected persisted AttemptCompleted, got %s", latest2.State)
	}
}

func TestU6Fixtures_FaultNotificationConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	ports, faultStore := u6WrapPortsWithFaultAttemptStore(jobsqlite.Ports(jobsqlite.NewStore(db.DB)))

	const writers = 4
	var writersWG sync.WaitGroup
	writersWG.Add(writers)
	startGate := make(chan struct{})

	faultStore.SetCommitFault(u6FaultBeforeWrite, errors.New("concurrent fault"))
	ep, seq := faultStore.FaultToken()

	for i := 0; i < writers; i++ {
		go func(idx int) {
			defer writersWG.Done()
			<-startGate
			_ = ports.Attempts.CommitAttemptResult(ctx, fmt.Sprintf("att-%d", idx), 1, jobs.AttemptCompleted, nil, "")
		}(i)
	}

	// Accumulate all concurrent faults deterministically before consuming.
	close(startGate)
	writersWG.Wait()

	gotEp, accumulatedSeq := faultStore.FaultToken()
	if gotEp != ep || accumulatedSeq != seq+uint64(writers) {
		t.Fatalf("expected accumulated fault token=(%d, %d), got (%d, %d)", ep, seq+uint64(writers), gotEp, accumulatedSeq)
	}

	curSeq := seq
	for i := 0; i < writers; i++ {
		nextSeq, werr := faultStore.WaitNextFault(ctx, ep, curSeq)
		if werr != nil {
			t.Fatalf("WaitNextFault iteration %d failed: %v", i, werr)
		}
		if nextSeq != curSeq+1 {
			t.Fatalf("WaitNextFault skipped sequence: got %d, want %d", nextSeq, curSeq+1)
		}
		curSeq = nextSeq
	}

	if curSeq != seq+uint64(writers) {
		t.Fatalf("expected final consumed sequence=%d, got %d", seq+uint64(writers), curSeq)
	}
	faultStore.ClearCommitFault()
}

func TestU6Fixtures_SQLiteExclusiveLockHolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbPath := t.TempDir() + "/locktest.db"
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(50)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open primary sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	lockHolder := acquireU6SQLiteExclusiveLock(t, dbPath, 2*time.Second)

	// Write must fail with SQLITE_BUSY or SQLITE_LOCKED while exclusive lock is held.
	_, err = db.ExecContext(ctx, `INSERT INTO items (id) VALUES (1)`)
	if err == nil {
		t.Fatalf("expected SQLite write to fail while exclusive lock is held")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected *sqlite.Error, got %T: %v", err, err)
	}
	primaryCode := sqliteErr.Code() & 0xff
	if primaryCode != sqlite3.SQLITE_BUSY && primaryCode != sqlite3.SQLITE_LOCKED {
		t.Fatalf("expected SQLITE_BUSY (%d) or SQLITE_LOCKED (%d), got code=%d (primary=%d): %v",
			sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqliteErr.Code(), primaryCode, err)
	}

	lockHolder.Release(t)

	// Write must succeed deterministically after lock is released.
	if _, err := db.ExecContext(ctx, `INSERT INTO items (id) VALUES (1)`); err != nil {
		t.Fatalf("expected SQLite write to succeed after lock release, got: %v", err)
	}
}
