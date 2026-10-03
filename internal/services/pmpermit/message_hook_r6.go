package pmpermit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"go.uber.org/zap"
)

type IncomingPMEffectKind uint8

const (
	IncomingPMEffectNone IncomingPMEffectKind = iota
	IncomingPMEffectWarning
	IncomingPMEffectLimit
)

type IncomingPMDecision struct {
	Handled   bool
	Effect    IncomingPMEffectKind
	UserID    int64
	WarnCount int
	MaxWarns  int
}

type AutoApproveEffect struct {
	UserID  int64
	WarnIDs []int
}

func (s *Service) DecideIncomingPM(
	ctx context.Context,
	senderID int64,
	actor PMActor,
) (IncomingPMDecision, error) {
	decision := IncomingPMDecision{UserID: senderID}
	if !s.IsEnabled() {
		return decision, nil
	}
	if actor.IsBot || actor.IsSelf || actor.Verified {
		return decision, nil
	}
	if senderID == s.ownerID || s.IsSudoID(senderID) {
		return decision, nil
	}

	approved, err := s.IsApproved(ctx, senderID)
	if err != nil {
		s.logger.Warn("pm permit approval check failed", zap.Error(err), zap.Int64("sender_id", senderID))
		decision.Handled = true
		return decision, nil
	}
	if approved {
		return decision, nil
	}
	if s.repo == nil {
		decision.Handled = true
		return decision, nil
	}

	if rec, _ := s.repo.GetPMRecord(ctx, senderID); rec != nil && rec.Status == StatusBlocked {
		decision.Handled = true
		return decision, nil
	}

	warnCooldown := s.WarnCooldown()
	if warnCooldown > 0 {
		s.warnTimeMu.Lock()
		lastTime := s.lastWarnTime[senderID]
		now := time.Now()
		inCooldown := !lastTime.IsZero() && now.Sub(lastTime) < warnCooldown
		if !inCooldown {
			s.lastWarnTime[senderID] = now
		}
		s.warnTimeMu.Unlock()
		if inCooldown {
			decision.Handled = true
			return decision, nil
		}
	}

	warnCount, err := s.repo.IncrementPMWarn(ctx, senderID)
	if err != nil {
		s.logger.Warn("failed to increment pm warn", zap.Error(err))
		decision.Handled = true
		return decision, nil
	}

	s.mu.RLock()
	maxWarns := s.maxWarns
	s.mu.RUnlock()

	decision.Handled = true
	decision.WarnCount = warnCount
	decision.MaxWarns = maxWarns
	if warnCount >= maxWarns {
		s.approvedCache.Delete(senderID)
		if err := s.repo.SetPMStatus(ctx, senderID, StatusBlocked, "exceeded pm warning threshold", nil); err != nil {
			s.logger.Warn("failed to persist pm block threshold", zap.Int64("user_id", senderID), zap.Error(err))
		}
		decision.Effect = IncomingPMEffectLimit
		s.publishEvent("block", senderID, "", warnCount, "exceeded pm warning threshold", true, "")
		return decision, nil
	}

	decision.Effect = IncomingPMEffectWarning
	return decision, nil
}

