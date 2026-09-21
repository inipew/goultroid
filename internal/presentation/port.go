package presentation

import "context"

type Target interface {
	PresentationTargetKind() string
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
