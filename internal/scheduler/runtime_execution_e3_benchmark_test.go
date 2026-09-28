package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	_ "modernc.org/sqlite"
)

type executionReconcileMeasurementRepository struct {
	Repository
	rows  map[int64]*ScheduledJob
	reads atomic.Int64
}

func (r *executionReconcileMeasurementRepository) GetScheduledJob(_ context.Context, id int64) (*ScheduledJob, error) {
	r.reads.Add(1)
	row := r.rows[id]
	if row == nil {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

type executionReconcileMeasurementOccurrenceStore struct {
	*executionReconcileOccurrenceStore
	reads atomic.Int64
}

func (s *executionReconcileMeasurementOccurrenceStore) GetOccurrence(_ context.Context, id string) (*jobs.JobOccurrence, error) {
	s.reads.Add(1)
	return &jobs.JobOccurrence{ID: id, State: jobs.OccurrenceReady}, nil
}

func BenchmarkRuntimeExecutionE3_ReconcileSettledClaims(b *testing.B) {
	for _, claimCount := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("claims=%d", claimCount), func(b *testing.B) {
			repo := &executionReconcileMeasurementRepository{rows: make(map[int64]*ScheduledJob, claimCount)}
			occurrences := &executionReconcileMeasurementOccurrenceStore{
				executionReconcileOccurrenceStore: &executionReconcileOccurrenceStore{},
			}
			manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Occurrences: occurrences}, nil)
			engine := NewEngine(repo, nil)
			engine.SetJobsManager(manager)
			for i := 0; i < claimCount; i++ {
				jobID := int64(i + 1)
				claimToken := fmt.Sprintf("claim-%d", jobID)
				occurrenceID := fmt.Sprintf("occ-%d", jobID)
				repo.rows[jobID] = &ScheduledJob{ID: jobID, Status: JobStatusRunning, ClaimToken: claimToken}
				engine.trackClaim(jobID, claimToken, fmt.Sprintf("definition-%d", jobID), occurrenceID)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				engine.reconcileSettledClaims(context.Background())
			}
			b.StopTimer()
			b.ReportMetric(float64(repo.reads.Load())/float64(b.N), "scheduled_reads/op")
			b.ReportMetric(float64(occurrences.reads.Load())/float64(b.N), "occurrence_reads/op")
		})
	}
}

type executionReconcileSQLiteRepository struct {
	*SQLiteRepository
	reads atomic.Int64
}

func (r *executionReconcileSQLiteRepository) GetScheduledJob(ctx context.Context, id int64) (*ScheduledJob, error) {
	r.reads.Add(1)
	return r.SQLiteRepository.GetScheduledJob(ctx, id)
}

type executionReconcileSQLiteOccurrences struct {
	*jobsqlite.Store
	reads atomic.Int64
}

func (s *executionReconcileSQLiteOccurrences) GetOccurrence(ctx context.Context, id string) (*jobs.JobOccurrence, error) {
	s.reads.Add(1)
	return s.Store.GetOccurrence(ctx, id)
}

// BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite measures the
// existing reconciliation path through the production SQLite repositories.
// Fixtures are created before timing; claims stay active for every pass.
func BenchmarkRuntimeExecutionE3_ReconcileSettledClaimsSQLite(b *testing.B) {
	for _, claimCount := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("claims=%d", claimCount), func(b *testing.B) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			repo := &executionReconcileSQLiteRepository{SQLiteRepository: NewSQLiteRepository(db)}
			if err := repo.InitSchema(ctx); err != nil {
				b.Fatal(err)
			}
			if err := jobsqlite.InitSchema(ctx, db); err != nil {
				b.Fatal(err)
			}
			occurrences := &executionReconcileSQLiteOccurrences{Store: jobsqlite.NewStore(db)}
			manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Occurrences: occurrences}, nil)
			engine := NewEngine(repo, nil)
			engine.SetJobsManager(manager)
			now := time.Now().UTC()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO job_definitions
				(id, scope_owner, quota_owner, handler_type, updated_at)
				VALUES ('reconcile-benchmark', 'benchmark', 'benchmark', 'benchmark', ?)`, now); err != nil {
				b.Fatal(err)
			}
			for i := 1; i <= claimCount; i++ {
				jobID := int64(i)
				claimToken := fmt.Sprintf("claim-%d", i)
				occurrenceID := fmt.Sprintf("occ-%d", i)
				if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_jobs
					(id, chat_id, action_type, payload, next_run_at, created_at, status, claim_token)
					VALUES (?, 1, 'job', 'reconcile-benchmark', ?, ?, 'running', ?)`,
					jobID, now, now, claimToken); err != nil {
					b.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO job_occurrences
					(id, job_id, scheduled_for, occurrence_key, state, ready_at, updated_at)
					VALUES (?, 'reconcile-benchmark', ?, ?, 'ready', ?, ?)`,
					occurrenceID, now, occurrenceID, now, now); err != nil {
					b.Fatal(err)
				}
				engine.trackClaim(jobID, claimToken, "reconcile-benchmark", occurrenceID)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				engine.reconcileSettledClaims(ctx)
			}
			b.StopTimer()
			if count := engine.trackedClaimCount(); count != claimCount {
				b.Fatalf("tracked claims = %d, want %d", count, claimCount)
			}
			b.ReportMetric(float64(repo.reads.Load())/float64(b.N), "scheduled_reads/op")
			b.ReportMetric(float64(occurrences.reads.Load())/float64(b.N), "occurrence_reads/op")
		})
	}
}
