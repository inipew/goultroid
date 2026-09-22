package core

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
)

// GroupRoleRequest identifies one chat-scoped Telegram principal lookup.
type GroupRoleRequest struct {
	ChatID int64
	Kind   ChatKind
	Peer   tg.InputPeerClass
	UserID int64
}

// GroupRoleSnapshot is an authoritative role observation. Cached only means the
// observation came from the resolver's bounded TTL cache; Verified remains true
// only for a previously successful authoritative Telegram lookup.
type GroupRoleSnapshot struct {
	Principal  GroupActorPrincipal
	ObservedAt time.Time
	Cached     bool
}

// GroupRoleResolver resolves Telegram chat-scoped roles independently from the
// global Goultroid Owner/Sudo permission model.
type GroupRoleResolver interface {
	ResolveGroupRole(context.Context, GroupRoleRequest) (GroupRoleSnapshot, error)
	ResolveGroupRoleFresh(context.Context, GroupRoleRequest) (GroupRoleSnapshot, error)
}

// ResolveGroupActor resolves the triggering actor's contextual Telegram role.
// fresh=true bypasses the bounded cache and is intended for mutation-time
// revalidation in P7-C/P7-G.
func (c *Context) ResolveGroupActor(fresh bool) (GroupRoleSnapshot, error) {
	if c == nil || c.Chat == nil || !c.IsManagerGroup() {
		return GroupRoleSnapshot{}, ErrGroupOnly
	}
	if c.GroupRoles == nil {
		return GroupRoleSnapshot{}, fmt.Errorf("%w: group role resolver is not configured", ErrUnavailable)
	}
	userID := c.SenderID()
	if userID <= 0 {
		return GroupRoleSnapshot{}, ErrUnauthorized
	}

	request := GroupRoleRequest{
		ChatID: c.Chat.ID,
		Kind:   c.Chat.Kind(),
		Peer:   c.PeerID,
		UserID: userID,
	}
	var (
		snapshot GroupRoleSnapshot
		err      error
	)
	if fresh {
		snapshot, err = c.GroupRoles.ResolveGroupRoleFresh(c.Ctx, request)
	} else {
		snapshot, err = c.GroupRoles.ResolveGroupRole(c.Ctx, request)
	}
	if err != nil {
		return GroupRoleSnapshot{}, err
	}
	if !snapshot.Principal.Verified || snapshot.Principal.UserID != userID {
		return GroupRoleSnapshot{}, fmt.Errorf("%w: resolver returned an unverified or mismatched principal", ErrUnavailable)
	}

	if principal := c.GetPrincipal(); principal != nil {
		snapshot.Principal.IsOwner = principal.IsOwner
		snapshot.Principal.IsSudo = principal.IsSudo
	}
	contextual := snapshot.Principal
	c.GroupPrincipal = &contextual
	return snapshot, nil
}
