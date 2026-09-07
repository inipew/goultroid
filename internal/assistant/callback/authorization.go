package callback

import (
	"context"
)

// Actor encapsulates caller identification and authorization attributes.
type Actor struct {
	UserID  int64
	ChatID  int64
	IsOwner bool
	IsSudo  bool
}

// Authorizer evaluates whether an actor has permission to execute an action.
type Authorizer interface {
	Authorize(ctx context.Context, actor Actor, action string) error
}

// AllowAllAuthorizer allows all actors to perform actions (used for public or unrestricted menus).
type AllowAllAuthorizer struct{}

var _ Authorizer = AllowAllAuthorizer{}

// Authorize permits any actor.
func (AllowAllAuthorizer) Authorize(ctx context.Context, actor Actor, action string) error {
	return nil
}

// OwnerAuthorizer restricts protected actions strictly to the bot owner and optional authorized sudo users.
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

// Authorize checks if actor matches ownerID or is in the sudo list.
// If ownerID is 0, it fails closed to prevent unauthorized execution.
func (a *OwnerAuthorizer) Authorize(ctx context.Context, actor Actor, action string) error {
	if a.ownerID == 0 {
		return ErrUnauthorized // Fail closed if ownerID is not configured
	}
	if actor.IsOwner || actor.UserID == a.ownerID {
		return nil
	}
	if actor.IsSudo {
		return nil
	}
	if a.sudoGetter != nil {
		for _, id := range a.sudoGetter() {
			if actor.UserID == id {
				return nil
			}
		}
	}
	return ErrUnauthorized
}
