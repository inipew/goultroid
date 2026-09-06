package core

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

// PeerFacade provides entity and peer resolution capabilities.
type PeerFacade struct {
	ctx *Context
}

// ResolveUser resolves a user reference (ID, @username, phone) using the injected PeerResolver.
func (p *PeerFacade) ResolveUser(ref string) (tg.InputPeerClass, int64, error) {
	c := p.ctx
	if c != nil && c.Resolver != nil {
		return c.Resolver.ResolveUser(c.Ctx, ref)
	}
	return nil, 0, ErrUnsupported
}

// ResolveChat resolves a chat reference (ID, @username) using the injected PeerResolver.
func (p *PeerFacade) ResolveChat(ref string) (tg.InputPeerClass, error) {
	c := p.ctx
	if c != nil && c.Resolver != nil {
		return c.Resolver.ResolveChat(c.Ctx, ref)
	}
	return nil, ErrUnsupported
}

// ResolveTargetUser extracts the target user's InputPeer and UserID from args (numeric ID or @username) or from replied message.
// It leverages PeerResolver to obtain full access hashes whenever available.
func (p *PeerFacade) ResolveTargetUser() (tg.InputPeerClass, int64, error) {
	c := p.ctx
	if c == nil {
		return nil, 0, errors.New("context is nil")
	}

	if len(c.Args) > 0 {
		arg := c.Args[0]
		// 1. Numeric ID
		if uid, err := strconv.ParseInt(arg, 10, 64); err == nil && uid != 0 {
			if c.Resolver != nil {
				peer, id, err := c.Resolver.ResolveUser(c.Ctx, arg)
				if err == nil && peer != nil {
					return peer, id, nil
				}
			}
			return &tg.InputPeerUser{UserID: uid}, uid, nil
		}

		// 2. Username (@username or username)
		if strings.HasPrefix(arg, "@") || (!strings.ContainsAny(arg, " /.:") && len(arg) >= 3) {
			if c.Resolver != nil {
				peer, id, err := c.Resolver.ResolveUser(c.Ctx, arg)
				if err == nil && peer != nil {
					return peer, id, nil
				}
			}

			// Fallback to legacy ResolveUsername
			username := strings.TrimPrefix(arg, "@")
			if c.Svc != nil {
				resolved, err := c.ResolveUsername(username)
				if err == nil && resolved != nil {
					for _, u := range resolved.Users {
						if user, ok := u.(*tg.User); ok {
							return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, user.ID, nil
						}
					}
				}
			}
		}
	}

	// 3. Reply to user
	reply, err := c.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		if c.Resolver != nil {
			peer, id, err := c.Resolver.ResolveUser(c.Ctx, strconv.FormatInt(reply.SenderID, 10))
			if err == nil && peer != nil {
				return peer, id, nil
			}
		}
		return &tg.InputPeerUser{UserID: reply.SenderID}, reply.SenderID, nil
	}

	return nil, 0, errors.New("please provide a valid user ID, username, or reply to a user's message")
}

// GetFullUser fetches detailed user information.
func (p *PeerFacade) GetFullUser(user tg.InputUserClass) (*tg.UsersUserFull, error) {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	return c.Svc.GetFullUser(c.Ctx, user)
}

// ResolveUsername resolves a public username to user/chat entities.
func (p *PeerFacade) ResolveUsername(username string) (*tg.ContactsResolvedPeer, error) {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	return c.Svc.ResolveUsername(c.Ctx, username)
}

// GetFullChat fetches detailed chat/channel information for the current chat.
func (p *PeerFacade) GetFullChat() (*tg.MessagesChatFull, error) {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return nil, errors.New("peer is nil")
	}
	return c.Svc.GetFullChat(c.Ctx, c.PeerID)
}

// BlockUser adds a peer to the account blocklist.
func (p *PeerFacade) BlockUser(peer tg.InputPeerClass) error {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if peer == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.BlockUser(c.Ctx, peer)
}

// UnblockUser removes a peer from the account blocklist.
func (p *PeerFacade) UnblockUser(peer tg.InputPeerClass) error {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if peer == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.UnblockUser(c.Ctx, peer)
}

