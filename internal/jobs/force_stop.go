package jobs

import (
	"context"

	"github.com/inipew/goultroid/internal/runtime"
)

var _ runtime.ForcedStopper = (*PersistencePump)(nil)

// ForceStop closes the pump queue/cancels operation contexts through
// the normal Stop transition, but never extends the supplied deadline.
func (p *PersistencePump) ForceStop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return p.Stop(ctx)
}
