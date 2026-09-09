package userlog

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

const (
	SettingLogDestination = "log_destination"
	SettingLogChatID      = "log_chat_id" // Legacy numeric key kept for backward compat
	SettingTagsEnable     = "log_tags_enabled"
	SettingPMsEnable      = "log_pms_enabled"
	SettingActionsEnable  = "log_actions_enabled"
)

// LogDestinationType defines whether the destination is a basic chat or channel/supergroup.
type LogDestinationType string

const (
	LogDestinationChat    LogDestinationType = "chat"
	LogDestinationChannel LogDestinationType = "channel"
)

// LogDestination models a normalized Telegram destination peer with preserved access hash.
type LogDestination struct {
	Type       LogDestinationType `json:"type"`
	ID         int64              `json:"id"`
	AccessHash int64              `json:"access_hash,omitempty"`
	Title      string             `json:"title,omitempty"`
}

// InputPeer converts the LogDestination into a Telegram MTProto InputPeerClass.
func (d LogDestination) InputPeer() tg.InputPeerClass {
	if d.Type == LogDestinationChannel {
		return &tg.InputPeerChannel{
			ChannelID:  d.ID,
			AccessHash: d.AccessHash,
		}
	}
	return &tg.InputPeerChat{
		ChatID: d.ID,
	}
}

// DeliveryStatus tracks runtime health of the logging pipeline.
type DeliveryStatus string

const (
	StatusUnconfigured DeliveryStatus = "UNCONFIGURED"
	StatusHealthy      DeliveryStatus = "HEALTHY"
	StatusDegraded     DeliveryStatus = "DEGRADED"
	StatusFailed       DeliveryStatus = "FAILED"
)

// ServiceStats provides observability into UserLog delivery health.
type ServiceStats struct {
	Status              DeliveryStatus `json:"status"`
	DeliveredCount      int64          `json:"delivered_count"`
	FailedCount         int64          `json:"failed_count"`
	ConsecutiveFailures int64          `json:"consecutive_failures"`
	LastSuccessAt       time.Time      `json:"last_success_at,omitempty"`
	LastErrorAt         time.Time      `json:"last_error_at,omitempty"`
	LastError           string         `json:"last_error,omitempty"`
}

// Service manages event logging to a private log group or channel.
type Service struct {
	repo    Repository
	svc     core.TelegramServicer
	svcFunc func() core.TelegramServicer
	logger  *zap.Logger

	mu         sync.RWMutex
	cachedDest *LogDestination

	deliveredCount      atomic.Int64
	failedCount         atomic.Int64
	consecutiveFailures atomic.Int64
	lastSuccessAt       atomic.Pointer[time.Time]
	lastErrorAt         atomic.Pointer[time.Time]
	lastErrorMu         sync.RWMutex
	lastErrorMsg        string
}

