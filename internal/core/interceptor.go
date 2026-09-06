package core

// InterceptDecision controls whether message processing continues after an interceptor.
type InterceptDecision uint8

const (
	// InterceptContinue allows subsequent interceptors and command execution.
	InterceptContinue InterceptDecision = iota
	// InterceptHandled stops processing because the interceptor handled the message.
	InterceptHandled
)
