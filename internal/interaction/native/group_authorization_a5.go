package native

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

// GroupActionScope is captured when a canonical a2 session is opened in a
// verified group. It is not an authorization grant: every mutation must fetch
// the actor's authoritative Telegram role again immediately before writing.
// TopicID is retained for caller-specific rule routing; it never changes the
// chat identity checked below.
type GroupActionScope struct {
	ChatID  int64
	Kind    core.ChatKind
	TopicID int
}

// AuthorizeFreshGroupAction checks the already-bound a2 actor/chat/message,
// derives the Telegram group peer only from the callback target, and requires
// a fresh authoritative role. Global owner/sudo does not bypass group rights.
// A caller must invoke this in the TaskEngine handler immediately before a
// group-scoped write, not only when opening or rendering the menu.
func AuthorizeFreshGroupAction(
	ctx context.Context,
	session interaction.Session,
	target presentation.Target,
	scope GroupActionScope,
	roles core.GroupRoleResolver,
	requirement core.GroupAuthorizationRequirement,
) error {
	if ctx == nil {
		return fmt.Errorf("%w: missing callback context", core.ErrUnavailable)
	}
	if err := requirement.Validate(); err != nil {
		return fmt.Errorf("%w: invalid group authorization policy: %v", core.ErrUnavailable, err)
	}
	if requirement.Level < core.GroupAuthorizationAdministrator {
		return fmt.Errorf("%w: mutation requires administrator authorization", core.ErrGroupAuthorizationDenied)
	}
	if scope.ChatID <= 0 || scope.TopicID < 0 ||
		(scope.Kind != core.ChatKindGroup && scope.Kind != core.ChatKindSupergroup) {
		return core.ErrGroupOnly
	}
	binding := session.Binding
	if binding.ActorID <= 0 || binding.ChatID != scope.ChatID || binding.MessageID <= 0 || binding.InlineMessageID != "" {
		return fmt.Errorf("%w: group callback has no matching actor/chat/message binding", core.ErrUnauthorized)
	}
	messageTarget, ok := target.(presentationtelegram.MessageTarget)
	if !ok || messageTarget.ChatID != scope.ChatID || messageTarget.MessageID != binding.MessageID {
		return fmt.Errorf("%w: group callback target differs from the session", core.ErrUnauthorized)
	}
	switch peer := messageTarget.Peer.(type) {
	case *tg.InputPeerChat:
		if scope.Kind != core.ChatKindGroup || peer == nil || peer.ChatID != scope.ChatID {
			return core.ErrGroupOnly
		}
	case *tg.InputPeerChannel:
		if scope.Kind != core.ChatKindSupergroup || peer == nil || peer.ChannelID != scope.ChatID || peer.AccessHash == 0 {
			return core.ErrGroupOnly
		}
	default:
		return core.ErrGroupOnly
	}
	if roles == nil {
		return fmt.Errorf("%w: contextual group role resolver is unavailable", core.ErrUnavailable)
	}
	snapshot, err := roles.ResolveGroupRoleFresh(ctx, core.GroupRoleRequest{
		ChatID: scope.ChatID,
		Kind:   scope.Kind,
		Peer:   messageTarget.Peer,
		UserID: binding.ActorID,
	})
	if err != nil {
		return err
	}
	if !snapshot.Principal.Verified || snapshot.Principal.UserID != binding.ActorID {
		return fmt.Errorf("%w: fresh group principal is unverified or mismatched", core.ErrUnavailable)
	}
	return requirement.Authorize(snapshot.Principal)
}