// NewService creates a new UserLog service instance.
func NewService(repo Repository, svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		repo:   repo,
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

func (s *Service) recordSuccess() {
	s.deliveredCount.Add(1)
	s.consecutiveFailures.Store(0)
	now := time.Now()
	s.lastSuccessAt.Store(&now)
}

func (s *Service) recordFailure(err error) {
	if err == nil {
		return
	}
	s.failedCount.Add(1)
	s.consecutiveFailures.Add(1)
	now := time.Now()
	s.lastErrorAt.Store(&now)
	s.lastErrorMu.Lock()
	s.lastErrorMsg = err.Error()
	s.lastErrorMu.Unlock()
}

// Stats returns a snapshot of UserLog delivery metrics and health state.
func (s *Service) Stats(ctx context.Context) ServiceStats {
	dest, _ := s.GetDestination(ctx)
	status := StatusHealthy
	if dest == nil || dest.ID == 0 {
		status = StatusUnconfigured
	} else {
		consec := s.consecutiveFailures.Load()
		if consec >= 5 {
			status = StatusFailed
		} else if consec > 0 {
			status = StatusDegraded
		}
	}

	stats := ServiceStats{
		Status:              status,
		DeliveredCount:      s.deliveredCount.Load(),
		FailedCount:         s.failedCount.Load(),
		ConsecutiveFailures: s.consecutiveFailures.Load(),
	}
	if t := s.lastSuccessAt.Load(); t != nil {
		stats.LastSuccessAt = *t
	}
	if t := s.lastErrorAt.Load(); t != nil {
		stats.LastErrorAt = *t
	}
	s.lastErrorMu.RLock()
	stats.LastError = s.lastErrorMsg
	s.lastErrorMu.RUnlock()
	return stats
}

// SetDestination stores a structured LogDestination.
func (s *Service) SetDestination(ctx context.Context, dest LogDestination) error {
	data, err := json.Marshal(dest)
	if err != nil {
		return fmt.Errorf("marshal log destination: %w", err)
	}
	if err := s.repo.SetUserLogSetting(ctx, SettingLogDestination, string(data)); err != nil {
		return err
	}

	// Update legacy key for backward compatibility
	legacyID := dest.ID
	if dest.Type == LogDestinationChannel {
		legacyID = -dest.ID
	}
	_ = s.repo.SetUserLogSetting(ctx, SettingLogChatID, strconv.FormatInt(legacyID, 10))

	s.mu.Lock()
	s.cachedDest = &dest
	s.mu.Unlock()
	return nil
}

// ClearDestination removes configured destination.
func (s *Service) ClearDestination(ctx context.Context) error {
	_ = s.repo.SetUserLogSetting(ctx, SettingLogDestination, "")
	_ = s.repo.SetUserLogSetting(ctx, SettingLogChatID, "")
	s.mu.Lock()
	s.cachedDest = nil
	s.mu.Unlock()
	return nil
}

// GetDestination retrieves the configured LogDestination with access hash preservation.
func (s *Service) GetDestination(ctx context.Context) (*LogDestination, error) {
	s.mu.RLock()
	if s.cachedDest != nil {
		dest := *s.cachedDest
		s.mu.RUnlock()
		return &dest, nil
	}
	s.mu.RUnlock()

	val, err := s.repo.GetUserLogSetting(ctx, SettingLogDestination)
	if err != nil {
		return nil, fmt.Errorf("get user log destination: %w", err)
	}
	if val != "" {
		var dest LogDestination
		if err := json.Unmarshal([]byte(val), &dest); err == nil && dest.ID != 0 {
			s.mu.Lock()
			s.cachedDest = &dest
			s.mu.Unlock()
			return &dest, nil
		}
	}

	// Fallback to legacy SettingLogChatID
	chatVal, err := s.repo.GetUserLogSetting(ctx, SettingLogChatID)
	if err != nil {
		return nil, fmt.Errorf("get legacy log chat: %w", err)
	}
	if chatVal == "" {
		return nil, nil
	}
	legacyID, err := strconv.ParseInt(chatVal, 10, 64)
	if err != nil || legacyID == 0 {
		return nil, nil
	}
	dest := LogDestination{
		ID: legacyID,
	}
	if legacyID < 0 {
		dest.Type = LogDestinationChannel
		str := strconv.FormatInt(legacyID, 10)
		if strings.HasPrefix(str, "-100") {
			if parsed, err := strconv.ParseInt(str[4:], 10, 64); err == nil {
				dest.ID = parsed
			} else {
				dest.ID = -legacyID
			}
		} else {
			dest.ID = -legacyID
		}
	} else {
		dest.Type = LogDestinationChat
	}

	s.mu.Lock()
	s.cachedDest = &dest
	s.mu.Unlock()
	return &dest, nil
}

// SetLogChat configures the destination chat/channel ID (maintained for backward compatibility).
func (s *Service) SetLogChat(ctx context.Context, chatID int64) error {
	dest := LogDestination{
		ID: chatID,
	}
	if chatID < 0 {
		dest.Type = LogDestinationChannel
		str := strconv.FormatInt(chatID, 10)
		if strings.HasPrefix(str, "-100") {
			if parsed, err := strconv.ParseInt(str[4:], 10, 64); err == nil {
				dest.ID = parsed
			} else {
				dest.ID = -chatID
			}
		} else {
			dest.ID = -chatID
		}
	} else {
		dest.Type = LogDestinationChat
	}
	return s.SetDestination(ctx, dest)
}

// GetLogChat retrieves destination chat ID (maintained for backward compatibility).
func (s *Service) GetLogChat(ctx context.Context) (int64, error) {
	dest, err := s.GetDestination(ctx)
	if err != nil || dest == nil {
		return 0, err
	}
	if dest.Type == LogDestinationChannel {
		return -dest.ID, nil
	}
	return dest.ID, nil
}

// IsLogDestination checks if the provided peer matches the configured log destination chat/channel.
func (s *Service) IsLogDestination(peer tg.PeerClass) bool {
	if peer == nil {
		return false
	}
	s.mu.RLock()
	dest := s.cachedDest
	s.mu.RUnlock()
	if dest == nil || dest.ID == 0 {
		return false
	}
	switch p := peer.(type) {
	case *tg.PeerChannel:
		return dest.Type == LogDestinationChannel && p.ChannelID == dest.ID
	case *tg.PeerChat:
		return dest.Type == LogDestinationChat && p.ChatID == dest.ID
	default:
		return false
	}
}

// SetFeatureEnabled toggles a specific log category (e.g. SettingTagsEnable, SettingPMsEnable, SettingActionsEnable).
func (s *Service) SetFeatureEnabled(ctx context.Context, feature string, enabled bool) error {
	val := "false"
	if enabled {
		val = "true"
	}
	return s.repo.SetUserLogSetting(ctx, feature, val)
}

// IsFeatureEnabled returns whether a log category is active (defaults to true if log chat configured).
func (s *Service) IsFeatureEnabled(ctx context.Context, feature string) (bool, error) {
	val, err := s.repo.GetUserLogSetting(ctx, feature)
	if err != nil {
		return false, err
	}
	if val == "" {
		return true, nil // Default enabled
	}
	return val == "true", nil
}

func (s *Service) sendToLogChat(ctx context.Context, text string) error {
	dest, err := s.GetDestination(ctx)
	if err != nil {
		s.logger.Error("failed to retrieve userlog destination", zap.Error(err))
		s.recordFailure(err)
		return err
	}
	if dest == nil || dest.ID == 0 {
		return nil // Logging unconfigured
	}

	svc := s.getService()
	if svc == nil {
		err := fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
		s.recordFailure(err)
		return err
	}

	peer := dest.InputPeer()

	// Transient error retry logic with backoff
	var sendErr error
	maxRetries := 2
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*150) * time.Millisecond):
			}
		}

		_, sendErr = svc.SendMessage(ctx, peer, text)
		if sendErr == nil {
			s.recordSuccess()
			return nil
		}

		errStr := sendErr.Error()
		if strings.Contains(errStr, "CHAT_WRITE_FORBIDDEN") ||
			strings.Contains(errStr, "CHANNEL_PRIVATE") ||
			strings.Contains(errStr, "PEER_ID_INVALID") ||
			strings.Contains(errStr, "USER_BANNED_IN_CHANNEL") {
			s.logger.Warn("userlog permanent delivery rejection",
				zap.String("destination_type", string(dest.Type)),
				zap.Int64("destination_id", dest.ID),
				zap.Error(sendErr),
			)
			s.recordFailure(sendErr)
			return sendErr
		}
	}

	s.logger.Error("userlog delivery failed after retries",
		zap.String("destination_type", string(dest.Type)),
		zap.Int64("destination_id", dest.ID),
		zap.Error(sendErr),
	)
	s.recordFailure(sendErr)
	return sendErr
}

