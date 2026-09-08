package pmpermit

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ApproveWithPeer persists approval and performs Telegram cleanup using the
// already-resolved peer. This avoids reconstructing an InputPeerUser without
// an access hash later in the service.
func (s *Service) ApproveWithPeer(ctx context.Context, peer tg.InputPeerClass, userID int64, reason string, duration time.Duration) error {
	if userID == 0 { return fmt.Errorf("invalid user id") }
	if s.db == nil { return fmt.Errorf("pm permit database is unavailable") }
	var exp *time.Time
	if duration != 0 { t := time.Now().UTC().Add(duration); exp = &t }
	if reason == "" { reason = "approved by user" }
	if err := s.db.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil { return err }
	if err := s.db.ResetPMWarn(ctx, userID); err != nil { s.logger.Warn("failed to reset pm warn count", zap.Int64("user_id", userID), zap.Error(err)) }
	entry := approvalCacheEntry{}
	if exp != nil { entry.expiresAt = *exp }
	s.approvedCache.Store(userID, entry)

	ids := s.getWarnIDs(userID)
	if len(ids) > 0 {
		if svc := s.getService(); svc != nil && peer != nil {
			if err := svc.DeleteMessage(ctx, peer, ids); err != nil { s.logger.Warn("failed to delete warning messages on approve", zap.Int64("user_id", userID), zap.Error(err)) }
		}
	}
	s.clearWarnIDs(userID)
	if svc := s.getService(); svc != nil && peer != nil {
		if err := svc.UnblockUser(ctx, peer); err != nil { s.logger.Warn("failed to unblock user on approve", zap.Int64("user_id", userID), zap.Error(err)) }
	}
	s.publishEvent("approve", userID, "", 0, reason, true, "")
	return nil
}
