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

const ( StatusUnknown = "unknown"; StatusPending = "pending"; StatusApproved = "approved"; StatusDenied = "denied"; StatusBlocked = "blocked"; DefaultMaxWarns = 4 )

type Service struct { db *database.DB; svc core.TelegramServicer; svcFunc func() core.TelegramServicer; ownerID int64; perms *core.Permissions; logger *zap.Logger; enabled bool; maxWarns int; mu sync.RWMutex; approvedCache sync.Map }
type approvalCacheEntry struct { expiresAt time.Time }

func NewService(db *database.DB, svc any, ownerID int64, perms *core.Permissions, logger *zap.Logger) *Service {
	if logger == nil { logger = zap.NewNop() }
	s := &Service{db: db, ownerID: ownerID, perms: perms, logger: logger, enabled: true, maxWarns: DefaultMaxWarns}
	switch v := svc.(type) { case core.TelegramServicer: s.svc = v; case func() core.TelegramServicer: s.svcFunc = v }
	return s
}
func (s *Service) getService() core.TelegramServicer { if s.svc != nil { return s.svc }; if s.svcFunc != nil { return s.svcFunc() }; return nil }
func (s *Service) IsEnabled() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.enabled }
func (s *Service) SetEnabled(enabled bool) { s.mu.Lock(); s.enabled = enabled; s.mu.Unlock() }
func (s *Service) SetMaxWarns(max int) { s.mu.Lock(); if max > 0 { s.maxWarns = max }; s.mu.Unlock() }

func (s *Service) IsApproved(ctx context.Context, userID int64) (bool, error) {
	if s.perms != nil && s.perms.IsSudo(userID) { return true, nil }
	if userID == s.ownerID { return true, nil }
	if value, ok := s.approvedCache.Load(userID); ok {
		entry := value.(approvalCacheEntry)
		if entry.expiresAt.IsZero() || time.Now().UTC().Before(entry.expiresAt) { return true, nil }
		s.approvedCache.Delete(userID)
	}
	if s.db == nil { return false, fmt.Errorf("pm permit database is unavailable") }
	rec, err := s.db.GetPMRecord(ctx, userID)
	if err != nil { return false, err }
	if rec == nil || rec.Status != StatusApproved { return false, nil }
	if rec.ExpiresAt != nil && time.Now().UTC().After(*rec.ExpiresAt) { _ = s.db.SetPMStatus(ctx, userID, StatusPending, "approval expired", nil); return false, nil }
	entry := approvalCacheEntry{}; if rec.ExpiresAt != nil { entry.expiresAt = *rec.ExpiresAt }; s.approvedCache.Store(userID, entry)
	return true, nil
}

func (s *Service) Approve(ctx context.Context, userID int64, reason string, duration time.Duration) error {
	var exp *time.Time; if duration != 0 { t := time.Now().UTC().Add(duration); exp = &t }
	if reason == "" { reason = "approved by user" }
	if s.db == nil { return fmt.Errorf("pm permit database is unavailable") }
	if err := s.db.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil { return err }
	if err := s.db.ResetPMWarn(ctx, userID); err != nil { return err }
	entry := approvalCacheEntry{}; if exp != nil { entry.expiresAt = *exp }; s.approvedCache.Store(userID, entry)
	return nil
}
func (s *Service) Disapprove(ctx context.Context, userID int64) error { s.approvedCache.Delete(userID); if s.db == nil { return fmt.Errorf("pm permit database is unavailable") }; return s.db.SetPMStatus(ctx, userID, StatusPending, "approval revoked", nil) }
func (s *Service) Block(ctx context.Context, userID int64, reason string) error { s.approvedCache.Delete(userID); if reason == "" { reason = "blocked by user" }; if s.db == nil { return fmt.Errorf("pm permit database is unavailable") }; return s.db.SetPMStatus(ctx, userID, StatusBlocked, reason, nil) }

func (s *Service) HandleIncomingPM(ctx context.Context, peer tg.InputPeerClass, senderID int64) (bool, error) {
	if !s.IsEnabled() { return false, nil }
	approved, err := s.IsApproved(ctx, senderID)
	if err != nil {
		// Security gate must fail closed. A DB outage must never turn into an
		// accidental bypass of PM protection.
		s.logger.Warn("pm permit approval check failed", zap.Error(err), zap.Int64("sender_id", senderID))
		return true, nil
	}
	if approved { return false, nil }
	if s.db == nil { return true, nil }
	warnCount, err := s.db.IncrementPMWarn(ctx, senderID)
	if err != nil { s.logger.Warn("failed to increment pm warn", zap.Error(err)); return true, nil }
	s.mu.RLock(); maxWarns := s.maxWarns; s.mu.RUnlock()
	svc := s.getService(); if svc == nil { return true, nil }
	if warnCount >= maxWarns { _ = s.Block(ctx, senderID, "exceeded pm warning threshold"); _, _ = svc.SendMessage(ctx, peer, "⛔ <b>PM Permit Limit Reached</b>\n\nYou sent too many unapproved messages without waiting. You have been blocked from private messaging."); return true, nil }
	remaining := maxWarns - warnCount
	warnMsg := fmt.Sprintf("👋 <b>Hello!</b>\n\nI haven't approved you for private messaging yet. Please wait patiently until I review your message.\n\n⚠️ <b>Warning %d/%d</b> (%d remaining before block)", warnCount, maxWarns, remaining)
	_, _ = svc.SendMessage(ctx, peer, warnMsg)
	return true, nil
}