// SendTestMessage sends a test notification to verify delivery and write permissions.
func (s *Service) SendTestMessage(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	text := "🧪 <b>UserLog Test Verification</b>\n\n• <b>Status:</b> Working\n• <b>System:</b> GoUltroid Interaction Engine\n• <b>Timestamp:</b> <code>" + time.Now().UTC().Format(time.RFC3339) + "</code>"
	err := s.sendToLogChat(ctx, text)
	return time.Since(start), err
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

// LogMention formats and dispatches a mention alert to the log chat with clickable user links.
func (s *Service) LogMention(ctx context.Context, chatTitle string, senderName string, senderID int64, messageText string) error {
	enabled, err := s.IsFeatureEnabled(ctx, SettingTagsEnable)
	if err != nil {
		s.logger.Error("failed to check tags setting", zap.Error(err))
		return err
	}
	if !enabled {
		return nil
	}

	snippet := truncateRunes(messageText, 250)

	var fromStr string
	if senderID > 0 {
		fromStr = fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a> (<code>%d</code>)",
			senderID, core.EscapeHTML(senderName), senderID)
	} else {
		fromStr = fmt.Sprintf("<code>%s</code>", core.EscapeHTML(senderName))
	}

	text := fmt.Sprintf(
		"🔔 <b>Tag / Mention Alert</b>\n\n"+
			"• <b>Chat:</b> <code>%s</code>\n"+
			"• <b>From:</b> %s\n"+
			"• <b>Message:</b>\n<i>%s</i>",
		core.EscapeHTML(chatTitle),
		fromStr,
		core.EscapeHTML(snippet),
	)

	return s.sendToLogChat(ctx, text)
}

