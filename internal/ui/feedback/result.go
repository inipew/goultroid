package feedback

// Status indicates the outcome of an operation.
type Status string

const (
	StatusSuccess Status = "success"
	StatusError   Status = "error"
	StatusPending Status = "pending"
)

// OperationResult is a typed container for UI operation outcomes.
// It carries both the domain value and a user-safe notification.
type OperationResult[T any] struct {
	Value        T
	Status       Status
	Message      string
	Retryable    bool
	Notification string
}

// Success creates a successful result.
func Success[T any](value T, message string) OperationResult[T] {
	return OperationResult[T]{
		Value:   value,
		Status:  StatusSuccess,
		Message: message,
	}
}

// Failure creates a failed result with user-safe message.
func Failure[T any](message string, retryable bool) OperationResult[T] {
	return OperationResult[T]{
		Status:    StatusError,
		Message:   message,
		Retryable: retryable,
	}
}

// IsSuccess reports whether the result is successful.
func (r OperationResult[T]) IsSuccess() bool {
	return r.Status == StatusSuccess
}
