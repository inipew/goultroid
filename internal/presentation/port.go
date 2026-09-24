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

// Media describes one materialized local asset for transport delivery.
type Media struct {
	Type     string
	Path     string
	FileName string
	MIMEType string
	Caption  string
}

// MediaDeliverer is an optional presentation capability for media targets.
type MediaDeliverer interface {
	DeliverMedia(context.Context, Target, Media) error
}

type Port interface {
	Send(context.Context, Target, CompiledView) (Target, error)
	Edit(context.Context, Target, CompiledView) error
	Answer(context.Context, Answer) error
}

// Deleter is an optional presentation capability for transports that can
// remove a concrete interaction target. Keeping it separate from Port avoids
// forcing inline-only or synthetic ports to implement deletion.
type Deleter interface {
	Delete(context.Context, Target) error
}
