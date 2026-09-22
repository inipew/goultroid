package moderation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

const (
	DefaultWarnThreshold  = 3
	MaxWarningThreshold   = 16
	MaxWarningReasonBytes = 1024
	MaxWarningRows        = 50_000
	warningLockStripes    = 64

	ActionNone           = "none"
	ActionMute           = "muted"
	ActionKick           = "kicked"
	ActionBan            = "banned"
)

type WarningRecord struct {
	ID        int
	ChatID    int64
	UserID    int64
	Reason    string
	WarnedBy  int64
	CreatedAt time.Time
}

type WarningRepository interface {
	AddWarning(context.Context, int64, int64, string, int64) error
	GetWarnings(context.Context, int64, int64) ([]*WarningRecord, error)
	GetWarningCount(context.Context, int64, int64) (int, error)
	ResetWarnings(context.Context, int64, int64) error
}

type WarnResult struct {
	CurrentCount int
	Threshold    int
	ActionTaken  string
}

type Moderator interface {
	Warn(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, chatID, userID int64, reason string, warnedBy int64, threshold int, actionOnThreshold string) (*WarnResult, error)
	GetWarnings(ctx context.Context, chatID, userID int64) ([]*WarningRecord, error)
	GetWarningCount(ctx context.Context, chatID, userID int64) (int, error)
	ResetWarnings(ctx context.Context, chatID, userID int64) error
	Mute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, duration time.Duration) error
	Unmute(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	Ban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error
	Unban(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	Kick(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
}

type Service struct {
	repo             WarningRepository
	svc              core.TelegramServicer
	svcFunc          func() core.TelegramServicer
	logger           *zap.Logger
	defaultThreshold int
	warningLocks     [warningLockStripes]sync.Mutex
}

func (s *Service) warningLock(chatID, userID int64) *sync.Mutex {
	mixed := uint64(chatID)*0x9e3779b97f4a7c15 ^ uint64(userID)*0xbf58476d1ce4e5b9
	return &s.warningLocks[mixed%warningLockStripes]
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

func NewService(repo WarningRepository, svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{repo: repo, logger: logger, defaultThreshold: DefaultWarnThreshold}
	switch v := svc.(type) {
	case core.TelegramServicer:
		s.svc = v
	case func() core.TelegramServicer:
		s.svcFunc = v
	}
	return s
}

func (s *Service) SetTelegramService(svc core.TelegramServicer) { s.svc = svc }

// Warn records a warning and enforces the configured threshold action using
// the service's default Telegram transport.
func (s *Service) Warn(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, chatID, userID int64, reason string, warnedBy int64, threshold int, actionOnThreshold string) (*WarnResult, error) {
	return s.warnWithService(ctx, s.getService(), peer, user, chatID, userID, reason, warnedBy, threshold, actionOnThreshold)
}

// WarnWithService is the surface-aware variant used by Assistant P7-I. The
// caller-supplied Telegram servicer preserves the already-admitted execution
// surface, so threshold enforcement cannot accidentally escape through the
// userbot transport.
func (s *Service) WarnWithService(
	ctx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	user tg.InputPeerClass,
	chatID, userID int64,
	reason string,
	warnedBy int64,
	threshold int,
	actionOnThreshold string,
) (*WarnResult, error) {
	return s.warnWithServiceGuarded(
		ctx,
		svc,
		peer,
		user,
		chatID,
		userID,
		reason,
		warnedBy,
		threshold,
		actionOnThreshold,
		nil,
	)
}

// WarnWithServiceGuarded is the Assistant P7-I variant. guard is evaluated
// under the same-target warning stripe immediately before warning persistence,
// closing the role-change window between handler preflight and AddWarning.
func (s *Service) WarnWithServiceGuarded(
	ctx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	user tg.InputPeerClass,
	chatID, userID int64,
	reason string,
	warnedBy int64,
	threshold int,
	actionOnThreshold string,
	guard func(context.Context) error,
) (*WarnResult, error) {
	return s.warnWithServiceGuarded(
		ctx,
		svc,
		peer,
		user,
		chatID,
		userID,
		reason,
		warnedBy,
		threshold,
		actionOnThreshold,
		guard,
	)
}

func (s *Service) warnWithService(
	ctx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	user tg.InputPeerClass,
	chatID, userID int64,
	reason string,
	warnedBy int64,
	threshold int,
	actionOnThreshold string,
) (*WarnResult, error) {
	return s.warnWithServiceGuarded(
		ctx,
		svc,
		peer,
		user,
		chatID,
		userID,
		reason,
		warnedBy,
		threshold,
		actionOnThreshold,
		nil,
	)
}

func (s *Service) warnWithServiceGuarded(
	ctx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	user tg.InputPeerClass,
	chatID, userID int64,
	reason string,
	warnedBy int64,
	threshold int,
	actionOnThreshold string,
	guard func(context.Context) error,
) (*WarnResult, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("warning repository is nil")
	}
	if chatID <= 0 || userID <= 0 {
		return nil, fmt.Errorf("%w: warning chat/user coordinates must be positive", core.ErrInvalidArgs)
	}
	if threshold <= 0 {
		threshold = s.defaultThreshold
	}
	if threshold > MaxWarningThreshold {
		return nil, fmt.Errorf("%w: warning threshold exceeds %d", core.ErrResourceLimit, MaxWarningThreshold)
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxWarningReasonBytes {
		return nil, fmt.Errorf("%w: warning reason exceeds %d bytes", core.ErrInvalidArgs, MaxWarningReasonBytes)
	}
	if actionOnThreshold == "" {
		actionOnThreshold = ActionMute
	}

	lock := s.warningLock(chatID, userID)
	lock.Lock()
	defer lock.Unlock()

	count, err := s.repo.GetWarningCount(ctx, chatID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to check warning count: %w", err)
	}
	if count < threshold {
		if guard != nil {
			if err := guard(ctx); err != nil {
				return nil, err
			}
		}
		if err := s.repo.AddWarning(ctx, chatID, userID, reason, warnedBy); err != nil {
			return nil, fmt.Errorf("failed to add warning record: %w", err)
		}
		count++
	}
	res := &WarnResult{CurrentCount: count, Threshold: threshold, ActionTaken: ActionNone}
	if count < threshold {
		return res, nil
	}

	if svc == nil {
		return res, fmt.Errorf("%w: moderation Telegram service is nil", core.ErrUnavailable)
	}

	var actionErr error
	switch actionLower := strings.ToLower(actionOnThreshold); actionLower {
	case ActionKick:
		actionErr = svc.KickUser(ctx, peer, user)
		if actionErr == nil {
			res.ActionTaken = ActionKick
		}
	case ActionBan:
		actionErr = svc.BanUser(ctx, peer, user, 0)
		if actionErr == nil {
			res.ActionTaken = ActionBan
		}
	default:
		actionErr = svc.MuteUser(ctx, peer, user, int(time.Now().Add(24*time.Hour).Unix()))
		if actionErr == nil {
			res.ActionTaken = ActionMute
		}
	}

	if actionErr != nil {
		s.logger.Warn("automated moderation enforcement failed; warnings retained", zap.Error(actionErr), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID), zap.String("action", actionOnThreshold), zap.Int("count", count))
		return res, fmt.Errorf("threshold enforcement failed: %w", actionErr)
	}
	if err := s.repo.ResetWarnings(ctx, chatID, userID); err != nil {
		return res, fmt.Errorf("punitive action succeeded but failed to reset warnings: %w", err)
	}
	return res, nil
}

func (s *Service) GetWarnings(ctx context.Context, chatID, userID int64) ([]*WarningRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("warning repository is nil")
	}
	return s.repo.GetWarnings(ctx, chatID, userID)
}

func (s *Service) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	if s.repo == nil {
		return 0, fmt.Errorf("warning repository is nil")
	}
	return s.repo.GetWarningCount(ctx, chatID, userID)
}

func (s *Service) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	if s.repo == nil {
		return fmt.Errorf("warning repository is nil")
	}
	lock := s.warningLock(chatID, userID)
	lock.Lock()
	defer lock.Unlock()
	return s.repo.ResetWarnings(ctx, chatID, userID)
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
