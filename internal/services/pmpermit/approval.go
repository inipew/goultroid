package pmpermit

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ApproveWithPeer persists approval and performs Telegram cleanup using the
// already-resolved peer. User/channel peers must carry an access hash.
func (s *Service) ApproveWithPeer(ctx context.Context, peer tg.InputPeerClass, userID int64, reason string, duration time.Duration) error {
	if userID == 0 {
		return fmt.Errorf("invalid user id")
	}
	if s.db == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if !usablePeer(peer) {
		return fmt.Errorf("pm permit: unresolved or incomplete peer for user %d", userID)
	}
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

	// Telegram is an external side effect. If it fails, compensate the durable
	// state so DB/cache do not claim approval that Telegram did not establish.
	if svc := s.getService(); svc != nil {
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.approvedCache.Delete(userID)
			if rollbackErr := s.db.SetPMStatus(ctx, userID, StatusPending, "approval Telegram side-effect failed", nil); rollbackErr != nil {
				s.logger.Error("failed to rollback PM approval after Telegram failure", zap.Int64("user_id", userID), zap.Error(rollbackErr))
			}
			s.publishEvent("approve", userID, "", 0, reason, false, err.Error())
			return fmt.Errorf("unblock user: %w", err)
		}
	}

	if err := s.db.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset pm warn count", zap.Int64("user_id", userID), zap.Error(err))
	}
	entry := approvalCacheEntry{}
	if exp != nil {
		entry.expiresAt = *exp
	}
	s.approvedCache.Store(userID, entry)

	if ids := s.getWarnIDs(userID); len(ids) > 0 {
		if svc := s.getService(); svc != nil {
			if err := svc.DeleteMessage(ctx, peer, ids); err != nil {
				s.logger.Warn("failed to delete warning messages on approve", zap.Int64("user_id", userID), zap.Error(err))
			}
		}
	}
	s.clearWarnIDs(userID)
	s.publishEvent("approve", userID, "", 0, reason, true, "")
	return nil
}

func usablePeer(peer tg.InputPeerClass) bool {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return p != nil && p.UserID != 0 && p.AccessHash != 0
	case *tg.InputPeerChannel:
		return p != nil && p.ChannelID != 0 && p.AccessHash != 0
	case *tg.InputPeerChat:
		return p != nil && p.ChatID != 0
	case *tg.InputPeerSelf:
		return p != nil
	default:
		return peer != nil
	}
}
