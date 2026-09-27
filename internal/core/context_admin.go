package core

import (
	"errors"

	"github.com/gotd/td/tg"
)

// AdminFacade provides a dedicated namespace for group administration and moderation actions.
type AdminFacade struct {
	ctx *Context
}

// Ban restricts a user in the chat until untilDate (0 for permanent).
func (a *AdminFacade) Ban(user tg.InputPeerClass, untilDate int) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().BanUser(c.Ctx, c.PeerID, user, untilDate)
}

// Unban removes restrictions from a user in the chat.
func (a *AdminFacade) Unban(user tg.InputPeerClass) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().UnbanUser(c.Ctx, c.PeerID, user)
}

// Kick kicks a user from the chat.
func (a *AdminFacade) Kick(user tg.InputPeerClass) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().KickUser(c.Ctx, c.PeerID, user)
}

// Mute mutes a user in the chat until the specified unix timestamp (0 for permanent).
func (a *AdminFacade) Mute(user tg.InputPeerClass, untilDate int) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().MuteUser(c.Ctx, c.PeerID, user, untilDate)
}

// Unmute unmutes a user in the chat.
func (a *AdminFacade) Unmute(user tg.InputPeerClass) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().UnmuteUser(c.Ctx, c.PeerID, user)
}

// Promote promotes a user to administrator in the current chat.
func (a *AdminFacade) Promote(user tg.InputPeerClass, title string) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().PromoteAdmin(c.Ctx, c.PeerID, user, title)
}

// Demote demotes an administrator to a regular member in the current chat.
func (a *AdminFacade) Demote(user tg.InputPeerClass) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().DemoteAdmin(c.Ctx, c.PeerID, user)
}

// SetChatPermissions updates default permissions / locks for all members in the chat.
func (a *AdminFacade) SetChatPermissions(rights tg.ChatBannedRights) error {
	c := a.ctx
	if c == nil || c.adminServicer() == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.adminServicer().EditChatDefaultBannedRights(c.Ctx, c.PeerID, rights)
}
