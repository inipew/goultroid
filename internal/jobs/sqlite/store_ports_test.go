package sqlite

import (
	"testing"

	"github.com/inipew/goultroid/internal/jobs"
)

var (
	_ jobs.Store                 = (*Store)(nil)
	_ jobs.ScheduleStore         = (*Store)(nil)
	_ jobs.OutboxStore           = (*Store)(nil)
	_ jobs.DeferredDeadlineStore = (*Store)(nil)
	_ jobs.DurableDiagnosticsStore = (*Store)(nil)
	_ jobs.AttemptSummaryStore     = (*Store)(nil)
	_ jobs.NextAttemptLeaseStore   = (*Store)(nil)
	_ jobs.RecoveryCandidateStore  = (*Store)(nil)
	_ jobs.DefinitionLoaderStore   = (*Store)(nil)

	_ jobs.Store                 = (*ResourceStore)(nil)
	_ jobs.ScheduleStore         = (*ResourceStore)(nil)
	_ jobs.OutboxStore           = (*ResourceStore)(nil)
	_ jobs.DeferredDeadlineStore = (*ResourceStore)(nil)
	_ jobs.DurableDiagnosticsStore = (*ResourceStore)(nil)
	_ jobs.AttemptSummaryStore     = (*ResourceStore)(nil)
	_ jobs.NextAttemptLeaseStore   = (*ResourceStore)(nil)
	_ jobs.RecoveryCandidateStore  = (*ResourceStore)(nil)
	_ jobs.DefinitionLoaderStore   = (*ResourceStore)(nil)
)

func TestStorePortsFromSQLiteStorePreservesOptionalCapabilities(t *testing.T) {
	ports := jobs.StorePortsFromStore(NewStore(nil))
	if ports.Definitions == nil || ports.Occurrences == nil || ports.Attempts == nil || ports.Recovery == nil {
		t.Fatal("core SQLite store ports were not populated")
	}
	if ports.Schedules == nil || ports.Outbox == nil || ports.DeferredDeadlines == nil ||
		ports.Diagnostics == nil || ports.AttemptSummaries == nil || ports.NextAttemptLeases == nil ||
		ports.RecoveryCandidates == nil || ports.DefinitionLoader == nil {
		t.Fatalf("optional SQLite store ports were not preserved: %+v", ports)
	}
}
