package presentation

import "errors"

var (
	// ErrNilScreen indicates a render attempt on an uninitialized Screen pointer.
	ErrNilScreen = errors.New("assistant/presentation: nil screen")
	// ErrRenderFailed indicates failure during screen rendering.
	ErrRenderFailed = errors.New("assistant/presentation: render failed")
)
