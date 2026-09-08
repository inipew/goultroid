package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

type PeerFacade struct{ ctx *Context }

func (p *PeerFacade) ResolveUser(ref string) (tg.InputPeerClass, int64, error) {
	c := p.ctx
	if c != nil && c.Resolver != nil {
		return c.Resolver.ResolveUser(c.Ctx, ref)
	}
	return nil, 0, ErrUnsupported
}

func (p *PeerFacade) ResolveChat(ref string) (tg.InputPeerClass, error) {
	c := p.ctx
	if c != nil && c.Resolver != nil {
		return c.Resolver.ResolveChat(c.Ctx, ref)
	}
	return nil, ErrUnsupported
}

// ResolveTargetUser requires a fully usable InputPeer. It never fabricates an
// InputPeerUser with access_hash=0 for arbitrary users.
func (p *PeerFacade) ResolveTargetUser() (tg.InputPeerClass, int64, error) {
	c := p.ctx
	if c == nil {
		return nil, 0, errors.New("context is nil")
	}
	if c.Resolver == nil {
		return nil, 0, errors.New("peer resolver is not initialized")
	}
	if len(c.Args) > 0 {
		arg := c.Args[0]
		if uid, err := strconv.ParseInt(arg, 10, 64); err == nil {
			if uid <= 0 {
				return nil, 0, errors.New("invalid user ID: must be positive")
			}
			peer, id, err := c.Resolver.ResolveUser(c.Ctx, arg)
			if err != nil {
				return nil, 0, fmt.Errorf("cannot resolve user %q: %w", arg, err)
			}
			if peer == nil || id == 0 {
				return nil, 0, fmt.Errorf("cannot resolve user %q: %w", arg, ErrPeerUnresolved)
			}
			input, ok := peer.(*tg.InputPeerUser)
			if !ok || input.AccessHash == 0 {
				return nil, 0, fmt.Errorf("%w: user %d", ErrAccessHashMissing, id)
			}
			return peer, id, nil
		}
		if strings.HasPrefix(arg, "@") || (!strings.ContainsAny(arg, " /.:") && len(arg) >= 3) {
			peer, id, err := c.Resolver.ResolveUser(c.Ctx, arg)
			if err != nil {
				return nil, 0, fmt.Errorf("cannot resolve user %q: %w", arg, err)
			}
			if peer == nil || id == 0 {
				return nil, 0, fmt.Errorf("cannot resolve user %q: %w", arg, ErrPeerUnresolved)
			}
			input, ok := peer.(*tg.InputPeerUser)
			if !ok || input.AccessHash == 0 {
				return nil, 0, fmt.Errorf("%w: user %d", ErrAccessHashMissing, id)
			}
			return peer, id, nil
		}
	}

	reply, err := c.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		peer, id, resolveErr := c.Resolver.ResolveUser(c.Ctx, strconv.FormatInt(reply.SenderID, 10))
		if resolveErr != nil {
			return nil, 0, fmt.Errorf("cannot resolve replied user %d: %w", reply.SenderID, resolveErr)
		}
		if peer == nil || id == 0 {
			return nil, 0, fmt.Errorf("cannot resolve replied user %d: %w", reply.SenderID, ErrPeerUnresolved)
		}
		input, ok := peer.(*tg.InputPeerUser)
		if !ok || input.AccessHash == 0 {
			return nil, 0, fmt.Errorf("%w: user %d", ErrAccessHashMissing, id)
		}
		return peer, id, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("cannot inspect replied message: %w", err)
	}
	return nil, 0, errors.New("please provide a valid user ID, username, or reply to a user's message")
}

func (p *PeerFacade) GetFullUser(user tg.InputUserClass) (*tg.UsersUserFull, error) {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	return c.Svc.GetFullUser(c.Ctx, user)
}

func (p *PeerFacade) ResolveUsername(username string) (*tg.ContactsResolvedPeer, error) {
	c := p.ctx
	if c == nil || c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	return c.Svc.ResolveUsername(c.Ctx, username)
}

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
