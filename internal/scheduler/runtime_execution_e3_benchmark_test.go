package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/inipew/goultroid/internal/jobs"
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
