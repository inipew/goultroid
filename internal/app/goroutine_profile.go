package app

import (
	"errors"
	"io"
	"runtime/pprof"
)

// WriteGoroutineProfile writes a full goroutine dump only when requested.
func (a *App) WriteGoroutineProfile(w io.Writer) error {
	if w == nil {
		return errors.New("goroutine profile writer is nil")
	}
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		return errors.New("goroutine profile unavailable")
	}
	return profile.WriteTo(w, 2)
}