// LogPM formats and dispatches a new PM notification to the log chat.
func (s *Service) LogPM(ctx context.Context, senderName string, senderID int64, messageText string) error {
	enabled, err := s.IsFeatureEnabled(ctx, SettingPMsEnable)
	if err != nil {
		s.logger.Error("failed to check pms setting", zap.Error(err))
		return err
	}
	if !enabled {
		return nil
	}

	snippet := truncateRunes(messageText, 250)

	var senderStr string
	if senderID > 0 {
		senderStr = fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a> (<code>%d</code>)",
			senderID, core.EscapeHTML(senderName), senderID)
	} else {
		senderStr = fmt.Sprintf("<code>%s</code>", core.EscapeHTML(senderName))
	}

	text := fmt.Sprintf(
		"📩 <b>New Private Message</b>\n\n"+
			"• <b>Sender:</b> %s\n"+
			"• <b>Message:</b>\n<i>%s</i>",
		senderStr,
		core.EscapeHTML(snippet),
	)

	return s.sendToLogChat(ctx, text)
}

// LogAction logs administrative punishments (mute/ban/warn) with success/failure audit details.
func (s *Service) LogAction(ctx context.Context, action string, targetID int64, reason string) error {
	return s.LogActionDetailed(ctx, action, targetID, "", 0, "", reason, true, "")
}

// LogActionDetailed logs administrative punishments with actor, chat, and outcome metadata.
func (s *Service) LogActionDetailed(ctx context.Context, action string, targetID int64, targetName string, actorID int64, chatTitle string, reason string, success bool, errDetail string) error {
	enabled, err := s.IsFeatureEnabled(ctx, SettingActionsEnable)
	if err != nil {
		s.logger.Error("failed to check actions setting", zap.Error(err))
		return err
	}
	if !enabled {
		return nil
	}

	statusStr := "✅ Success"
	if !success {
		statusStr = "❌ Failed"
		if errDetail != "" {
			statusStr += fmt.Sprintf(" (<i>%s</i>)", core.EscapeHTML(errDetail))
		}
	}

	if targetName == "" {
		targetName = strconv.FormatInt(targetID, 10)
	}
	targetStr := fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a> (<code>%d</code>)",
		targetID, core.EscapeHTML(targetName), targetID)

	var actorStr string
	if actorID > 0 {
		actorStr = fmt.Sprintf("<a href=\"tg://user?id=%d\">%d</a>", actorID, actorID)
	} else {
		actorStr = "<code>Self / Admin</code>"
	}

	if chatTitle == "" {
		chatTitle = "Current Chat"
	}

	if reason == "" {
		reason = "None specified"
	}

	text := fmt.Sprintf(
		"⚖️ <b>Admin Action Audit</b>\n\n"+
			"• <b>Action:</b> <code>%s</code>\n"+
			"• <b>Actor:</b> %s\n"+
			"• <b>Target:</b> %s\n"+
			"• <b>Chat:</b> <code>%s</code>\n"+
			"• <b>Status:</b> %s\n"+
			"• <b>Reason:</b> <i>%s</i>",
		strings.ToUpper(core.EscapeHTML(action)),
		actorStr,
		targetStr,
		core.EscapeHTML(chatTitle),
		statusStr,
		core.EscapeHTML(reason),
	)

	return s.sendToLogChat(ctx, text)
}
