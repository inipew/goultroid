package tasks

import "context"

// HandlerFunc is an executable runtime capability resolved from HandlerRef.
// It is deliberately absent from WorkSpec and all persisted Job values.
type HandlerFunc func(context.Context, PayloadRef) (ResultRef, error)

// HandlerResolver resolves a versioned handler reference at the physical
// execution boundary.
type HandlerResolver interface {
	ResolveHandler(HandlerRef) (HandlerFunc, bool)
}

// WorkerSlot describes one physical execution slot and its current generation.
type WorkerSlot struct {
	Pool       PoolID
	WorkerID   WorkerID
	Generation uint64
}

// PhysicalWorkers exposes real worker inventory and one-assignment handoff.
// Assign must not create a second logical backlog.
type PhysicalWorkers interface {
	Slots() []WorkerSlot
	Assign(context.Context, WorkerAssignment, WorkerEvents) error
}
