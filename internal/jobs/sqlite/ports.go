package sqlite

import "github.com/inipew/goultroid/internal/jobs"

// Ports exposes the durable capabilities implemented by the base SQLite store
// without routing through an aggregate compatibility interface.
func Ports(store *Store) jobs.StorePorts {
	if store == nil {
		return jobs.StorePorts{}
	}
	return jobs.StorePorts{
		Definitions:        store,
		Occurrences:        store,
		Attempts:           store,
		Recovery:           store,
		Schedules:          store,
		Outbox:             store,
		DeferredDeadlines:  store,
		Diagnostics:        store,
		AttemptSummaries:   store,
		NextAttemptLeases:  store,
		RecoveryCandidates: store,
		DefinitionLoader:   store,
	}
}

// ResourcePorts is the production port set. ResourceStore remains the
// definition/loader authority so explicit resource profiles use its overrides.
func ResourcePorts(store *ResourceStore) jobs.StorePorts {
	if store == nil {
		return jobs.StorePorts{}
	}
	return jobs.StorePorts{
		Definitions:        store,
		Occurrences:        store,
		Attempts:           store,
		Recovery:           store,
		Schedules:          store,
		Outbox:             store,
		DeferredDeadlines:  store,
		Diagnostics:        store,
		AttemptSummaries:   store,
		NextAttemptLeases:  store,
		RecoveryCandidates: store,
		DefinitionLoader:   store,
	}
}
