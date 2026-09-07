package pmpermit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

const (
	StatusUnknown  = "unknown"
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusBlocked  = "blocked"

	DefaultMaxWarns     = 4
	DefaultWarnCooldown = 2500 * time.Millisecond
)

// PMActor represents metadata of the sender in a private chat.
type PMActor struct {
	UserID   int64
	IsBot    bool
	IsSelf   bool
	Verified bool
}

type Service struct {
	db            *database.DB
	svc           core.TelegramServicer
	svcFunc       func() core.TelegramServicer
	ownerID       int64
	perms         *core.Permissions
	logger        *zap.Logger
	enabled       bool
	maxWarns      int
	warnCooldown  time.Duration
	mu            sync.RWMutex
	approvedCache sync.Map
	warnIDs       map[int64][]int
	warnMu        sync.Mutex

	lastWarnTime map[int64]time.Time
	warnTimeMu   sync.Mutex

	eventBus *core.EventBus
}

type approvalCacheEntry struct {
	expiresAt time.Time
}

func NewService(db *database.DB, svc any, ownerID int64, perms *core.Permissions, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		db:           db,
		ownerID:      ownerID,
		perms:        perms,
		logger:       logger,
		enabled:      true,
		maxWarns:     DefaultMaxWarns,
		warnCooldown: DefaultWarnCooldown,
		warnIDs:      make(map[int64][]int),
		lastWarnTime: make(map[int64]time.Time),
	}
	switch v := svc.(type) {
	case core.TelegramServicer:
		s.svc = v
	case func() core.TelegramServicer:
		s.svcFunc = v
	}
	return s
}

func (s *Service) SetEventBus(eb *core.EventBus) {
	s.mu.Lock()
	s.eventBus = eb
	s.mu.Unlock()
}

func (s *Service) publishEvent(action string, userID int64, targetName string, warnCount int, reason string, success bool, errStr string) {
	s.mu.RLock()
	eb := s.eventBus
	s.mu.RUnlock()
	if eb == nil {
		return
	}
	eb.Publish(&core.PMPermitEvent{
		At:         time.Now(),
		Action:     action,
		UserID:     userID,
		TargetName: targetName,
		WarnCount:  warnCount,
		Reason:     reason,
		Success:    success,
		Error:      errStr,
	})
}

func (s *Service) SetWarnCooldown(d time.Duration) {
	s.mu.Lock()
	s.warnCooldown = d
	s.mu.Unlock()
}

