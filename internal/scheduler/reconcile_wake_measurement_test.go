package scheduler

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"
)

// This fixture measures the actual scheduler loop after coalesced wake signals.
// It uses separate, in-memory SQLite stores as the runtime harness does.
func TestSyntheticLegacyClaimNotificationReconciliation(t *testing.T) {
	for _, claimCount := range []int{1, 100, 1000} {
		t.Run(fmt.Sprintf("claims=%d", claimCount), func(t *testing.T) {
			h := newTimingHarness(t)
			ctx := context.Background()
			now := time.Now().UTC()
			if _, err := h.jobDB.ExecContext(ctx, `INSERT INTO job_definitions
				(id, scope_owner, quota_owner, handler_type, updated_at)
				VALUES ('wake-fixture', 'test', 'test', 'test', ?)`, now); err != nil {
				t.Fatal(err)
			}
			rows, err := h.repo.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			occurrences, err := h.jobDB.BeginTx(ctx, nil)
			if err != nil {
				_ = rows.Rollback()
				t.Fatal(err)
			}
			for i := 1; i <= claimCount; i++ {
				id := int64(i)
				token := fmt.Sprintf("claim-%d", i)
				occurrenceID := fmt.Sprintf("occ-%d", i)
				if _, err := rows.ExecContext(ctx, `INSERT INTO scheduled_jobs
					(id, chat_id, action_type, payload, next_run_at, created_at, status, claim_token)
					VALUES (?, 1, 'job', 'wake-fixture', ?, ?, 'running', ?)`, id, now, now, token); err != nil {
					_ = rows.Rollback()
					_ = occurrences.Rollback()
					t.Fatal(err)
				}
				if _, err := occurrences.ExecContext(ctx, `INSERT INTO job_occurrences
					(id, job_id, scheduled_for, occurrence_key, state, ready_at, updated_at)
					VALUES (?, 'wake-fixture', ?, ?, 'ready', ?, ?)`, occurrenceID, now, occurrenceID, now, now); err != nil {
					_ = rows.Rollback()
					_ = occurrences.Rollback()
					t.Fatal(err)
				}
				h.sched.trackClaim(id, token, "wake-fixture", occurrenceID)
			}
			if err := rows.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := occurrences.Commit(); err != nil {
				t.Fatal(err)
			}

			const passes = 10
			latencies := make([]time.Duration, 0, passes)
			started := time.Now()
			for i := 0; i < passes; i++ {
				idleDeadline := time.Now().Add(10 * time.Second)
				for {
					stats := h.sched.Diagnostics()
					if stats.ReconcilePasses == stats.CompletedReconcilePasses {
						break
					}
					if time.Now().After(idleDeadline) {
						t.Fatalf("scheduler reconciliation did not become idle: %+v", stats)
					}
					time.Sleep(time.Millisecond)
				}
				before := h.sched.Diagnostics()
				wakeAt := time.Now()
				h.sched.notifyWake()
				deadline := wakeAt.Add(10 * time.Second)
				for {
					after := h.sched.Diagnostics()
					if after.NotificationWakes > before.NotificationWakes &&
						after.CompletedReconcilePasses > before.CompletedReconcilePasses &&
						after.ClaimsInspected >= before.ClaimsInspected+uint64(claimCount) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("notification pass did not settle: before=%+v after=%+v", before, after)
					}
					time.Sleep(time.Millisecond)
				}
				latencies = append(latencies, time.Since(wakeAt))
			}
			elapsed := time.Since(started)
			stats := h.sched.Diagnostics()
			if stats.ActiveLegacyClaims != claimCount {
				t.Fatalf("active claims = %d, want %d", stats.ActiveLegacyClaims, claimCount)
			}
			if stats.ScheduledReads < uint64(claimCount*passes) || stats.OccurrenceReads < uint64(claimCount*passes) {
				t.Fatalf("read counters below ten passes: %+v", stats)
			}
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			percentile := func(p int) time.Duration { return latencies[(p*len(latencies)+99)/100-1] }
			t.Logf("claims=%d passes=%d elapsed=%s notification_passes/s=%.1f reads/s=%.0f p50=%s p95=%s p99=%s stats=%+v",
				claimCount, passes, elapsed, float64(passes)/elapsed.Seconds(),
				float64(2*claimCount*passes)/elapsed.Seconds(), percentile(50), percentile(95), percentile(99), stats)
		})
	}
}
