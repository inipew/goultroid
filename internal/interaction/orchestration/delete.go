package orchestration

import (
	"errors"

	"github.com/inipew/goultroid/internal/presentation"
)

var ErrDeleteUnsupported = errors.New("interaction/orchestration: presentation target deletion is unsupported")

// Delete removes the concrete presentation target when the active transport
// exposes the optional presentation.Deleter capability.
func (c *Context) Delete() error {
	if c == nil || c.engine == nil || c.target == nil {
		return ErrInvalidEngine
	}
	deleter, ok := c.engine.port.(presentation.Deleter)
	if !ok || deleter == nil {
		return ErrDeleteUnsupported
	}
	return deleter.Delete(c.Context(), c.target)
}
