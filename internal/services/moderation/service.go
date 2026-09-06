package moderation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

const (
	// DefaultWarnThreshold is the default number of warnings before an automated action triggers.
	DefaultWarnThreshold = 3

	// ActionNone indicates no punitive action was taken on warning.
	ActionNone = "none"
	// ActionMute indicates the user was muted upon reaching threshold.
	ActionMute = "muted"
	// ActionKick indicates the user was kicked upon reaching threshold.
	ActionKick = "kicked"
	// ActionBan indicates the user was banned upon reaching threshold.
	ActionBan = "banned"
)

// WarnResult summarizes the outcome of issuing a warning.
type WarnResult struct {
	CurrentCount int
	Threshold    int
	ActionTaken  string
}

// Moderator provides high-level group moderation, warning accumulation, and automated enforcement.
type Moderator interface {
	Warn(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, chatID, userID int64, reason string, warnedBy int64, threshold int, actionOnThreshold string) (*WarnResult, error)
	GetWarnings(ctx context.Context, chatID, userID int64) ([]*database.WarningRecord, error)
	GetWarningCount(ctx context.Context, chatID, userID int64) (int, error)
	ResetWarnings(ctx context.Context, chatID, userID int64) error

	Mute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, duration time.Duration) error
	Unmute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	Ban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error
	Unban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	Kick(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
}

// Service coordinates moderation business logic, persistence, and Telegram enforcement.
type Service struct {
	db               *database.DB
	svc              core.TelegramServicer
	svcFunc          func() core.TelegramServicer
	logger           *zap.Logger
	defaultThreshold int
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

// NewService creates a new moderation Service accepting either core.TelegramServicer or func() core.TelegramServicer.
func NewService(db *database.DB, svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		db:               db,
		logger:           logger,
		defaultThreshold: DefaultWarnThreshold,
	}
	switch v := svc.(type) {
	case core.TelegramServicer:
		s.svc = v
	case func() core.TelegramServicer:
		s.svcFunc = v
	}
	return s
}

// SetTelegramService updates the TelegramServicer instance.
func (s *Service) SetTelegramService(svc core.TelegramServicer) {
	s.svc = svc
}

// Warn records a warning against a user. If count reaches threshold, automated punitive action is executed.
func (s *Service) Warn(
	ctx context.Context,
	peer tg.InputPeerClass,
	user tg.InputPeerClass,
	chatID, userID int64,
	reason string,
	warnedBy int64,
	threshold int,
	actionOnThreshold string,
) (*WarnResult, error) {
	if threshold <= 0 {
		threshold = s.defaultThreshold
	}
	if actionOnThreshold == "" {
		actionOnThreshold = ActionMute
	}

	if err := s.db.AddWarning(ctx, chatID, userID, reason, warnedBy); err != nil {
		return nil, fmt.Errorf("failed to add warning record: %w", err)
	}

	count, err := s.db.GetWarningCount(ctx, chatID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to check warning count: %w", err)
	}

	res := &WarnResult{
		CurrentCount: count,
		Threshold:    threshold,
		ActionTaken:  ActionNone,
	}

	if count >= threshold {
		// Execute automated action
		actionLower := strings.ToLower(actionOnThreshold)
		switch actionLower {
		case ActionKick:
			if err := s.Kick(ctx, peer, user); err == nil {
				res.ActionTaken = ActionKick
			} else {
				s.logger.Warn("auto-kick on warn threshold failed", zap.Error(err), zap.Int64("user_id", userID))
			}
		case ActionBan:
			if err := s.Ban(ctx, peer, user, 0); err == nil {
				res.ActionTaken = ActionBan
			} else {
				s.logger.Warn("auto-ban on warn threshold failed", zap.Error(err), zap.Int64("user_id", userID))
			}
		default: // default is mute (e.g. 24 hours)
			if err := s.Mute(ctx, peer, user, 24*time.Hour); err == nil {
				res.ActionTaken = ActionMute
			} else {
				s.logger.Warn("auto-mute on warn threshold failed", zap.Error(err), zap.Int64("user_id", userID))
			}
		}

		// Reset warnings after threshold reached and action executed
		_ = s.db.ResetWarnings(ctx, chatID, userID)
	}

	return res, nil
}

// GetWarnings returns the infraction history for a user in a chat.
func (s *Service) GetWarnings(ctx context.Context, chatID, userID int64) ([]*database.WarningRecord, error) {
	return s.db.GetWarnings(ctx, chatID, userID)
}

// GetWarningCount returns the total warning count for a user in a chat.
func (s *Service) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	return s.db.GetWarningCount(ctx, chatID, userID)
}

// ResetWarnings clears all warnings for a user in a chat.
func (s *Service) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	return s.db.ResetWarnings(ctx, chatID, userID)
}

// Mute mutes a user in a group for the specified duration.
func (s *Service) Mute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, duration time.Duration) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	untilDate := 0
	if duration > 0 {
		untilDate = int(time.Now().Add(duration).Unix())
	}
	return svc.MuteUser(ctx, peer, user, untilDate)
}

// Unmute unmutes a user in a group.
func (s *Service) Unmute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.UnmuteUser(ctx, peer, user)
}

// Ban bans a user from a group.
func (s *Service) Ban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.BanUser(ctx, peer, user, untilDate)
}

// Unban unbans a user in a group.
func (s *Service) Unban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.UnbanUser(ctx, peer, user)
}

// Kick kicks a user from a group.
func (s *Service) Kick(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.KickUser(ctx, peer, user)
}
