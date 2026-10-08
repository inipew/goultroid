package pmpermit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"go.uber.org/zap"
)

var (
	pmWarningResponse = savedresponse.NewHTML("👋 <b>Hello!</b>\n\nI haven't approved you for private messaging yet. Please wait patiently until I review your message.\n\n⚠️ <b>Warning {count}/{limit}</b> ({remaining} remaining before block)")
	pmWarningTemplate = mustCompilePMPermitResponse(pmWarningResponse, "count", "limit", "remaining")
	pmLimitResponse   = savedresponse.NewHTML("⛔ <b>PM Permit Limit Reached</b>\n\nYou sent too many unapproved messages without waiting. You have been blocked from private messaging.")
	pmLimitTemplate   = mustCompilePMPermitResponse(pmLimitResponse)
)

func mustCompilePMPermitResponse(response savedresponse.Response, variables ...string) *savedresponse.CompiledTemplate {
	compiled, err := savedresponse.CompileWithVariables(response, variables...)
	if err != nil {
		panic(fmt.Sprintf("pmpermit: compile static response: %v", err))
	}
	return compiled
}

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

type TelegramService interface {
	SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error)
	DeleteMessage(context.Context, tg.InputPeerClass, []int) error
	BlockUser(context.Context, tg.InputPeerClass) error
	UnblockUser(context.Context, tg.InputPeerClass) error
	IsBotSent(int) bool
}

type Service struct {
	repo          Repository
	svc           TelegramService
	svcFunc       func() TelegramService
	ownerID       int64
	perms         *core.Permissions
	logger        *zap.Logger
	enabled       bool
	maxWarns      int
	warnCooldown  time.Duration
	mu            sync.RWMutex
	approvedCache boundedApprovalCache
	warnIDs       map[int64][]int
	warnMu        sync.Mutex

	lastWarnTime map[int64]time.Time
	warnTimeMu   sync.Mutex
	statusLocks  [128]sync.Mutex

	eventBus *core.EventBus
	delivery *savedresponse.ResponseDelivery
}

type approvalCacheEntry struct {
	expiresAt time.Time
}

func NewService(repo Repository, svc any, ownerID int64, perms *core.Permissions, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		repo:         repo,
		ownerID:      ownerID,
		perms:        perms,
		logger:       logger,
		enabled:      true,
		maxWarns:     DefaultMaxWarns,
		warnCooldown: DefaultWarnCooldown,
		warnIDs:      make(map[int64][]int),
		lastWarnTime: make(map[int64]time.Time),
		delivery:     savedresponse.NewResponseDelivery(savedresponse.NewService(nil)),
	}
	switch v := svc.(type) {
	case TelegramService:
		s.svc = v
	case func() TelegramService:
		s.svcFunc = v
	case func() core.TelegramServicer:
		s.svcFunc = func() TelegramService { return v() }
	}
	return s
}

