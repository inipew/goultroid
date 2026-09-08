package clone

import (
	"context"
	"time"
)

type CloneState struct {
	OwnerID       int64
	OriginalFirst string
	OriginalLast  string
	OriginalBio   string
	OriginalPhoto string
	ClonedPhoto   bool
	Active        bool
	UpdatedAt     time.Time
}

type Repository interface {
	GetCloneState(context.Context, int64) (*CloneState, error)
	SaveCloneState(context.Context, CloneState) error
	ClearCloneState(context.Context, int64) error
}
