// Package jobs provides durable job orchestration over the shared TaskEngine.
//
// Manager is the single orchestration authority. It owns job definitions,
// occurrences, retry/recovery coordination, and schedule integration while all
// physical execution remains delegated to tasks.Client. Durable persistence is
// supplied through responsibility-sized StorePorts; the package does not own a
// second executor, retry engine, or persistence implementation.
package jobs