func (s *Service) sendTemplate(
	ctx context.Context,
	svc TelegramService,
	peer tg.InputPeerClass,
	response savedresponse.Response,
	compiled *savedresponse.CompiledTemplate,
	vars savedresponse.TemplateVars,
) (*tg.Message, error) {
	if svc == nil {
		return nil, fmt.Errorf("pm permit telegram service is unavailable")
	}
	if s.delivery == nil {
		return nil, savedresponse.ErrResponseDeliveryUnavailable
	}
	var sent *tg.Message
	_, err := s.delivery.DeliverCompiled(ctx, response, compiled, vars, savedresponse.DeliverySink{
		SendText: func(text string) error {
			var sendErr error
			sent, sendErr = svc.SendMessage(ctx, peer, text)
			return sendErr
		},
	})
	return sent, err
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
	if eb == nil || !eb.HasSubscribersAtPriority(core.EventTypePMPermit, core.PriorityNormal) {
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

func (s *Service) WarnCooldown() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.warnCooldown
}

func (s *Service) MaxWarns() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxWarns
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
func (s *Service) getService() TelegramService {
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
	unlock := s.lockUserStatus(userID)
	defer unlock()
	return s.isApprovedLocked(ctx, userID)
}

// Called only while the user status lock is held, including from PM ingress.
func (s *Service) isApprovedLocked(ctx context.Context, userID int64) (bool, error) {
	if value, ok := s.approvedCache.Load(userID); ok {
		entry := value.(approvalCacheEntry)
		if entry.expiresAt.IsZero() || time.Now().UTC().Before(entry.expiresAt) {
			return true, nil
		}
		s.approvedCache.Delete(userID)
	}
	if s.repo == nil {
		return false, fmt.Errorf("pm permit database is unavailable")
	}
	rec, err := s.repo.GetPMRecord(ctx, userID)
	if err != nil {
		return false, err
	}
	if rec == nil || rec.Status != StatusApproved {
		return false, nil
	}
	if rec.ExpiresAt != nil && time.Now().UTC().After(*rec.ExpiresAt) {
		if err := s.repo.SetPMStatus(ctx, userID, StatusPending, "approval expired", nil); err != nil {
			return false, fmt.Errorf("persist expired PM approval: %w", err)
		}
		return false, nil
	}
	entry := approvalCacheEntry{}
	if rec.ExpiresAt != nil {
		entry.expiresAt = *rec.ExpiresAt
	}
	s.approvedCache.Store(userID, entry)
	return true, nil
}

// addWarnID persists the bot-origin marker using the ingress context. If
// persistence fails, keep the bounded in-memory marker until a later retry.
func (s *Service) addWarnID(ctx context.Context, userID int64, msgID int) error {
	if msgID == 0 {
		return nil
	}
	var persistErr error
	if s.repo != nil {
		persistErr = s.repo.AddWarnMsgID(ctx, userID, msgID)
	}
	s.warnMu.Lock()
	if _, tracked := s.warnIDs[userID]; !tracked && len(s.warnIDs) >= maxPMPermitWarnUsers {
		// The DB is authoritative after eviction; never grow beyond capacity.
		remaining := maxPMPermitWarnUsers * 3 / 4
		for oldUser := range s.warnIDs {
			delete(s.warnIDs, oldUser)
			if len(s.warnIDs) <= remaining {
				break
			}
		}
	}
	s.warnIDs[userID] = append(s.warnIDs[userID], msgID)
	if len(s.warnIDs[userID]) > 20 {
		s.warnIDs[userID] = s.warnIDs[userID][len(s.warnIDs[userID])-20:]
	}
	s.warnMu.Unlock()
	if persistErr != nil {
		return fmt.Errorf("persist PM warning message ID: %w", persistErr)
	}
	return nil
}

// getWarnIDs uses the caller's deadline for the DB fallback and never
// silently treats a failed read as an empty durable warning list.
func (s *Service) getWarnIDs(ctx context.Context, userID int64) ([]int, error) {
	s.warnMu.Lock()
	ids := append([]int(nil), s.warnIDs[userID]...)
	s.warnMu.Unlock()
	if len(ids) == 0 && s.repo != nil {
		var err error
		ids, err = s.repo.GetWarnMsgIDs(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("read PM warning message IDs: %w", err)
		}
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}

// clearWarnIDs commits durable cleanup before evicting the in-memory copy.
// On SQLite failure callers may retry; no unbounded goroutine is spawned.
func (s *Service) clearWarnIDs(ctx context.Context, userID int64) error {
	if s.repo != nil {
		if err := s.repo.ClearWarnMsgIDs(ctx, userID); err != nil {
			return fmt.Errorf("clear PM warning message IDs: %w", err)
		}
	}
	s.warnMu.Lock()
	delete(s.warnIDs, userID)
	s.warnMu.Unlock()
	return nil
}

// IsWarnID only returns a definitive miss after successful storage lookup.
// This prevents an outgoing PMPermit warning from being auto-approved if
// SQLite is unavailable and its in-memory marker was evicted.
func (s *Service) IsWarnID(ctx context.Context, userID int64, msgID int) (bool, error) {
	if msgID == 0 {
		return false, nil
	}
	s.warnMu.Lock()
	for _, id := range s.warnIDs[userID] {
		if id == msgID {
			s.warnMu.Unlock()
			return true, nil
		}
	}
	s.warnMu.Unlock()
	if s.repo != nil {
		ids, err := s.repo.GetWarnMsgIDs(ctx, userID)
		if err != nil {
			return false, fmt.Errorf("check PM warning message ID: %w", err)
		}
		for _, id := range ids {
			if id == msgID {
				return true, nil
			}
		}
	}
	return false, nil
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
	if s.repo == nil {
		return false
	}
	rec, err := s.repo.GetPMRecord(ctx, userID)
	return err == nil && rec != nil && rec.Status == StatusBlocked
}

func (s *Service) resolvePeer(userID int64) tg.InputPeerClass {
	return &tg.InputPeerUser{UserID: userID}
}

func (s *Service) AutoApproveOutgoing(ctx context.Context, peer tg.InputPeerClass, userID int64) error {
	if userID == 0 || userID == s.ownerID || s.IsSudoID(userID) {
		return nil
	}
	unlock := s.lockUserStatus(userID)
	defer unlock()
	approved, err := s.isApprovedLocked(ctx, userID)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}
	if s.repo == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	// Telegram must unblock first; DB/cache never claim an unusable approval.
	if svc := s.getService(); svc != nil {
		if !usablePeer(peer) {
			return fmt.Errorf("pm permit: unresolved outgoing peer for user %d", userID)
		}
		if err := svc.UnblockUser(ctx, peer); err != nil {
			return fmt.Errorf("auto-approve unblock user: %w", err)
		}
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusApproved, "outgoing auto-approved", nil); err != nil {
		return err
	}
	s.approvedCache.Store(userID, approvalCacheEntry{})
	ids, idsErr := s.getWarnIDs(ctx, userID)
	if idsErr != nil {
		s.logger.Warn("failed to load PM warning IDs for cleanup", zap.Int64("user_id", userID), zap.Error(idsErr))
	}
	if len(ids) > 0 {
		if svc := s.getService(); svc != nil {
			if err := svc.DeleteMessage(ctx, peer, ids); err != nil {
				s.logger.Warn("failed to delete warning messages on auto-approve", zap.Int64("user_id", userID), zap.Error(err))
			}
		}
	}
	if err := s.clearWarnIDs(ctx, userID); err != nil {
		s.logger.Warn("failed to clear PM warning IDs", zap.Int64("user_id", userID), zap.Error(err))
	}
	if err := s.repo.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset PM warn count after auto-approval", zap.Int64("user_id", userID), zap.Error(err))
	}
	s.publishEvent("auto_approve", userID, "", 0, "outgoing auto-approved", true, "")
	return nil
}