func (s *Service) ApplyIncomingPMEffect(
	ctx context.Context,
	peer tg.InputPeerClass,
	decision IncomingPMDecision,
) error {
	if !decision.Handled || decision.Effect == IncomingPMEffectNone || decision.UserID == 0 {
		return nil
	}

	svc := s.getService()
	if svc == nil {
		return nil
	}

	switch decision.Effect {
	case IncomingPMEffectWarning:
		approved, err := s.IsApproved(ctx, decision.UserID)
		if err != nil || approved {
			return nil
		}
		if s.repo != nil {
			record, recordErr := s.repo.GetPMRecord(ctx, decision.UserID)
			if recordErr != nil || (record != nil && record.Status == StatusBlocked) {
				return nil
			}
		}
		remaining := decision.MaxWarns - decision.WarnCount
		if remaining < 0 {
			remaining = 0
		}
		msg, err := s.sendTemplate(ctx, svc, peer, pmWarningResponse, pmWarningTemplate, savedresponse.TemplateVars{
			Extra: map[string]string{
				"count":     strconv.Itoa(decision.WarnCount),
				"limit":     strconv.Itoa(decision.MaxWarns),
				"remaining": strconv.Itoa(remaining),
			},
		})
		if err != nil {
			s.logger.Warn("failed to send pm warning message", zap.Int64("user_id", decision.UserID), zap.Error(err))
		} else if msg != nil {
			s.addWarnID(decision.UserID, msg.ID)
		}
		s.publishEvent(
			"warn",
			decision.UserID,
			"",
			decision.WarnCount,
			fmt.Sprintf("warning %d/%d", decision.WarnCount, decision.MaxWarns),
			true,
			"",
		)
		return nil

	case IncomingPMEffectLimit:
		if !s.IsBlocked(ctx, decision.UserID) {
			return nil
		}
		if err := svc.BlockUser(ctx, peer); err != nil {
			s.logger.Warn("failed to block user after pm limit", zap.Int64("user_id", decision.UserID), zap.Error(err))
		}
		msg, err := s.sendTemplate(ctx, svc, peer, pmLimitResponse, pmLimitTemplate, savedresponse.TemplateVars{})
		if err != nil {
			s.logger.Warn("failed to send pm limit reached message", zap.Int64("user_id", decision.UserID), zap.Error(err))
		} else if msg != nil {
			s.addWarnID(decision.UserID, msg.ID)
		}
		return nil
	default:
		return nil
	}
}

func (s *Service) PrepareAutoApproveOutgoing(
	ctx context.Context,
	userID int64,
) (AutoApproveEffect, bool, error) {
	effect := AutoApproveEffect{UserID: userID}
	if userID == 0 || userID == s.ownerID || s.IsSudoID(userID) {
		return effect, false, nil
	}
	if approved, _ := s.IsApproved(ctx, userID); approved {
		return effect, false, nil
	}
	if s.repo == nil {
		return effect, false, fmt.Errorf("pm permit database is unavailable")
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusApproved, "outgoing auto-approved", nil); err != nil {
		s.logger.Error("failed to set pm status approved", zap.Int64("user_id", userID), zap.Error(err))
		return effect, false, err
	}
	s.approvedCache.Store(userID, approvalCacheEntry{})
	effect.WarnIDs = s.getWarnIDs(userID)
	_ = s.repo.ResetPMWarn(ctx, userID)
	s.publishEvent("auto_approve", userID, "", 0, "outgoing auto-approved", true, "")
	return effect, true, nil
}

func (s *Service) ApplyAutoApproveOutgoingEffect(
	ctx context.Context,
	peer tg.InputPeerClass,
	effect AutoApproveEffect,
) error {
	if effect.UserID == 0 {
		return nil
	}
	approved, err := s.IsApproved(ctx, effect.UserID)
	if err != nil || !approved {
		return nil
	}
	svc := s.getService()
	if svc == nil {
		return nil
	}
	warnIDs := mergeWarnIDs(effect.WarnIDs, s.getWarnIDs(effect.UserID))
	if len(warnIDs) > 0 {
		if err := svc.DeleteMessage(ctx, peer, warnIDs); err != nil {
			s.logger.Warn("failed to delete warning messages on auto-approve", zap.Int64("user_id", effect.UserID), zap.Error(err))
		} else {
			s.clearWarnIDs(effect.UserID)
		}
	}
	if err := svc.UnblockUser(ctx, peer); err != nil {
		s.logger.Warn("failed to unblock user on auto-approve", zap.Int64("user_id", effect.UserID), zap.Error(err))
	}
	return nil
}

func mergeWarnIDs(groups ...[]int) []int {
	seen := make(map[int]struct{})
	var merged []int
	for _, group := range groups {
		for _, id := range group {
			if id == 0 {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
	}
	return merged
}
