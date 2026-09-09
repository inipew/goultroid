package scheduler

import "time"

// Job is the domain model for a scheduled task.
// It is intentionally separate from scheduler.ScheduledJob (persistence row)
// per P2-15: database row -> mapper -> domain model.
type Job struct {
	ID        int64
	ChatID    int64
	PeerType  string
	Action    string
	Payload   string
	Interval  time.Duration
	NextRunAt time.Time
	Status    string
}

// JobRow is an alias for the persistence representation.
// In a full implementation, scheduler.ScheduledJob would be the DB struct
// and this domain Job would be mapped via ToDomain/FromDomain functions
// in internal/scheduler/repository.go (mapper layer).
// This file demonstrates the boundary; full mapper will be added incrementally.