// Approve is for deliberately storage-only installations. Managed Telegram
// callers must use ApproveWithPeer so an actual access hash is supplied before
// durable approval can be published.
func (s *Service) Approve(ctx context.Context, userID int64, reason string, duration time.Duration) error {
	if s.svc != nil || s.svcFunc != nil {
		return fmt.Errorf("%w: use ApproveWithPeer", ErrResolvedApprovalPeer)
	}
	unlock := s.lockUserStatus(userID)
	defer unlock()
	if s.repo == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	var exp *time.Time
	if duration != 0 {
		t := time.Now().UTC().Add(duration)
		exp = &t
	}
	if reason == "" {
		reason = "approved by user"
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusApproved, reason, exp); err != nil {
		return fmt.Errorf("persist storage-only PM approval: %w", err)
	}
	if err := s.repo.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset PM warn count", zap.Int64("user_id", userID), zap.Error(err))
	}
	entry := approvalCacheEntry{}
	if exp != nil {
		entry.expiresAt = *exp
	}
	s.approvedCache.Store(userID, entry)
	if err := s.clearWarnIDs(ctx, userID); err != nil {
		s.logger.Warn("failed to clear PM warning IDs", zap.Int64("user_id", userID), zap.Error(err))
	}
	s.publishEvent("approve", userID, "", 0, reason, true, "")
	return nil
}

func (s *Service) Disapprove(ctx context.Context, userID int64) error {
	unlock := s.lockUserStatus(userID)
	defer unlock()
	if s.repo == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusPending, "approval revoked", nil); err != nil {
		return err
	}
	s.approvedCache.Delete(userID)
	if err := s.clearWarnIDs(ctx, userID); err != nil {
		s.logger.Warn("failed to clear PM warning IDs", zap.Int64("user_id", userID), zap.Error(err))
	}
	if err := s.repo.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset PM warnings after disapproval", zap.Int64("user_id", userID), zap.Error(err))
	}
	s.publishEvent("disapprove", userID, "", 0, "approval revoked", true, "")
	return nil
}

func (s *Service) Unblock(ctx context.Context, peer tg.InputPeerClass, userID int64) error {
	unlock := s.lockUserStatus(userID)
	defer unlock()
	if s.repo == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	// Do not publish a pending state while Telegram still blocks the peer.
	if svc := s.getService(); svc != nil {
		if peer == nil {
			peer = s.resolvePeer(userID)
		}
		if err := svc.UnblockUser(ctx, peer); err != nil {
			s.publishEvent("unblock", userID, "", 0, "unblocked by user", false, err.Error())
			return fmt.Errorf("unblock user via Telegram: %w", err)
		}
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusPending, "unblocked by user", nil); err != nil {
		return fmt.Errorf("persist PM unblock: %w", err)
	}
	s.approvedCache.Delete(userID)
	if err := s.clearWarnIDs(ctx, userID); err != nil {
		s.logger.Warn("failed to clear PM warning IDs", zap.Int64("user_id", userID), zap.Error(err))
	}
	if err := s.repo.ResetPMWarn(ctx, userID); err != nil {
		s.logger.Warn("failed to reset PM warnings after unblock", zap.Int64("user_id", userID), zap.Error(err))
	}
	s.publishEvent("unblock", userID, "", 0, "unblocked by user", true, "")
	return nil
}

func (s *Service) Block(ctx context.Context, userID int64, reason string) error {
	return s.BlockWithPeer(ctx, nil, userID, reason)
}

func (s *Service) BlockWithPeer(ctx context.Context, peer tg.InputPeerClass, userID int64, reason string) error {
	unlock := s.lockUserStatus(userID)
	defer unlock()
	return s.blockWithPeerLocked(ctx, peer, userID, reason)
}

