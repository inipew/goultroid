package admission

import (
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// OwnerLimits defines fairness quotas and concurrency budgets for an owner (ADR 0006 §5.4).
type OwnerLimits struct {
	MaxWaiting     int   `json:"max_waiting"`
	MaxActive      int   `json:"max_active"`
	Weight         int   `json:"weight"`
	MaxPayloadByte int64 `json:"max_payload_byte"`
}

// DefaultOwnerLimits provides standard default limits.
var DefaultOwnerLimits = OwnerLimits{
	MaxWaiting:     50,
	MaxActive:      10,
	Weight:         1,
	MaxPayloadByte: 50 * 1024 * 1024, // 50 MB
}

// QueueEntry represents one item waiting in the ready admission queue.
type QueueEntry struct {
	Spec        tasks.WorkSpec
	EnqueuedAt  time.Time
	PayloadSize int64
	HeapIndex   int
}

// ClassDeficit holds deficit counters for a priority class.
type ClassDeficit struct {
	Class   tasks.PriorityClass
	Quantum int
	Deficit int
}

// OwnerDeficit holds deficit counters for an owner within a class.
type OwnerDeficit struct {
	Owner   tasks.OwnerID
	Quantum int
	Deficit int
}
