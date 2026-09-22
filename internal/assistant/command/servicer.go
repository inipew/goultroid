package command

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

// ErrGroupMutationUnavailable fences Assistant group mutations until the P7
// manager transport has authoritative actor/bot rights revalidation and managed
// Telegram RPC wiring. It prevents the embedded mock fallback from reporting a
// mutation as successful when no Telegram operation occurred.
var (
	ErrGroupMutationUnavailable = fmt.Errorf("%w: assistant group mutation transport is not configured", core.ErrUnavailable)
	ErrGroupMutationNotAdmitted = fmt.Errorf("%w: assistant group mutation requires TaskEngine admission", core.ErrForbidden)
	ErrGroupQueryUnavailable    = fmt.Errorf("%w: assistant group query transport is not configured", core.ErrUnavailable)
)

// GroupQueryReader is the read-only Telegram query boundary exposed to canonical
// Assistant manager commands. It intentionally contains no mutation methods.
type GroupQueryReader interface {
	GetFullChat(context.Context, tg.InputPeerClass) (*tg.MessagesChatFull, error)
}

// GroupMutationContext carries the immutable command coordinates required by
// the P7-G managed mutation service. It deliberately carries no cached role.
type GroupMutationContext struct {
	ActorID int64
	ChatID  int64
	Kind    core.ChatKind
}

// GroupMutationRequest is the typed transport request emitted by the thin
// TelegramServicer adapter. Authorization and Telegram RPC policy live in the
// managed mutation executor, not in this adapter.
type GroupMutationRequest struct {
	Action       core.GroupMutationAction
	Peer         tg.InputPeerClass
	Target       tg.InputPeerClass
	MessageID    int
	Silent       bool
	TopicID      int
	FromID       int
	ToID         int
	UntilDate    int
	Title        string
	DefaultRights tg.ChatBannedRights
}

type GroupMutationResult struct {
	Deleted int
}

type GroupMutationExecutor interface {
	Execute(context.Context, GroupMutationContext, GroupMutationRequest) (GroupMutationResult, error)
}

// assistantServicerAdapter adapts Assistant MessageInteraction into core.TelegramServicer
// so plugin command handlers can transparently use ctx.Reply, ctx.EditOrReply, and ctx.ReplyMarkup.
type assistantServicerAdapter struct {
	core.MockTelegramServicer
	inter             interaction.MessageInteraction
	groupQuery        GroupQueryReader
	groupMutation     GroupMutationExecutor
	mutationContext   GroupMutationContext
	mutationAdmitted  bool
}

func (a *assistantServicerAdapter) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if a.inter != nil {
		return a.inter.SendMessage(ctx, peer, text, nil)
	}
	return a.MockTelegramServicer.SendMessage(ctx, peer, text)
}

func (a *assistantServicerAdapter) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if a.inter != nil {
		return a.inter.SendMessage(ctx, peer, text, markup)
	}
	return a.MockTelegramServicer.SendMessageWithMarkup(ctx, peer, text, markup)
}

func (a *assistantServicerAdapter) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	if a.inter != nil {
		return a.inter.SendMedia(ctx, peer, mediaType, filePath, caption)
	}
	return a.MockTelegramServicer.SendMedia(ctx, peer, mediaType, filePath, caption)
}

func (a *assistantServicerAdapter) SendMessageContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
	markup tg.ReplyMarkupClass,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if contextual, ok := a.inter.(interface {
		SendMessageContext(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass, core.MessageSendContext) (*tg.Message, error)
	}); ok {
		return contextual.SendMessageContext(ctx, peer, text, markup, send)
	}
	if send.TopicID > 0 {
		return nil, fmt.Errorf("%w: assistant interaction cannot preserve forum topic %d", core.ErrUnavailable, send.TopicID)
	}
	if markup != nil {
		return a.SendMessageWithMarkup(ctx, peer, text, markup)
	}
	return a.SendMessage(ctx, peer, text)
}

func (a *assistantServicerAdapter) SendMediaContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	mediaType string,
	filePath string,
	caption string,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if contextual, ok := a.inter.(interface {
		SendMediaContext(context.Context, tg.InputPeerClass, string, string, string, core.MessageSendContext) (*tg.Message, error)
	}); ok {
		return contextual.SendMediaContext(ctx, peer, mediaType, filePath, caption, send)
	}
	if send.TopicID > 0 {
		return nil, fmt.Errorf("%w: assistant interaction cannot preserve forum topic %d", core.ErrUnavailable, send.TopicID)
	}
	return a.SendMedia(ctx, peer, mediaType, filePath, caption)
}

