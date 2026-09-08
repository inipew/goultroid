package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

type PeerFacade struct { ctx *Context }
func (p *PeerFacade) ResolveUser(ref string) (tg.InputPeerClass, int64, error) { c := p.ctx; if c != nil && c.Resolver != nil { return c.Resolver.ResolveUser(c.Ctx, ref) }; return nil, 0, ErrUnsupported }
func (p *PeerFacade) ResolveChat(ref string) (tg.InputPeerClass, error) { c := p.ctx; if c != nil && c.Resolver != nil { return c.Resolver.ResolveChat(c.Ctx, ref) }; return nil, ErrUnsupported }

// ResolveTargetUser requires a fully usable InputPeer for user-targeted RPCs.
// Returning an InputPeerUser without an access hash is unsafe for arbitrary users
// because Telegram may reject it with PEER_ID_INVALID. Fail closed instead.
func (p *PeerFacade) ResolveTargetUser() (tg.InputPeerClass, int64, error) {
	c := p.ctx
	if c == nil { return nil, 0, errors.New("context is nil") }
	if c.Resolver == nil { return nil, 0, errors.New("peer resolver is not initialized") }

	if len(c.Args) > 0 {
		arg := c.Args[0]
		if uid, err := strconv.ParseInt(arg, 10, 64); err == nil {
			if uid <= 0 { return nil, 0, errors.New("invalid user ID: must be positive") }
			peer, id, err := c.Resolver.ResolveUser(c.Ctx, arg)
			if err != nil || peer == nil || id == 0 { return nil, 0, fmt.Errorf("cannot resolve user %q with a usable peer: %w", arg, err) }
			return peer, id, nil
		}
		if strings.HasPrefix(arg, "@") || (!strings.ContainsAny(arg, " /.:") && len(arg) >= 3) {
			peer, id, err := c.Resolver.ResolveUser(c.Ctx, arg)
			if err == nil && peer != nil && id != 0 { return peer, id, nil }
			username := strings.TrimPrefix(arg, "@")
			if c.Svc != nil {
				resolved, resolveErr := c.ResolveUsername(username)
				if resolveErr == nil && resolved != nil {
					for _, u := range resolved.Users {
						if user, ok := u.(*tg.User); ok && user.ID != 0 { return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, user.ID, nil }
					}
				}
			}
			if err == nil { err = errors.New("resolver returned no usable peer") }
			return nil, 0, fmt.Errorf("cannot resolve user %q: %w", arg, err)
		}
	}

	reply, err := c.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		peer, id, resolveErr := c.Resolver.ResolveUser(c.Ctx, strconv.FormatInt(reply.SenderID, 10))
		if resolveErr == nil && peer != nil && id != 0 { return peer, id, nil }
		if resolveErr == nil { resolveErr = errors.New("resolver returned no usable peer") }
		return nil, 0, fmt.Errorf("cannot resolve replied user %d: %w", reply.SenderID, resolveErr)
	}
	if err != nil { return nil, 0, fmt.Errorf("cannot inspect replied message: %w", err) }
	return nil, 0, errors.New("please provide a valid user ID, username, or reply to a user's message")
}

func (p *PeerFacade) GetFullUser(user tg.InputUserClass) (*tg.UsersUserFull, error) { c := p.ctx; if c == nil || c.Svc == nil { return nil, errors.New("telegram service not initialized") }; return c.Svc.GetFullUser(c.Ctx, user) }
func (p *PeerFacade) ResolveUsername(username string) (*tg.ContactsResolvedPeer, error) { c := p.ctx; if c == nil || c.Svc == nil { return nil, errors.New("telegram service not initialized") }; return c.Svc.ResolveUsername(c.Ctx, username) }
func (p *PeerFacade) GetFullChat() (*tg.MessagesChatFull, error) { c := p.ctx; if c == nil || c.Svc == nil { return nil, errors.New("telegram service not initialized") }; if c.PeerID == nil { return nil, errors.New("peer is nil") }; return c.Svc.GetFullChat(c.Ctx, c.PeerID) }
func (p *PeerFacade) BlockUser(peer tg.InputPeerClass) error { c := p.ctx; if c == nil || c.Svc == nil { return errors.New("telegram service not initialized") }; if peer == nil { return errors.New("peer is nil") }; return c.Svc.BlockUser(c.Ctx, peer) }
func (p *PeerFacade) UnblockUser(peer tg.InputPeerClass) error { c := p.ctx; if c == nil || c.Svc == nil { return errors.New("telegram service not initialized") }; if peer == nil { return errors.New("peer is nil") }; return c.Svc.UnblockUser(c.Ctx, peer) }
