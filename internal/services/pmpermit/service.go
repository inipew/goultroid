package pmpermit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

const (
	// StatusUnknown indicates a user with no prior record.
	StatusUnknown = "unknown"
	// StatusPending indicates an unapproved user within warning limit.
	StatusPending = "pending"
	// StatusApproved indicates the user is authorized to send private messages.
	StatusApproved = "approved"
	// StatusDenied indicates user was denied PM access.
	StatusDenied = "denied"
	// StatusBlocked indicates user exceeded warning limit and is blocked.
	StatusBlocked = "blocked"

	// DefaultMaxWarns is the maximum number of unapproved messages before auto-block.
	DefaultMaxWarns = 4
)

// Service coordinates private message access control and anti-flood warnings.
type Service struct {
	db       *database.DB
	svc      core.TelegramServicer
	svcFunc  func() core.TelegramServicer
	ownerID  int64
	perms    *core.Permissions
	logger   *zap.Logger
	enabled  bool
	maxWarns int
	mu       sync.RWMutex
}

// NewService creates a new PM Permit service instance.
func NewService(db *database.DB, svc any, ownerID int64, perms *core.Permissions, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		db:       db,
		ownerID:  ownerID,
		perms:    perms,
		logger:   logger,
		enabled:  true,
		maxWarns: DefaultMaxWarns,
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

// IsEnabled returns whether the PM Permit shield is currently active.
func (s *Service) IsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// SetEnabled toggles the PM Permit shield.
func (s *Service) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
}

// SetMaxWarns updates the maximum warning threshold.
func (s *Service) SetMaxWarns(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if max > 0 {
		s.maxWarns = max
	}
}

// IsApproved checks if a user is authorized to send private messages.
func (s *Service) IsApproved(ctx context.Context, userID int64) (bool, error) {
	if s.perms != nil && s.perms.IsSudo(userID) {
		return true, nil
	}
	if userID == s.ownerID {
		return true, nil
	}

	rec, err := s.db.GetPMRecord(ctx, userID)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, nil
	}

	if rec.Status == StatusApproved {
		if rec.ExpiresAt != nil && time.Now().UTC().After(*rec.ExpiresAt) {
			_ = s.db.SetPMStatus(ctx, userID, StatusPending, "approval expired", nil)
			return false, nil
		}
		return true, nil
	}

	return false, nil
}

// Approve authorizes a user to send private messages.
func (s *Service) Approve(ctx context.Context, userID int64, reason string, duration time.Duration) error {
	var exp *time.Time
	if duration != 0 {
		t := time.Now().UTC().Add(duration)
		exp = &t
	}
	if reason == "" {
		reason = "approved by user"
	}
	if err := s.db.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil {
		return err
	}
	return s.db.ResetPMWarn(ctx, userID)
}

// Disapprove revokes PM approval for a user.
func (s *Service) Disapprove(ctx context.Context, userID int64) error {
	return s.db.SetPMStatus(ctx, userID, StatusPending, "approval revoked", nil)
}

// Block marks a user as blocked in PM permit.
func (s *Service) Block(ctx context.Context, userID int64, reason string) error {
	if reason == "" {
		reason = "blocked by user"
	}
	return s.db.SetPMStatus(ctx, userID, StatusBlocked, reason, nil)
}

// HandleIncomingPM intercepts an incoming private message and enforces PM permit policies.
// Returns handled=true if the message was handled by PM permit and should not be processed further.
func (s *Service) HandleIncomingPM(ctx context.Context, peer tg.InputPeerClass, senderID int64) (bool, error) {
	if !s.IsEnabled() {
		return false, nil
	}

	approved, err := s.IsApproved(ctx, senderID)
	if err != nil {
		s.logger.Warn("pm permit approval check failed", zap.Error(err), zap.Int64("sender_id", senderID))
		return false, nil
	}
	if approved {
		return false, nil
	}

	// Unapproved user: check / increment warning count
	warnCount, err := s.db.IncrementPMWarn(ctx, senderID)
	if err != nil {
		s.logger.Warn("failed to increment pm warn", zap.Error(err))
		return false, nil
	}

	s.mu.RLock()
	maxWarns := s.maxWarns
	s.mu.RUnlock()

	svc := s.getService()
	if svc == nil {
		return true, nil
	}

	if warnCount >= maxWarns {
		_ = s.Block(ctx, senderID, "exceeded pm warning threshold")
		blockMsg := "⛔ <b>PM Permit Limit Reached</b>\n\nYou sent too many unapproved messages without waiting. You have been blocked from private messaging."
		_, _ = svc.SendMessage(ctx, peer, blockMsg)
		return true, nil
	}

	remaining := maxWarns - warnCount
	warnMsg := fmt.Sprintf(
		"👋 <b>Hello!</b>\n\nI haven't approved you for private messaging yet. Please wait patiently until I review your message.\n\n⚠️ <b>Warning %d/%d</b> (%d remaining before block)",
		warnCount, maxWarns, remaining,
	)
	_, _ = svc.SendMessage(ctx, peer, warnMsg)
	return true, nil
}
