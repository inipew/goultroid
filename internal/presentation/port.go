package presentation

import (
	"context"

	"github.com/inipew/goultroid/internal/interaction"
)

type Target interface {
	PresentationTargetKind() string
}

// SessionTarget is implemented by transport targets that can derive both the
// partial session binding used before a send and the concrete target binding
// available after a message/inline target exists.
type SessionTarget interface {
	Target
	SessionBinding(actorID int64) interaction.Binding
	TargetBinding() (interaction.TargetBinding, bool)
}

type Answer struct {
	QueryID int64
	Text    string
	Alert   bool
}

type Port interface {
	Send(context.Context, Target, CompiledView) (Target, error)
	Edit(context.Context, Target, CompiledView) error
	Answer(context.Context, Answer) error
}
