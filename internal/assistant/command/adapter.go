package command

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

// assistantServicerAdapter adapts Assistant MessageInteraction into core.TelegramServicer
// so plugin command handlers can transparently use ctx.Reply, ctx.EditOrReply, and ctx.ReplyMarkup.
type assistantServicerAdapter struct {
	core.MockTelegramServicer
	inter interaction.MessageInteraction
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

// UnifiedCommandAdapter bridges assistant Telegram commands to the Unified Command Registry (§10, §29 bug16_1).
type UnifiedCommandAdapter struct {
	registry   CommandSource
	logger     *zap.Logger
	ownerID    int64
	sudoGetter func() []int64
}

// NewUnifiedCommandAdapter creates an adapter bridging Assistant commands to the unified registry.
func NewUnifiedCommandAdapter(reg CommandSource, logger *zap.Logger) *UnifiedCommandAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UnifiedCommandAdapter{
		registry: reg,
		logger:   logger,
	}
}

// SetOwner configures the owner identity and sudo lookup for permission enforcement.
func (a *UnifiedCommandAdapter) SetOwner(ownerID int64, sudoGetter func() []int64) {
	a.ownerID = ownerID
	a.sudoGetter = sudoGetter
}

// Execute attempts to find and run a command from the unified registry on the Assistant surface.
func (a *UnifiedCommandAdapter) Execute(ctx context.Context, cmdName string, args []string, senderID int64, peer tg.InputPeerClass, inter interaction.MessageInteraction) (bool, error) {
	if a == nil || a.registry == nil {
		return false, nil
	}

	// 1. Capability check (§10 bug16_1)
	cmd, ok := a.registry.FindForSurface(cmdName, execution.SourceAssistant)
	if !ok || cmd.Handler == nil {
		return false, nil
	}

	// 2. Permission check (§9, §10 bug16_1)
	isOwner := a.ownerID != 0 && senderID == a.ownerID
	isSudo := isOwner
	var sudoList []int64
	if a.sudoGetter != nil {
		sudoList = a.sudoGetter()
		for _, s := range sudoList {
			if s == senderID {
				isSudo = true
				break
			}
		}
	}

	switch cmd.Permission {
	case core.PermissionOwner:
		if !isOwner {
			a.logger.Warn("assistant: permission denied for command",
				zap.String("command", cmdName),
				zap.Int64("sender_id", senderID),
			)
			if inter != nil {
				_, _ = inter.SendMessage(ctx, peer, "⛔ <i>This command is restricted to the bot owner.</i>", nil)
			}
			return true, nil
		}
	case core.PermissionSudo:
		if !isSudo {
			a.logger.Warn("assistant: sudo permission required for command",
				zap.String("command", cmdName),
				zap.Int64("sender_id", senderID),
			)
			if inter != nil {
				_, _ = inter.SendMessage(ctx, peer, "⛔ <i>This command requires sudo privileges.</i>", nil)
			}
			return true, nil
		}
	}

	a.logger.Debug("assistant: executing unified command via adapter",
		zap.String("command", cmdName),
		zap.Int64("sender_id", senderID),
	)

	chatID := extractChatIDFromInputPeer(peer)
	if chatID == 0 {
		chatID = senderID
	}

	perms := core.NewPermissions(a.ownerID, sudoList)
	principal := &core.Principal{
		UserID:  senderID,
		IsOwner: isOwner,
		IsSudo:  isSudo,
	}

	// 3. Construct fully populated core.Context adapter allowing plugin handlers to execute uniformly
	coreCtx := &core.Context{
		Ctx:       ctx,
		Source:    core.ExecutionAssistant,
		Command:   cmdName,
		Args:      args,
		RawArgs:   strings.Join(args, " "),
		PeerID:    peer,
		Message:   &core.Message{SenderID: senderID, Text: "/" + cmdName + " " + strings.Join(args, " ")},
		Sender:    &core.User{ID: senderID},
		Chat:      &core.Chat{ID: chatID},
		Perms:     perms,
		Principal: principal,
		Svc:       &assistantServicerAdapter{inter: inter},
	}

	return true, cmd.Handler(coreCtx)
}