func (s *Service) blockWithPeerLocked(ctx context.Context, peer tg.InputPeerClass, userID int64, reason string) error {
	if reason == "" {
		reason = "blocked by user"
	}
	if s.repo == nil {
		return fmt.Errorf("pm permit database is unavailable")
	}
	if err := s.repo.SetPMStatus(ctx, userID, StatusBlocked, reason, nil); err != nil {
		return err
	}
	s.approvedCache.Delete(userID)
	if svc := s.getService(); svc != nil {
		if peer == nil {
			peer = s.resolvePeer(userID)
		}
		if err := svc.BlockUser(ctx, peer); err != nil {
			s.publishEvent("block", userID, "", 0, reason, false, err.Error())
			return fmt.Errorf("block user via Telegram: %w", err)
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

	// Serialize decisions and status transitions for this user only.
	unlock := s.lockUserStatus(senderID)
	defer unlock()

	// 3. Approval check
	approved, err := s.isApprovedLocked(ctx, senderID)
	if err != nil {
		s.logger.Warn("pm permit approval check failed", zap.Error(err), zap.Int64("sender_id", senderID))
		return true, nil
	}
	if approved {
		return false, nil
	}
	if s.repo == nil {
		return true, nil
	}

	// 4. Blocked state handling: SILENT DROP (no reply loop!)
	rec, readErr := s.repo.GetPMRecord(ctx, senderID)
	if readErr != nil {
		s.logger.Warn("PM state check failed closed", zap.Int64("sender_id", senderID), zap.Error(readErr))
		return true, nil
	}
	if rec != nil && rec.Status == StatusBlocked {
		return true, nil
	}

	// 5. Atomically enforce the live cooldown with strictly bounded sender state.
	// When capacity is exhausted by active senders, suppress a new warning
	// instead of admitting an unbounded reply storm.
	if s.warnInCooldown(senderID) {
		return true, nil
	}

	// 6. Warning counter increment
	warnCount, err := s.repo.IncrementPMWarn(ctx, senderID)
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
		if err := s.blockWithPeerLocked(ctx, peer, senderID, "exceeded pm warning threshold"); err != nil {
			s.logger.Warn("failed to apply PM block", zap.Int64("sender_id", senderID), zap.Error(err))
			return true, nil
		}
		msg, err := s.sendTemplate(ctx, svc, peer, pmLimitResponse, pmLimitTemplate, savedresponse.TemplateVars{})
		if err != nil {
			s.logger.Warn("failed to send pm limit reached message", zap.Int64("user_id", senderID), zap.Error(err))
		} else if msg != nil {
			if err := s.addWarnID(ctx, senderID, msg.ID); err != nil {
				s.logger.Warn("PM limit warning ID not persisted", zap.Int64("sender_id", senderID), zap.Error(err))
			}
		}
		return true, nil
	}

	remaining := maxWarns - warnCount
	msg, err := s.sendTemplate(ctx, svc, peer, pmWarningResponse, pmWarningTemplate, savedresponse.TemplateVars{
		Extra: map[string]string{
			"count": strconv.Itoa(warnCount), "limit": strconv.Itoa(maxWarns), "remaining": strconv.Itoa(remaining),
		},
	})
	if err != nil {
		s.logger.Warn("failed to send pm warning message", zap.Int64("user_id", senderID), zap.Error(err))
	} else if msg != nil {
		if err := s.addWarnID(ctx, senderID, msg.ID); err != nil {
			s.logger.Warn("PM warning ID not persisted", zap.Int64("sender_id", senderID), zap.Error(err))
		}
	}
	s.publishEvent("warn", senderID, "", warnCount, fmt.Sprintf("warning %d/%d", warnCount, maxWarns), true, "")
	return true, nil
}

func (s *Service) ListApproved(ctx context.Context, limit, offset int) ([]*PMPermitRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("pm permit database is unavailable")
	}
	return s.repo.ListPMRecords(ctx, StatusApproved, limit, offset)
}

func (s *Service) ListBlocked(ctx context.Context, limit, offset int) ([]*PMPermitRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("pm permit database is unavailable")
	}
	return s.repo.ListPMRecords(ctx, StatusBlocked, limit, offset)
}

func (s *Service) ListPending(ctx context.Context, limit, offset int) ([]*PMPermitRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("pm permit database is unavailable")
	}
	return s.repo.ListPMRecords(ctx, StatusPending, limit, offset)
}

func (s *Service) GetStats(ctx context.Context) (pending, approved, blocked int, err error) {
	if s.repo == nil {
		return 0, 0, 0, fmt.Errorf("pm permit database is unavailable")
	}
	return s.repo.CountPMRecords(ctx)
}
