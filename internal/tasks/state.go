package tasks

// TaskState is the lifecycle state managed exclusively by TaskEngine.
type TaskState string

const (
	StateCreated   TaskState = "created"
	StateAdmitted  TaskState = "admitted"
	StateQueued    TaskState = "queued"
	StateRunning   TaskState = "running"
	StateCompleted TaskState = "completed"
	StateFailed    TaskState = "failed"
	StateCancelled TaskState = "cancelled"
	StateTimedOut  TaskState = "timed_out"
)
