package userlog

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

const (
	SettingLogChatID  = "log_chat_id"
	SettingTagsEnable = "log_tags_enabled"
	SettingPMsEnable  = "log_pms_enabled"
)

// Service manages event logging to a private log group or channel.
type Service struct {
	db      *database.DB
	svc     core.TelegramServicer
	svcFunc func() core.TelegramServicer
	logger  *zap.Logger
	mu      sync.RWMutex
}

// NewService creates a new UserLog service instance.
func NewService(db *database.DB, svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		db:     db,
		logger: logger,
	}
	switch v := svc.(type) {
	case core.TelegramServicer:
		s.svc = v
	case func() core.TelegramServicer:
		s.svcFunc = v
	}
	return s
}

func (s *Service) getService() core.TelegramServicer {
	if s.svc != nil {
		return s.svc
	}
	if s.svcFunc != nil {
		return s.svcFunc()
	}
	return nil
}

// SetLogChat configures the destination chat/channel ID for user logs.
func (s *Service) SetLogChat(ctx context.Context, chatID int64) error {
	return s.db.SetUserLogSetting(ctx, SettingLogChatID, strconv.FormatInt(chatID, 10))
}

// GetLogChat retrieves the configured destination chat ID, or 0 if unconfigured.
func (s *Service) GetLogChat(ctx context.Context) (int64, error) {
	val, err := s.db.GetUserLogSetting(ctx, SettingLogChatID)
	if err != nil {
		return 0, err
	}
	if val == "" {
		return 0, nil
	}
	return strconv.ParseInt(val, 10, 64)
}

// SetFeatureEnabled toggles a specific log category (e.g. SettingTagsEnable, SettingPMsEnable).
func (s *Service) SetFeatureEnabled(ctx context.Context, feature string, enabled bool) error {
	val := "false"
	if enabled {
		val = "true"
	}
	return s.db.SetUserLogSetting(ctx, feature, val)
}

// IsFeatureEnabled returns whether a log category is active (defaults to true if log chat configured).
func (s *Service) IsFeatureEnabled(ctx context.Context, feature string) (bool, error) {
	val, err := s.db.GetUserLogSetting(ctx, feature)
	if err != nil {
		return false, err
	}
	if val == "" {
		return true, nil // Default enabled
	}
	return val == "true", nil
}

func (s *Service) sendToLogChat(ctx context.Context, text string) error {
	chatID, err := s.GetLogChat(ctx)
	if err != nil || chatID == 0 {
		return nil // Logging not enabled or unconfigured
	}

	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}

	// Resolve peer type from chat ID
	var peer tg.InputPeerClass
	if chatID < 0 {
		// Channels / Supergroups usually have negative IDs (-100...)
		peer = &tg.InputPeerChannel{ChannelID: -chatID}
	} else {
		peer = &tg.InputPeerChat{ChatID: chatID}
	}

	_, err = svc.SendMessage(ctx, peer, text)
	return err
}

// LogMention formats and dispatches a mention alert to the log chat.
func (s *Service) LogMention(ctx context.Context, chatTitle string, senderName string, senderID int64, messageText string) error {
	enabled, _ := s.IsFeatureEnabled(ctx, SettingTagsEnable)
	if !enabled {
		return nil
	}

	snippet := messageText
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}

	text := fmt.Sprintf(
		"🔔 <b>Tag / Mention Alert</b>\n\n"+
			"• <b>Chat:</b> <code>%s</code>\n"+
			"• <b>From:</b> <code>%s</code> (<code>%d</code>)\n"+
			"• <b>Message:</b>\n<i>%s</i>",
		core.EscapeHTML(chatTitle),
		core.EscapeHTML(senderName),
		senderID,
		core.EscapeHTML(snippet),
	)

	return s.sendToLogChat(ctx, text)
}

// LogPM formats and dispatches a new PM notification to the log chat.
func (s *Service) LogPM(ctx context.Context, senderName string, senderID int64, messageText string) error {
	enabled, _ := s.IsFeatureEnabled(ctx, SettingPMsEnable)
	if !enabled {
		return nil
	}

	snippet := messageText
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}

	text := fmt.Sprintf(
		"📩 <b>New Private Message</b>\n\n"+
			"• <b>Sender:</b> <code>%s</code> (<code>%d</code>)\n"+
			"• <b>Message:</b>\n<i>%s</i>",
		core.EscapeHTML(senderName),
		senderID,
		core.EscapeHTML(snippet),
	)

	return s.sendToLogChat(ctx, text)
}

// LogAction logs administrative punishments (mute/ban/warn).
func (s *Service) LogAction(ctx context.Context, action string, targetID int64, reason string) error {
	text := fmt.Sprintf(
		"⚖️ <b>Admin Action Executed</b>\n\n"+
			"• <b>Action:</b> <code>%s</code>\n"+
			"• <b>Target:</b> <code>%d</code>\n"+
			"• <b>Reason:</b> <i>%s</i>",
		core.EscapeHTML(action),
		targetID,
		core.EscapeHTML(reason),
	)

	return s.sendToLogChat(ctx, text)
}
