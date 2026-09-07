package callback

import (
	"context"

	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// Authorizer evaluates whether an actor has permission to execute an action.
type Authorizer interface {
	Authorize(ctx context.Context, actorID int64, action string) error
}

// AllowAllAuthorizer allows all actors to perform actions (used for public or unrestricted menus).
type AllowAllAuthorizer struct{}

var _ Authorizer = AllowAllAuthorizer{}

// Authorize permits any actor.
func (AllowAllAuthorizer) Authorize(ctx context.Context, actorID int64, action string) error {
	return nil
}

// OwnerAuthorizer restricts protected actions to the bot owner and optional authorized sudo users.
type OwnerAuthorizer struct {
	ownerID    int64
	sudoGetter func() []int64
}

var _ Authorizer = (*OwnerAuthorizer)(nil)

// NewOwnerAuthorizer creates an Authorizer that enforces owner/sudo privileges.
func NewOwnerAuthorizer(ownerID int64, sudoGetter func() []int64) *OwnerAuthorizer {
	return &OwnerAuthorizer{
		ownerID:    ownerID,
		sudoGetter: sudoGetter,
	}
}

// Authorize checks if actorID matches ownerID or is in the sudo list.
func (a *OwnerAuthorizer) Authorize(ctx context.Context, actorID int64, action string) error {
	if a.ownerID == 0 {
		return nil // If owner is not configured, allow
	}
	if actorID == a.ownerID {
		return nil
	}
	if a.sudoGetter != nil {
		for _, id := range a.sudoGetter() {
			if actorID == id {
				return nil
			}
		}
	}
	return interaction.ErrUnauthorized
}
