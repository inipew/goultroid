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
	DefaultWarnThreshold = 3
	ActionNone           = "none"
	ActionMute           = "muted"
	ActionKick           = "kicked"
	ActionBan            = "banned"
)

type WarnResult struct {
	CurrentCount int
	Threshold    int
	ActionTaken  string
}

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

func NewService(db *database.DB, svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{db: db, logger: logger, defaultThreshold: DefaultWarnThreshold}
	switch v := svc.(type) {
	case core.TelegramServicer:
		s.svc = v
	case func() core.TelegramServicer:
		s.svcFunc = v
	}
	return s
}

func (s *Service) SetTelegramService(svc core.TelegramServicer) { s.svc = svc }

// Warn records a warning and enforces the configured threshold action. The
// warning state is reset only after a successful punitive action; a failed
// enforcement therefore remains visible and can be retried rather than being
// silently erased.
func (s *Service) Warn(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, chatID, userID int64, reason string, warnedBy int64, threshold int, actionOnThreshold string) (*WarnResult, error) {
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
	res := &WarnResult{CurrentCount: count, Threshold: threshold, ActionTaken: ActionNone}
	if count < threshold {
		return res, nil
	}

	var actionErr error
	switch actionLower := strings.ToLower(actionOnThreshold); actionLower {
	case ActionKick:
		actionErr = s.Kick(ctx, peer, user)
		if actionErr == nil {
			res.ActionTaken = ActionKick
		}
	case ActionBan:
		actionErr = s.Ban(ctx, peer, user, 0)
		if actionErr == nil {
			res.ActionTaken = ActionBan
		}
	default:
		actionErr = s.Mute(ctx, peer, user, 24*time.Hour)
		if actionErr == nil {
			res.ActionTaken = ActionMute
		}
	}

	if actionErr != nil {
		s.logger.Warn("automated moderation enforcement failed; warnings retained", zap.Error(actionErr), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID), zap.String("action", actionOnThreshold), zap.Int("count", count))
		return res, fmt.Errorf("threshold enforcement failed: %w", actionErr)
	}
	if err := s.db.ResetWarnings(ctx, chatID, userID); err != nil {
		return res, fmt.Errorf("punitive action succeeded but failed to reset warnings: %w", err)
	}
	return res, nil
}

func (s *Service) GetWarnings(ctx context.Context, chatID, userID int64) ([]*database.WarningRecord, error) {
	return s.db.GetWarnings(ctx, chatID, userID)
}

func (s *Service) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	return s.db.GetWarningCount(ctx, chatID, userID)
}

func (s *Service) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	return s.db.ResetWarnings(ctx, chatID, userID)
}

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

func (s *Service) Unmute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.UnmuteUser(ctx, peer, user)
}

func (s *Service) Ban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.BanUser(ctx, peer, user, untilDate)
}

func (s *Service) Unban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.UnbanUser(ctx, peer, user)
}

func (s *Service) Kick(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	svc := s.getService()
	if svc == nil {
		return fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	return svc.KickUser(ctx, peer, user)
}
