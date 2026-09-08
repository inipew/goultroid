package pmpermit

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ApproveWithPeer persists approval and performs Telegram cleanup using the
// already-resolved peer. The caller must provide a usable peer; user peers
// without an access hash are rejected instead of being sent to MTProto.
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

	// Telegram state is the externally visible side effect. Do it before
	// publishing success, while the durable DB state is still authoritative.
	svc := s.getService()
	if svc != nil {
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.publishEvent("approve", userID, "", 0, reason, false, err.Error())
			return fmt.Errorf("unblock user: %w", err)
		}
	}

	if err := s.db.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil {
		return err
	}
	if err := s.db.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset pm warn count", zap.Int64("user_id", userID), zap.Error(err))
	}

	entry := approvalCacheEntry{}
	if exp != nil {
		entry.expiresAt = *exp
	}
	s.approvedCache.Store(userID, entry)

	ids := s.getWarnIDs(userID)
	if len(ids) > 0 && svc != nil {
		if err := svc.DeleteMessage(ctx, peer, ids); err != nil {
			s.logger.Warn("failed to delete warning messages on approve", zap.Int64("user_id", userID), zap.Error(err))
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
