package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

// DisplayUser returns an HTML-safe, clickable display name for a user. It
// falls back to the numeric ID when Telegram cannot provide profile details.
func (p *PeerFacade) DisplayUser(peer tg.InputPeerClass, userID int64) string {
	fallback := fmt.Sprintf("<code>%d</code>", userID)
	c := p.ctx
	if c == nil || c.Svc == nil || userID == 0 {
		return fallback
	}
	inputPeer, ok := peer.(*tg.InputPeerUser)
	if !ok || inputPeer == nil || inputPeer.AccessHash == 0 {
		return fallback
	}
	full, err := c.Svc.GetFullUser(c.Ctx, &tg.InputUser{UserID: inputPeer.UserID, AccessHash: inputPeer.AccessHash})
	if err != nil || full == nil {
		return fallback
	}
	for _, userClass := range full.Users {
		user, ok := userClass.(*tg.User)
		if !ok || user == nil || user.ID != userID {
			continue
		}
		name := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
		if name == "" && user.Username != "" {
			name = "@" + user.Username
		}
		if name == "" {
			return fallback
		}
		return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", userID, EscapeHTML(name))
	}
	return fallback
}

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

	resolve := func(ref string, replied bool) (tg.InputPeerClass, int64, error) {
		peer, id, err := c.Resolver.ResolveUser(c.Ctx, ref)
		if err != nil {
			if replied {
				return nil, 0, fmt.Errorf("cannot resolve replied user %s: %w", ref, err)
			}
			return nil, 0, fmt.Errorf("cannot resolve user %q: %w", ref, err)
		}
		if peer == nil || id == 0 {
			if replied {
				return nil, 0, fmt.Errorf("cannot resolve replied user %s: %w", ref, ErrPeerUnresolved)
			}
			return nil, 0, fmt.Errorf("cannot resolve user %q: %w", ref, ErrPeerUnresolved)
		}
		input, ok := peer.(*tg.InputPeerUser)
		if !ok || input.AccessHash == 0 {
			return nil, 0, fmt.Errorf("%w: user %d", ErrAccessHashMissing, id)
		}
		return peer, id, nil
	}

	// Explicit numeric IDs and @usernames always override reply targeting.
	if len(c.Args) > 0 {
		arg := c.Args[0]
		if uid, err := strconv.ParseInt(arg, 10, 64); err == nil {
			if uid <= 0 {
				return nil, 0, errors.New("invalid user ID: must be positive")
			}
			return resolve(arg, false)
		}
		if strings.HasPrefix(arg, "@") {
			return resolve(arg, false)
		}
	}

	// When a command is a reply, non-target arguments are command payload (for
	// example /warn <reason>) and the replied sender is authoritative. GetReply
	// also enforces P7-J linked-chat and forum-topic fences.
	if c.Message != nil && c.Message.ReplyToID > 0 {
		reply, err := c.GetReply()
		if err != nil {
			return nil, 0, fmt.Errorf("cannot inspect replied message: %w", err)
		}
		if reply != nil && reply.SenderID != 0 {
			ref := strconv.FormatInt(reply.SenderID, 10)
			return resolve(ref, true)
		}
	}

	// Preserve bare-username compatibility when there is no reply context.
	if len(c.Args) > 0 {
		arg := c.Args[0]
		if !strings.ContainsAny(arg, " /.:") && len(arg) >= 3 {
			return resolve(arg, false)
		}
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