func (s *Service) IsBotSent(msgID int) bool {
	if svc := s.getService(); svc != nil {
		return svc.IsBotSent(msgID)
	}
	return false
}
func (s *Service) OwnerID() int64 { return s.ownerID }
func (s *Service) IsSudoID(userID int64) bool {
	if s.perms != nil && s.perms.IsSudo(userID) {
		return true
	}
	return false
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
func (s *Service) IsEnabled() bool         { s.mu.RLock(); defer s.mu.RUnlock(); return s.enabled }
func (s *Service) SetEnabled(enabled bool) { s.mu.Lock(); s.enabled = enabled; s.mu.Unlock() }
func (s *Service) SetMaxWarns(max int) {
	s.mu.Lock()
	if max > 0 {
		s.maxWarns = max
	}
	s.mu.Unlock()
}

func (s *Service) IsApproved(ctx context.Context, userID int64) (bool, error) {
	if s.perms != nil && s.perms.IsSudo(userID) {
		return true, nil
	}
	if userID == s.ownerID {
		return true, nil
	}
	if value, ok := s.approvedCache.Load(userID); ok {
		entry := value.(approvalCacheEntry)
		if entry.expiresAt.IsZero() || time.Now().UTC().Before(entry.expiresAt) {
			return true, nil
		}
		s.approvedCache.Delete(userID)
	}
	if s.db == nil {
		return false, fmt.Errorf("pm permit database is unavailable")
	}
	rec, err := s.db.GetPMRecord(ctx, userID)
	if err != nil {
		return false, err
	}
	if rec == nil || rec.Status != StatusApproved {
		return false, nil
	}
	if rec.ExpiresAt != nil && time.Now().UTC().After(*rec.ExpiresAt) {
		_ = s.db.SetPMStatus(ctx, userID, StatusPending, "approval expired", nil)
		return false, nil
	}
	entry := approvalCacheEntry{}
	if rec.ExpiresAt != nil {
		entry.expiresAt = *rec.ExpiresAt
	}
	s.approvedCache.Store(userID, entry)
	return true, nil
}

func (s *Service) addWarnID(userID int64, msgID int) {
	if msgID == 0 {
		return
	}
	s.warnMu.Lock()
	s.warnIDs[userID] = append(s.warnIDs[userID], msgID)
	if len(s.warnIDs[userID]) > 20 {
		s.warnIDs[userID] = s.warnIDs[userID][len(s.warnIDs[userID])-20:]
	}
	s.warnMu.Unlock()
	if s.db != nil {
		_ = s.db.AddWarnMsgID(context.Background(), userID, msgID)
	}
}

func (s *Service) getWarnIDs(userID int64) []int {
	s.warnMu.Lock()
	ids := append([]int(nil), s.warnIDs[userID]...)
	s.warnMu.Unlock()
	if len(ids) == 0 && s.db != nil {
		if dbIDs, _ := s.db.GetWarnMsgIDs(context.Background(), userID); len(dbIDs) > 0 {
			return dbIDs
		}
	}
	// dedup
	seen := make(map[int]struct{})
	var out []int
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) clearWarnIDs(userID int64) {
	s.warnMu.Lock()
	delete(s.warnIDs, userID)
	s.warnMu.Unlock()
	if s.db != nil {
		_ = s.db.ClearWarnMsgIDs(context.Background(), userID)
	}
}

// IsWarnID returns true if msgID is a recorded warning message for userID.
func (s *Service) IsWarnID(userID int64, msgID int) bool {
	if msgID == 0 {
		return false
	}
	s.warnMu.Lock()
	ids := s.warnIDs[userID]
	for _, id := range ids {
		if id == msgID {
			s.warnMu.Unlock()
			return true
		}
	}
	s.warnMu.Unlock()
	if s.db != nil {
		if dbIDs, _ := s.db.GetWarnMsgIDs(context.Background(), userID); len(dbIDs) > 0 {
			for _, id := range dbIDs {
				if id == msgID {
					return true
				}
			}
		}
	}
	return false
}

// IsPMPermitMessage checks if text matches any bot-generated PM permit warning or response.
func (s *Service) IsPMPermitMessage(text string) bool {
	if text == "" {
		return false
	}
	signatures := []string{
		"haven't approved you for private messaging",
		"PM Permit Limit Reached",
		"You are blocked from private messaging",
		"PM Permit Status",
		"PM Permit is now",
		"Approved user",
		"Revoked approval",
		"Blocked user",
	}
	for _, sig := range signatures {
		if strings.Contains(text, sig) {
			return true
		}
	}
	return false
}

// IsBlocked checks whether userID is currently marked as blocked in the database.
func (s *Service) IsBlocked(ctx context.Context, userID int64) bool {
	if s.db == nil {
		return false
	}
	rec, err := s.db.GetPMRecord(ctx, userID)
	return err == nil && rec != nil && rec.Status == StatusBlocked
}

func (s *Service) resolvePeer(userID int64) tg.InputPeerClass {
	return &tg.InputPeerUser{UserID: userID}
}

func (s *Service) AutoApproveOutgoing(ctx context.Context, peer tg.InputPeerClass, userID int64) error {
	if userID == 0 || userID == s.ownerID {
		return nil
	}
	if s.IsSudoID(userID) {
		return nil
	}
	if approved, _ := s.IsApproved(ctx, userID); approved {
		return nil
	}
	if s.db == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if err := s.db.SetPMStatus(ctx, userID, StatusApproved, "outgoing auto-approved", nil); err != nil {
		s.logger.Error("failed to set pm status approved", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	s.approvedCache.Store(userID, approvalCacheEntry{})
	ids := s.getWarnIDs(userID)
	if len(ids) > 0 {
		if svc := s.getService(); svc != nil {
			if err := svc.DeleteMessage(ctx, peer, ids); err != nil {
				s.logger.Warn("failed to delete warning messages on auto-approve", zap.Int64("user_id", userID), zap.Error(err))
			}
		}
	}
	s.clearWarnIDs(userID)
	_ = s.db.ResetPMWarn(ctx, userID)
	if svc := s.getService(); svc != nil {
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.logger.Warn("failed to unblock user on auto-approve", zap.Int64("user_id", userID), zap.Error(err))
		}
	}
	s.publishEvent("auto_approve", userID, "", 0, "outgoing auto-approved", true, "")
	return nil
}

func (s *Service) Approve(ctx context.Context, userID int64, reason string, duration time.Duration) error {
	var exp *time.Time
	if duration != 0 {
		t := time.Now().UTC().Add(duration)
		exp = &t
	}
	if reason == "" {
		reason = "approved by user"
	}
	if s.db == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if err := s.db.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil {
		s.logger.Error("failed to set pm status approved", zap.Int64("user_id", userID), zap.Error(err))
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
	peer := s.resolvePeer(userID)
	if len(ids) > 0 {
		if svc := s.getService(); svc != nil {
			if err := svc.DeleteMessage(ctx, peer, ids); err != nil {
				s.logger.Warn("failed to delete warning messages on approve", zap.Int64("user_id", userID), zap.Error(err))
			}
		}
	}
	s.clearWarnIDs(userID)
	if svc := s.getService(); svc != nil {
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.logger.Warn("failed to unblock user on approve", zap.Int64("user_id", userID), zap.Error(err))
		}
	}
	s.publishEvent("approve", userID, "", 0, reason, true, "")
	return nil
}

func (s *Service) Disapprove(ctx context.Context, userID int64) error {
	s.approvedCache.Delete(userID)
	s.clearWarnIDs(userID)
	if s.db == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	_ = s.db.ResetPMWarn(ctx, userID)
	if err := s.db.SetPMStatus(ctx, userID, StatusPending, "approval revoked", nil); err != nil {
		s.logger.Error("failed to set pm status pending on disapprove", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	s.publishEvent("disapprove", userID, "", 0, "approval revoked", true, "")
	return nil
}

func (s *Service) Unblock(ctx context.Context, peer tg.InputPeerClass, userID int64) error {
	s.approvedCache.Delete(userID)
	s.clearWarnIDs(userID)
	if s.db == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	_ = s.db.ResetPMWarn(ctx, userID)
	if err := s.db.SetPMStatus(ctx, userID, StatusPending, "unblocked by user", nil); err != nil {
		s.logger.Error("failed to set pm status pending on unblock", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	if svc := s.getService(); svc != nil {
		if peer == nil {
			peer = s.resolvePeer(userID)
		}
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.logger.Warn("failed to unblock user via telegram rpc", zap.Int64("user_id", userID), zap.Error(err))
		}
	}
	s.publishEvent("unblock", userID, "", 0, "unblocked by user", true, "")
	return nil
}

func (s *Service) Block(ctx context.Context, userID int64, reason string) error {
	return s.BlockWithPeer(ctx, nil, userID, reason)
}

func (s *Service) BlockWithPeer(ctx context.Context, peer tg.InputPeerClass, userID int64, reason string) error {
	s.approvedCache.Delete(userID)
	if reason == "" {
		reason = "blocked by user"
	}
	if s.db == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if err := s.db.SetPMStatus(ctx, userID, StatusBlocked, reason, nil); err != nil {
		s.logger.Error("failed to set pm status blocked", zap.Int64("user_id", userID), zap.Error(err))
		return err
	}
	if svc := s.getService(); svc != nil {
		if peer == nil {
			peer = s.resolvePeer(userID)
		}
		if err := svc.BlockUser(ctx, peer); err != nil {
			s.logger.Warn("failed to block user via telegram rpc", zap.Int64("user_id", userID), zap.Error(err))
		}
	}
	s.publishEvent("block", userID, "", 0, reason, true, "")
	return nil
}

func (s *Service) HandleIncomingPM(ctx context.Context, peer tg.InputPeerClass, senderID int64, actorOpts ...PMActor) (bool, error) {
	if !s.IsEnabled() {
		return false, nil
	}

	// 1. Actor-level bypass: bots, verified users, self
	if len(actorOpts) > 0 {
		actor := actorOpts[0]
		if actor.IsBot || actor.IsSelf || actor.Verified {
			return false, nil
		}
	}

	// 2. Privilege bypass: owner & sudo
	if senderID == s.ownerID || s.IsSudoID(senderID) {
		return false, nil
	}

	// 3. Approval check
	approved, err := s.IsApproved(ctx, senderID)
	if err != nil {
		s.logger.Warn("pm permit approval check failed", zap.Error(err), zap.Int64("sender_id", senderID))
		return true, nil
	}
	if approved {
		return false, nil
	}
	if s.db == nil {
		return true, nil
	}

	// 4. Blocked state handling: SILENT DROP (no reply loop!)
	if rec, _ := s.db.GetPMRecord(ctx, senderID); rec != nil && rec.Status == StatusBlocked {
		return true, nil
	}

	// 5. Rate-limiting check to prevent warning reply storms and FloodWait
	if s.warnCooldown > 0 {
		s.warnTimeMu.Lock()
		lastTime := s.lastWarnTime[senderID]
		now := time.Now()
		inCooldown := !lastTime.IsZero() && now.Sub(lastTime) < s.warnCooldown
		if !inCooldown {
			s.lastWarnTime[senderID] = now
		}
		s.warnTimeMu.Unlock()

		if inCooldown {
			// Silently drop burst messages during cooldown
			return true, nil
		}
	}

	// 6. Warning counter increment
	warnCount, err := s.db.IncrementPMWarn(ctx, senderID)
	if err != nil {
		s.logger.Warn("failed to increment pm warn", zap.Error(err))
		return true, nil
	}
	s.mu.RLock()
	maxWarns := s.maxWarns
	s.mu.RUnlock()
	svc := s.getService()
	if svc == nil {
		return true, nil
	}

	if warnCount >= maxWarns {
		_ = s.BlockWithPeer(ctx, peer, senderID, "exceeded pm warning threshold")
		msg, err := svc.SendMessage(ctx, peer, "⛔ <b>PM Permit Limit Reached</b>\n\nYou sent too many unapproved messages without waiting. You have been blocked from private messaging.")
		if err != nil {
			s.logger.Warn("failed to send pm limit reached message", zap.Int64("user_id", senderID), zap.Error(err))
		} else if msg != nil {
			s.addWarnID(senderID, msg.ID)
		}
		return true, nil
	}

	remaining := maxWarns - warnCount
	warnMsg := fmt.Sprintf("👋 <b>Hello!</b>\n\nI haven't approved you for private messaging yet. Please wait patiently until I review your message.\n\n⚠️ <b>Warning %d/%d</b> (%d remaining before block)", warnCount, maxWarns, remaining)
	msg, err := svc.SendMessage(ctx, peer, warnMsg)
	if err != nil {
		s.logger.Warn("failed to send pm warning message", zap.Int64("user_id", senderID), zap.Error(err))
	} else if msg != nil {
		s.addWarnID(senderID, msg.ID)
	}
	s.publishEvent("warn", senderID, "", warnCount, fmt.Sprintf("warning %d/%d", warnCount, maxWarns), true, "")
	return true, nil
}

func (s *Service) ListApproved(ctx context.Context, limit, offset int) ([]*database.PMPermitRecord, error) {
	if s.db == nil {
		return nil, fmt.Errorf("pm permit database is unavailable")
	}
	return s.db.ListPMRecords(ctx, StatusApproved, limit, offset)
}

func (s *Service) ListBlocked(ctx context.Context, limit, offset int) ([]*database.PMPermitRecord, error) {
	if s.db == nil {
		return nil, fmt.Errorf("pm permit database is unavailable")
	}
	return s.db.ListPMRecords(ctx, StatusBlocked, limit, offset)
}

func (s *Service) ListPending(ctx context.Context, limit, offset int) ([]*database.PMPermitRecord, error) {
	if s.db == nil {
		return nil, fmt.Errorf("pm permit database is unavailable")
	}
	return s.db.ListPMRecords(ctx, StatusPending, limit, offset)
}

func (s *Service) GetStats(ctx context.Context) (pending, approved, blocked int, err error) {
	if s.db == nil {
		return 0, 0, 0, fmt.Errorf("pm permit database is unavailable")
	}
	return s.db.CountPMRecords(ctx)
}
