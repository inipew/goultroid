package jobs

import (
	"context"
	"time"
)

// DefinitionStore owns durable job-definition mutation.
type DefinitionStore interface {
	SaveDefinition(context.Context, *JobDefinition) error
	UpdateDefinitionCAS(context.Context, *JobDefinition, uint64) error
}

// OccurrenceStore owns one logical job occurrence and its durable lifecycle.
type OccurrenceStore interface {
	MaterializeOccurrence(context.Context, *JobOccurrence) error
	FinalizeOccurrence(context.Context, string, OccurrenceState) error
	CancelOccurrence(context.Context, string, string) error
	GetOccurrence(context.Context, string) (*JobOccurrence, error)
	GetOccurrenceByKey(context.Context, string) (*JobOccurrence, error)
	DeleteTerminalOccurrences(context.Context, string, time.Time, int) (int64, error)
	DeferOccurrence(context.Context, string, time.Time) error
}

// AttemptStore owns physical-attempt leasing, result persistence, and counters.
type AttemptStore interface {
	PrepareAttemptLease(context.Context, string, string, time.Duration) (*JobAttempt, error)
	CommitAttemptResult(context.Context, string, uint64, AttemptState, []byte, string) error
	CommitAttemptDeferred(context.Context, string, uint64, time.Time, string) error
	CountAttempts(context.Context, string) (int, error)
	CountRetryBudgetUses(context.Context, string) (int, error)
	CountDeferrals(context.Context, string) (int, error)
	LatestAttempt(context.Context, string) (*JobAttempt, error)
}

// RecoveryStore supplies bounded unresolved-occurrence scans.
type RecoveryStore interface {
	ListUnresolvedOccurrences(context.Context, int) ([]*JobOccurrence, error)
}

// ScheduleStore is the durable schedule boundary used by manager_schedule.go.
type ScheduleStore interface {
	SaveSchedule(context.Context, *JobSchedule) error
	DisableSchedule(context.Context, string) error
	ListDueSchedules(context.Context, time.Time, int) ([]JobSchedule, error)
	EarliestScheduleDue(context.Context) (time.Time, bool, error)
	MaterializeDueSchedule(context.Context, string, time.Time) (*JobOccurrence, error)
	SkipDueSchedule(context.Context, string, time.Time) error
	CutoverActive(context.Context) (bool, error)
}

// OutboxStore is the durable outbox boundary used by the coordinator.
type OutboxStore interface {
	ListPendingOutbox(context.Context, int) ([]OutboxEvent, error)
	MarkOutboxDelivered(context.Context, string) error
}

// DeferredDeadlineStore exposes only the next durable deferral deadline.
type DeferredDeadlineStore interface {
	EarliestDeferredOccurrenceDue(context.Context, time.Time) (time.Time, bool, error)
}

// DurableDiagnosticsStore exposes bounded durable diagnostics.
type DurableDiagnosticsStore interface {
	DurableDiagnostics(context.Context, time.Time) (DurableDiagnostics, error)
}

// AttemptSummaryStore provides the compact retry/recovery attempt snapshot.
type AttemptSummaryStore interface {
	AttemptSummary(context.Context, string) (*AttemptSummary, error)
}

// NextAttemptLeaseStore provides the writer-fenced next-attempt fast path.
type NextAttemptLeaseStore interface {
	PrepareNextAttemptLease(context.Context, string, time.Duration) (*JobAttempt, error)
}

// RecoveryCandidateStore provides the compact recovery scan fast path.
type RecoveryCandidateStore interface {
	ListRecoveryCandidates(context.Context, int) ([]RecoveryCandidate, error)
}

// DefinitionLoaderStore loads durable definitions during Manager startup.
type DefinitionLoaderStore interface {
	ListDefinitions(context.Context) ([]JobDefinition, error)
}

// Store is the compatibility aggregate accepted by NewManager while callers
// migrate to NewManagerWithPorts. Optional schedule/outbox/diagnostic fast
// paths are discovered independently.
type Store interface {
	DefinitionStore
	OccurrenceStore
	AttemptStore
	RecoveryStore
}

// StorePorts are consumer-specific durable boundaries for one Manager. A
// production SQLite store may implement every field; focused tests can provide
// only the ports exercised by the responsibility under test.
type StorePorts struct {
	Definitions        DefinitionStore
	Occurrences        OccurrenceStore
	Attempts           AttemptStore
	Recovery           RecoveryStore
	Schedules          ScheduleStore
	Outbox             OutboxStore
	DeferredDeadlines  DeferredDeadlineStore
	Diagnostics        DurableDiagnosticsStore
	AttemptSummaries   AttemptSummaryStore
	NextAttemptLeases  NextAttemptLeaseStore
	RecoveryCandidates RecoveryCandidateStore
	DefinitionLoader   DefinitionLoaderStore
}

func (p StorePorts) coreReady() bool {
	return p.Definitions != nil && p.Occurrences != nil && p.Attempts != nil && p.Recovery != nil
}

// StorePortsFromStore adapts the compatibility aggregate without creating a
// second persistence implementation.
func StorePortsFromStore(store Store) StorePorts {
	if store == nil {
		return StorePorts{}
	}
	ports := StorePorts{
		Definitions: store,
		Occurrences: store,
		Attempts:    store,
		Recovery:    store,
	}
	if capability, ok := any(store).(ScheduleStore); ok {
		ports.Schedules = capability
	}
	if capability, ok := any(store).(OutboxStore); ok {
		ports.Outbox = capability
	}
	if capability, ok := any(store).(DeferredDeadlineStore); ok {
		ports.DeferredDeadlines = capability
	}
	if capability, ok := any(store).(DurableDiagnosticsStore); ok {
		ports.Diagnostics = capability
	}
	if capability, ok := any(store).(AttemptSummaryStore); ok {
		ports.AttemptSummaries = capability
	}
	if capability, ok := any(store).(NextAttemptLeaseStore); ok {
		ports.NextAttemptLeases = capability
	}
	if capability, ok := any(store).(RecoveryCandidateStore); ok {
		ports.RecoveryCandidates = capability
	}
	if capability, ok := any(store).(DefinitionLoaderStore); ok {
		ports.DefinitionLoader = capability
	}
	return ports
}
