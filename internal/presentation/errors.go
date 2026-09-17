package presentation

import "errors"

var (
	// ErrScreenNotFound indicates no registered screen matched the requested key.
	ErrScreenNotFound = errors.New("presentation: screen not found")

	// ErrScreenUnavailable indicates the screen's owning plugin generation is inactive or shutting down.
	ErrScreenUnavailable = errors.New("presentation: screen unavailable")

	// ErrAccessDenied indicates the caller does not meet the screen's access policy.
	ErrAccessDenied = errors.New("presentation: access denied")

	// ErrPrivateRequired indicates the screen can only be rendered in a private conversation.
	ErrPrivateRequired = errors.New("presentation: private chat required")

	// ErrInvalidPresentation indicates the built screen or request violates presentation invariants.
	ErrInvalidPresentation = errors.New("presentation: invalid presentation")

	// ErrBuildTimeout indicates building the screen exceeded the allowed time deadline.
	ErrBuildTimeout = errors.New("presentation: build timeout")

	// ErrOutputValidationFailed indicates the screen failed output validation rules.
	ErrOutputValidationFailed = errors.New("presentation: output validation failed")

	// ErrNilBuilder indicates a nil builder was supplied for registration.
	ErrNilBuilder = errors.New("presentation: builder cannot be nil")

	// ErrDuplicateScreenKey indicates a screen with the same key is already registered.
	ErrDuplicateScreenKey = errors.New("presentation: duplicate screen key")

	// ErrRegistrationClosed indicates an operation was attempted on a closed lease.
	ErrRegistrationClosed = errors.New("presentation: registration lease closed")
)