func (a *assistantServicerAdapter) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.Edit(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0), text, nil)
	}
	return a.MockTelegramServicer.EditMessage(ctx, peer, msgID, text)
}

func (a *assistantServicerAdapter) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.Edit(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0), text, markup)
	}
	return a.MockTelegramServicer.EditMessageMarkup(ctx, peer, msgID, text, markup)
}

func (a *assistantServicerAdapter) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.EditMarkup(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0), markup)
	}
	return a.MockTelegramServicer.EditMessageMarkupOnly(ctx, peer, msgID, markup)
}

func (a *assistantServicerAdapter) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		for _, id := range msgIDs {
			_ = a.inter.Delete(ctx, interaction.NewMessageTarget(peer, id, chatID, 0))
		}
		return nil
	}
	return a.MockTelegramServicer.DeleteMessage(ctx, peer, msgIDs)
}

func (a *assistantServicerAdapter) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if a.inter != nil {
		chatID := extractChatIDFromInputPeer(peer)
		return a.inter.GetMessage(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0))
	}
	return a.MockTelegramServicer.GetMessage(ctx, peer, msgID)
}

func (a *assistantServicerAdapter) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	if a.groupQuery == nil {
		return nil, ErrGroupQueryUnavailable
	}
	return a.groupQuery.GetFullChat(ctx, peer)
}

func (a *assistantServicerAdapter) executeGroupMutation(
	ctx context.Context,
	request GroupMutationRequest,
) (GroupMutationResult, error) {
	if a == nil || a.groupMutation == nil {
		return GroupMutationResult{}, ErrGroupMutationUnavailable
	}
	if !a.mutationAdmitted {
		return GroupMutationResult{}, ErrGroupMutationNotAdmitted
	}
	return a.groupMutation.Execute(ctx, a.mutationContext, request)
}

func admitGroupMutationExecution(c *core.Context) {
	if c == nil || c.Svc == nil {
		return
	}
	adapter, ok := c.Svc.(*assistantServicerAdapter)
	if !ok || adapter == nil {
		return
	}
	admitted := *adapter
	admitted.mutationAdmitted = true
	c.Svc = &admitted
}

func (a *assistantServicerAdapter) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationPin, Peer: peer, MessageID: msgID, Silent: silent,
	})
	return err
}

func (a *assistantServicerAdapter) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationUnpin, Peer: peer, MessageID: msgID,
	})
	return err
}

func (a *assistantServicerAdapter) BanUser(ctx context.Context, peer, user tg.InputPeerClass, untilDate int) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: peer, Target: user, UntilDate: untilDate,
	})
	return err
}

func (a *assistantServicerAdapter) UnbanUser(ctx context.Context, peer, user tg.InputPeerClass) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationUnban, Peer: peer, Target: user,
	})
	return err
}

func (a *assistantServicerAdapter) KickUser(ctx context.Context, peer, user tg.InputPeerClass) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationKick, Peer: peer, Target: user,
	})
	return err
}

func (a *assistantServicerAdapter) MuteUser(ctx context.Context, peer, user tg.InputPeerClass, untilDate int) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationMute, Peer: peer, Target: user, UntilDate: untilDate,
	})
	return err
}

func (a *assistantServicerAdapter) UnmuteUser(ctx context.Context, peer, user tg.InputPeerClass) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationUnmute, Peer: peer, Target: user,
	})
	return err
}

func (a *assistantServicerAdapter) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID, fromID, toID int) (int, error) {
	result, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationPurge, Peer: peer, TopicID: topicID, FromID: fromID, ToID: toID,
	})
	return result.Deleted, err
}

func (a *assistantServicerAdapter) PromoteAdmin(ctx context.Context, peer, user tg.InputPeerClass, title string) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationPromote, Peer: peer, Target: user, Title: title,
	})
	return err
}

func (a *assistantServicerAdapter) DemoteAdmin(ctx context.Context, peer, user tg.InputPeerClass) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationDemote, Peer: peer, Target: user,
	})
	return err
}

func (a *assistantServicerAdapter) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	_, err := a.executeGroupMutation(ctx, GroupMutationRequest{
		Action: core.GroupMutationDefaultPermissions, Peer: peer, DefaultRights: rights,
	})
	return err
}
