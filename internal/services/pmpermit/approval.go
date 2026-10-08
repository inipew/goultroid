package pmpermit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ErrResolvedApprovalPeer prevents legacy approval from claiming success
// when Telegram is configured but no authenticated target peer was supplied.
var ErrResolvedApprovalPeer = errors.New("pm permit: resolved Telegram peer required")

// ApproveWithPeer persists approval and performs Telegram cleanup using the
// already-resolved peer. User/channel peers must carry an access hash.
func (s *Service) ApproveWithPeer(ctx context.Context, peer tg.InputPeerClass, userID int64, reason string, duration time.Duration) error {
	if userID == 0 {
		return fmt.Errorf("invalid user id")
	}
	if s.repo == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if !usablePeer(peer) {
		return fmt.Errorf("pm permit: unresolved or incomplete peer for user %d", userID)
	}
	unlock := s.lockUserStatus(userID)
	defer unlock()
	var exp *time.Time
	if duration != 0 {
		t := time.Now().UTC().Add(duration)
		exp = &t
	}
	if reason == "" {
		reason = "approved by user"
	}

	// Unblock first: on Telegram failure durable approval remains unchanged
	// (possibly blocked). This avoids an uncommitted positive cache entry and
	// an unsafe compensation window if a rollback were to fail.
	if svc := s.getService(); svc != nil {
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.publishEvent("approve", userID, "", 0, reason, false, err.Error())
			return fmt.Errorf("unblock user: %w", err)
		}
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil {
		return fmt.Errorf("persist PM approval: %w", err)
	}

	if err := s.repo.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset pm warn count", zap.Int64("user_id", userID), zap.Error(err))
	}
	entry := approvalCacheEntry{}
	if exp != nil {
		entry.expiresAt = *exp
	}
	s.approvedCache.Store(userID, entry)

	ids, idsErr := s.getWarnIDs(ctx, userID)
	if idsErr != nil {
		s.logger.Warn("failed to load PM warning IDs for approval cleanup", zap.Int64("user_id", userID), zap.Error(idsErr))
	}
	if len(ids) > 0 {
		if svc := s.getService(); svc != nil {
			if err := svc.DeleteMessage(ctx, peer, ids); err != nil {
				s.logger.Warn("failed to delete warning messages on approve", zap.Int64("user_id", userID), zap.Error(err))
			}
		}
	}
	if err := s.clearWarnIDs(ctx, userID); err != nil {
		s.logger.Warn("failed to clear PM warning IDs after approval", zap.Int64("user_id", userID), zap.Error(err))
	}
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
