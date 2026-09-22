package pmrelay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

var (
	ErrDisabled            = errors.New("pmrelay: relay disabled")
	ErrPreparedStale       = errors.New("pmrelay: prepared ingress stale")
	ErrMappingExpired      = errors.New("pmrelay: mapping expired")
	ErrUnsupportedDelivery = errors.New("pmrelay: unsupported delivery")
)

// IngressMessage is the transport-neutral message identity needed to classify
// Assistant PM relay work. Text/Telegram payloads remain owned by the transport
// adapter and are intentionally not retained here.
type IngressMessage struct {
	SenderID         int64
	ChatID           int64
	MessageID        int
	ReplyToMessageID int
}

func (m IngressMessage) valid() bool {
	return m.SenderID > 0 && m.ChatID > 0 && m.MessageID > 0
}

// PreparedIngress is immutable admission state produced before TaskEngine
// submission. RevalidatePrepared must revalidate it immediately before feature
// execution.
type PreparedIngress struct {
	direction        DeliveryDirection
	sourceChatID     int64
	sourceMessageID  int
	visitorUserID    int64
	targetChatID     int64
	ownerChatID      int64
	replyToMessageID int
	mapping          Mapping
	policyRevision   uint64
}

func (p PreparedIngress) Direction() DeliveryDirection { return p.direction }
func (p PreparedIngress) SourceChatID() int64          { return p.sourceChatID }
func (p PreparedIngress) SourceMessageID() int         { return p.sourceMessageID }
func (p PreparedIngress) VisitorUserID() int64         { return p.visitorUserID }
func (p PreparedIngress) TargetChatID() int64          { return p.targetChatID }

type VisitorForward struct {
	SourceChatID    int64
	SourceMessageID int
	TargetChatID    int64
	RandomID        int64
}

type VisitorTransport interface {
	ForwardVisitor(context.Context, VisitorForward) (int, error)
}

type OwnerSend struct {
	SourceChatID    int64
	SourceMessageID int
	TargetChatID    int64
	RandomID        int64
}

type OwnerTransport interface {
	SendOwnerReply(context.Context, OwnerSend) (int, error)
}

// Ingress is the narrow Assistant-facing PM relay contract. Prepare methods are
// read-only. RevalidatePrepared is the post-admission authority and revalidates
// mutable policy/mapping state before delivery execution.
type Ingress interface {
	PrepareVisitor(context.Context, IngressMessage) (PreparedIngress, bool, error)
	PrepareOwnerReply(context.Context, IngressMessage) (PreparedIngress, bool, error)
	RevalidatePrepared(context.Context, PreparedIngress) error
}

type VisitorExecutor interface {
	ExecuteVisitor(context.Context, PreparedIngress, VisitorTransport) error
}

type OwnerExecutor interface {
	ExecuteOwner(context.Context, PreparedIngress, OwnerTransport) error
}

type AudienceRegistry interface {
	TouchAudience(context.Context, AudienceTouch) (AudienceMember, error)
	SnapshotAudience(context.Context) (AudienceSnapshot, error)
	ListAudienceSnapshot(context.Context, AudienceSnapshot, int64, int) ([]AudienceMember, int64, error)
}

type ForceSubPolicy interface {
	ForceSubConfig(context.Context) (ForceSubConfig, error)
}

type ControlStatus struct {
	Enabled    bool
	Mappings   int
	Deliveries int
	Audience   int
	Blocked    int
}

type VisitorDetails struct {
	Mapping  Mapping
	Audience *AudienceMember
	Block    *VisitorBlock
}

// Service owns relay admission/block policy, durable delivery state, and
// reply-routing revalidation. Telegram remains behind narrow transport ports.
type Service struct {
	repo    Repository
	ownerID int64
	now     func() time.Time

	mu             sync.RWMutex
	enabled        bool
	revision       uint64
	forceSubCached *ForceSubConfig

	randomID func() (int64, error)
	claimID  func() (string, error)
}

func NewService(repo Repository, ownerID int64) *Service {
	return &Service{
		repo:     repo,
		ownerID:  ownerID,
		now:      time.Now,
		revision: 1,
		randomID: newRandomID,
		claimID:  newClaimID,
	}
}

func (s *Service) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.enabled != enabled {
		s.enabled = enabled
		s.revision++
	}
	s.mu.Unlock()
}

func (s *Service) IsEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	enabled := s.enabled
	s.mu.RUnlock()
	return enabled
}

func (s *Service) TouchAudience(ctx context.Context, touch AudienceTouch) (AudienceMember, error) {
	if s == nil || s.repo == nil {
		return AudienceMember{}, ErrUnavailable
	}
	normalized, err := touch.Normalize()
	if err != nil {
		return AudienceMember{}, err
	}
	member, err := s.repo.TouchAudience(ctx, normalized)
	if !errors.Is(err, ErrAudienceCapacity) {
		return member, err
	}

	now := s.now().UTC()
	if _, pruneErr := s.repo.PruneAudienceBefore(ctx, now.Add(-DefaultAudienceRetention), 64); pruneErr != nil {
		return AudienceMember{}, errors.Join(err, pruneErr)
	}
	return s.repo.TouchAudience(ctx, normalized)
}

func (s *Service) SnapshotAudience(ctx context.Context) (AudienceSnapshot, error) {
	if s == nil || s.repo == nil {
		return AudienceSnapshot{}, ErrUnavailable
	}
	return s.repo.SnapshotAudience(ctx)
}

func (s *Service) ListAudienceSnapshot(
	ctx context.Context,
	snapshot AudienceSnapshot,
	afterSequence int64,
	limit int,
) ([]AudienceMember, int64, error) {
	if s == nil || s.repo == nil {
		return nil, afterSequence, ErrUnavailable
	}
	return s.repo.ListAudienceSnapshot(ctx, snapshot, afterSequence, limit)
}

func (s *Service) ForceSubConfig(ctx context.Context) (ForceSubConfig, error) {
	if s == nil || s.repo == nil {
		return ForceSubConfig{}, ErrUnavailable
	}
	s.mu.RLock()
	if s.forceSubCached != nil {
		config := *s.forceSubCached
		s.mu.RUnlock()
		return config, nil
	}
	s.mu.RUnlock()

	config, err := s.repo.GetForceSubConfig(ctx)
	if err != nil {
		return ForceSubConfig{}, err
	}
	s.mu.Lock()
	if s.forceSubCached == nil || s.forceSubCached.Revision <= config.Revision {
		cached := config
		s.forceSubCached = &cached
	}
	config = *s.forceSubCached
	s.mu.Unlock()
	return config, nil
}

func (s *Service) ConfigureForceSub(
	ctx context.Context,
	enabled bool,
	channelUsername string,
	joinURL string,
	failureMode ForceSubFailureMode,
) (ForceSubConfig, error) {
	if s == nil || s.repo == nil {
		return ForceSubConfig{}, ErrUnavailable
	}
	current, err := s.ForceSubConfig(ctx)
	if err != nil {
		return ForceSubConfig{}, err
	}
	next := ForceSubConfig{
		Enabled:         enabled,
		ChannelUsername: channelUsername,
		JoinURL:         joinURL,
		FailureMode:     failureMode,
		Revision:        current.Revision + 1,
		UpdatedAt:       s.now().UTC(),
	}
	if !enabled {
		next.FailureMode = ForceSubFailClosed
	}
	normalized, err := next.Normalize()
	if err != nil {
		return ForceSubConfig{}, err
	}
	updated, err := s.repo.UpdateForceSubConfig(ctx, current.Revision, normalized)
	if err != nil {
		if errors.Is(err, ErrForceSubConfigConflict) {
			s.mu.Lock()
			s.forceSubCached = nil
			s.mu.Unlock()
		}
		return ForceSubConfig{}, err
	}
	s.mu.Lock()
	cached := updated
	s.forceSubCached = &cached
	s.mu.Unlock()
	return updated, nil
}

func (s *Service) checkVisitorAllowed(ctx context.Context, visitorID int64) error {
	if s == nil || s.repo == nil {
		return ErrUnavailable
	}
	if visitorID <= 0 {
		return ErrInvalidBlock
	}
	_, err := s.repo.GetVisitorBlock(ctx, visitorID)
	switch {
	case err == nil:
		return ErrVisitorBlocked
	case errors.Is(err, ErrBlockNotFound):
		return nil
	default:
		return err
	}
}

func (s *Service) Status(ctx context.Context) (ControlStatus, error) {
	if s == nil || s.repo == nil {
		return ControlStatus{}, ErrUnavailable
	}
	status := ControlStatus{Enabled: s.IsEnabled()}
	var err error
	if status.Mappings, err = s.repo.CountMappings(ctx); err != nil {
		return ControlStatus{}, err
	}
	if status.Deliveries, err = s.repo.CountDeliveries(ctx); err != nil {
		return ControlStatus{}, err
	}
	if status.Audience, err = s.repo.CountAudience(ctx); err != nil {
		return ControlStatus{}, err
	}
	if status.Blocked, err = s.repo.CountVisitorBlocks(ctx); err != nil {
		return ControlStatus{}, err
	}
	return status, nil
}

func (s *Service) BlockVisitor(ctx context.Context, visitorID int64, reason string) (VisitorBlock, error) {
	if s == nil || s.repo == nil {
		return VisitorBlock{}, ErrUnavailable
	}
	if visitorID <= 0 || visitorID == s.ownerID {
		return VisitorBlock{}, ErrInvalidBlock
	}
	return s.repo.SetVisitorBlock(ctx, VisitorBlock{
		VisitorUserID: visitorID,
		BlockedAt:     s.now().UTC(),
		Reason:        reason,
	})
}

func (s *Service) UnblockVisitor(ctx context.Context, visitorID int64) (bool, error) {
	if s == nil || s.repo == nil {
		return false, ErrUnavailable
	}
	if visitorID <= 0 || visitorID == s.ownerID {
		return false, ErrInvalidBlock
	}
	return s.repo.DeleteVisitorBlock(ctx, visitorID)
}

func (s *Service) ListVisitorBlocks(ctx context.Context, afterUserID int64, limit int) ([]VisitorBlock, error) {
	if s == nil || s.repo == nil {
		return nil, ErrUnavailable
	}
	return s.repo.ListVisitorBlocks(ctx, afterUserID, limit)
}

func (s *Service) VisitorDetails(ctx context.Context, ownerMessageID int) (VisitorDetails, error) {
	if s == nil || s.repo == nil || s.ownerID <= 0 {
		return VisitorDetails{}, ErrUnavailable
	}
	if ownerMessageID <= 0 {
		return VisitorDetails{}, ErrInvalidMapping
	}
	mapping, err := s.repo.GetMapping(ctx, s.ownerID, ownerMessageID)
	if err != nil {
		return VisitorDetails{}, err
	}
	if mapping.Expired(s.now().UTC()) {
		return VisitorDetails{}, ErrMappingExpired
	}
	details := VisitorDetails{Mapping: mapping}
	if member, audienceErr := s.repo.GetAudience(ctx, mapping.VisitorUserID); audienceErr == nil {
		details.Audience = &member
	} else if !errors.Is(audienceErr, ErrAudienceNotFound) {
		return VisitorDetails{}, audienceErr
	}
	if block, blockErr := s.repo.GetVisitorBlock(ctx, mapping.VisitorUserID); blockErr == nil {
		details.Block = &block
	} else if !errors.Is(blockErr, ErrBlockNotFound) {
		return VisitorDetails{}, blockErr
	}
	return details, nil
}

func (s *Service) policySnapshot() (bool, uint64) {
	if s == nil {
		return false, 0
	}
	s.mu.RLock()
	enabled, revision := s.enabled, s.revision
	s.mu.RUnlock()
	return enabled, revision
}

func (s *Service) PrepareVisitor(ctx context.Context, message IngressMessage) (PreparedIngress, bool, error) {
	if s == nil || s.ownerID <= 0 || !message.valid() {
		return PreparedIngress{}, false, nil
	}
	enabled, revision := s.policySnapshot()
	if !enabled {
		return PreparedIngress{}, false, nil
	}
	if message.SenderID == s.ownerID {
		return PreparedIngress{}, false, nil
	}
	// Assistant private-chat classification happens in the transport adapter.
	// Requiring chat==sender here prevents a caller from accidentally using this
	// visitor fallback for a group/channel identity.
	if message.ChatID != message.SenderID {
		return PreparedIngress{}, false, nil
	}
	if err := s.checkVisitorAllowed(ctx, message.SenderID); err != nil {
		if errors.Is(err, ErrVisitorBlocked) {
			// Blocked visitors are a normal policy outcome. Consume no task
			// capacity and emit no noisy error for ordinary blocked traffic.
			return PreparedIngress{}, false, nil
		}
		// Repository uncertainty fails closed instead of forwarding.
		return PreparedIngress{}, true, err
	}
	return PreparedIngress{
		direction:       DeliveryVisitorToOwner,
		sourceChatID:    message.ChatID,
		sourceMessageID: message.MessageID,
		visitorUserID:   message.SenderID,
		targetChatID:    s.ownerID,
		ownerChatID:     s.ownerID,
		policyRevision:  revision,
	}, true, nil
}

func (s *Service) PrepareOwnerReply(ctx context.Context, message IngressMessage) (PreparedIngress, bool, error) {
	if s == nil || s.ownerID <= 0 || !message.valid() || message.ReplyToMessageID <= 0 {
		return PreparedIngress{}, false, nil
	}
	enabled, revision := s.policySnapshot()
	if !enabled {
		return PreparedIngress{}, false, nil
	}
	if message.SenderID != s.ownerID || message.ChatID != s.ownerID {
		return PreparedIngress{}, false, nil
	}
	if s.repo == nil {
		return PreparedIngress{}, true, ErrUnavailable
	}

	mapping, err := s.repo.GetMapping(ctx, message.ChatID, message.ReplyToMessageID)
	if err != nil {
		if errors.Is(err, ErrMappingNotFound) {
			return PreparedIngress{}, false, nil
		}
		// An owner reply that cannot be classified because durable state is
		// unavailable must not fall through into a generic AwaitInput handler.
		return PreparedIngress{}, true, err
	}
	now := s.now().UTC()
	if mapping.Expired(now) {
		return PreparedIngress{}, true, ErrMappingExpired
	}
	if err := s.checkVisitorAllowed(ctx, mapping.VisitorUserID); err != nil {
		return PreparedIngress{}, true, err
	}
	return PreparedIngress{
		direction:        DeliveryOwnerToVisitor,
		sourceChatID:     message.ChatID,
		sourceMessageID:  message.MessageID,
		visitorUserID:    mapping.VisitorUserID,
		targetChatID:     mapping.VisitorUserID,
		ownerChatID:      mapping.OwnerChatID,
		replyToMessageID: message.ReplyToMessageID,
		mapping:          mapping,
		policyRevision:   revision,
	}, true, nil
}

// RevalidatePrepared is the post-admission authority fence. It rechecks the
// current relay policy, durable visitor block policy, and reply mapping before
// either delivery direction may perform Telegram transport.
func (s *Service) RevalidatePrepared(ctx context.Context, prepared PreparedIngress) error {
	if s == nil || s.ownerID <= 0 {
		return ErrUnavailable
	}
	enabled, revision := s.policySnapshot()
	if !enabled {
		return ErrDisabled
	}
	if prepared.policyRevision == 0 || prepared.policyRevision != revision {
		return ErrPreparedStale
	}

	switch prepared.direction {
	case DeliveryVisitorToOwner:
		if prepared.sourceChatID <= 0 ||
			prepared.sourceMessageID <= 0 ||
			prepared.visitorUserID <= 0 ||
			prepared.sourceChatID != prepared.visitorUserID ||
			prepared.targetChatID != s.ownerID ||
			prepared.ownerChatID != s.ownerID {
			return ErrPreparedStale
		}
		return s.checkVisitorAllowed(ctx, prepared.visitorUserID)

	case DeliveryOwnerToVisitor:
		if s.repo == nil ||
			prepared.sourceChatID != s.ownerID ||
			prepared.ownerChatID != s.ownerID ||
			prepared.replyToMessageID <= 0 ||
			prepared.visitorUserID <= 0 ||
			prepared.targetChatID != prepared.visitorUserID {
			return ErrPreparedStale
		}
		current, err := s.repo.GetMapping(ctx, prepared.ownerChatID, prepared.replyToMessageID)
		if err != nil {
			if errors.Is(err, ErrMappingNotFound) {
				return ErrPreparedStale
			}
			return err
		}
		if current.Expired(s.now().UTC()) || !sameMappingIdentity(current, prepared.mapping) {
			return ErrPreparedStale
		}
		return s.checkVisitorAllowed(ctx, prepared.visitorUserID)
	default:
		return ErrPreparedStale
	}
}

func newRandomID() (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return 0, fmt.Errorf("pmrelay: generate random id: %w", err)
	}
	id := n.Int64()
	if id == 0 {
		id = 1
	}
	return id, nil
}

func newClaimID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("pmrelay: generate claim id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Service) ensureDelivery(ctx context.Context, intent DeliveryIntent, now time.Time) (DeliveryIntent, error) {
	delivery, err := s.repo.EnsureDelivery(ctx, intent)
	if !errors.Is(err, ErrDeliveryCapacity) {
		return delivery, err
	}
	if _, pruneErr := s.repo.PruneExpiredDeliveries(ctx, now, 64); pruneErr != nil {
		return DeliveryIntent{}, errors.Join(err, pruneErr)
	}
	return s.repo.EnsureDelivery(ctx, intent)
}

func (s *Service) ensureMapping(ctx context.Context, mapping Mapping, now time.Time) error {
	if _, err := s.repo.EnsureMapping(ctx, mapping); !errors.Is(err, ErrMappingCapacity) {
		return err
	}
	if _, err := s.repo.PruneExpiredMappings(ctx, now, 64); err != nil {
		return err
	}
	_, err := s.repo.EnsureMapping(ctx, mapping)
	return err
}

func (s *Service) touchRelayAudience(ctx context.Context, visitorID int64, seenAt, _ time.Time) error {
	_, err := s.TouchAudience(ctx, AudienceTouch{
		UserID: visitorID,
		Source: AudienceSourceRelay,
		SeenAt: seenAt,
	})
	return err
}

func (s *Service) finalizeVisitorDelivery(ctx context.Context, prepared PreparedIngress, delivery DeliveryIntent, now time.Time) error {
	if !delivery.Completed() || delivery.TargetMessageID <= 0 {
		return ErrDeliveryConflict
	}
	deliveredAt := now
	if delivery.DeliveredAt != nil {
		deliveredAt = delivery.DeliveredAt.UTC()
	}
	if err := s.ensureMapping(ctx, Mapping{
		OwnerChatID:      prepared.targetChatID,
		OwnerMessageID:   delivery.TargetMessageID,
		VisitorUserID:    prepared.visitorUserID,
		VisitorMessageID: prepared.sourceMessageID,
		CreatedAt:        deliveredAt,
		ExpiresAt:        deliveredAt.Add(DefaultMappingRetention),
	}, now); err != nil {
		return fmt.Errorf("pmrelay: persist visitor mapping: %w", err)
	}
	if err := s.touchRelayAudience(ctx, prepared.visitorUserID, deliveredAt, now); err != nil {
		return fmt.Errorf("pmrelay: touch relay audience: %w", err)
	}
	return nil
}

// ExecuteVisitor owns the durable visitor→owner delivery state machine. It is
// called only from an admitted TaskEngine handler. The transport receives the
// durable random_id so a crash after Telegram success but before local commit
// can safely retry the same logical forward.
func (s *Service) ExecuteVisitor(ctx context.Context, prepared PreparedIngress, transport VisitorTransport) error {
	if transport == nil || s == nil || s.repo == nil {
		return ErrUnavailable
	}
	if prepared.direction != DeliveryVisitorToOwner {
		return ErrUnsupportedDelivery
	}
	if err := s.RevalidatePrepared(ctx, prepared); err != nil {
		return err
	}

	now := s.now().UTC()
	randomID, err := s.randomID()
	if err != nil {
		return err
	}
	intent, err := s.ensureDelivery(ctx, DeliveryIntent{
		DeliveryKey: DeliveryKey{
			Direction:       DeliveryVisitorToOwner,
			SourceChatID:    prepared.sourceChatID,
			SourceMessageID: prepared.sourceMessageID,
		},
		TargetChatID: prepared.targetChatID,
		RandomID:     randomID,
		CreatedAt:    now,
		UpdatedAt:    now,
		ExpiresAt:    now.Add(DefaultDeliveryRetention),
	}, now)
	if err != nil {
		return err
	}
	if intent.Expired(now) {
		return ErrDeliveryExpired
	}
	if intent.Completed() {
		return s.finalizeVisitorDelivery(ctx, prepared, intent, now)
	}

	claimID, err := s.claimID()
	if err != nil {
		return err
	}
	claimed, err := s.repo.ClaimDelivery(
		ctx,
		intent.DeliveryKey,
		now,
		claimID,
		now.Add(DeliveryClaimTTL),
	)
	if err != nil {
		if errors.Is(err, ErrDeliveryCompleted) {
			current, getErr := s.repo.GetDelivery(ctx, intent.DeliveryKey)
			if getErr != nil {
				return getErr
			}
			return s.finalizeVisitorDelivery(ctx, prepared, current, now)
		}
		return err
	}

	if err := s.RevalidatePrepared(ctx, prepared); err != nil {
		releaseErr := s.repo.ReleaseDelivery(ctx, claimed.DeliveryKey, claimID, s.now().UTC(), err.Error())
		if releaseErr != nil {
			return errors.Join(err, releaseErr)
		}
		return err
	}

	targetMessageID, sendErr := transport.ForwardVisitor(ctx, VisitorForward{
		SourceChatID:    prepared.sourceChatID,
		SourceMessageID: prepared.sourceMessageID,
		TargetChatID:    prepared.targetChatID,
		RandomID:        claimed.RandomID,
	})
	if sendErr != nil {
		releaseErr := s.repo.ReleaseDelivery(ctx, claimed.DeliveryKey, claimID, s.now().UTC(), sendErr.Error())
		if releaseErr != nil {
			return errors.Join(sendErr, releaseErr)
		}
		return sendErr
	}
	if targetMessageID <= 0 {
		sendErr = ErrDeliveryConflict
		releaseErr := s.repo.ReleaseDelivery(ctx, claimed.DeliveryKey, claimID, s.now().UTC(), sendErr.Error())
		if releaseErr != nil {
			return errors.Join(sendErr, releaseErr)
		}
		return sendErr
	}

	deliveredAt := s.now().UTC()
	committed, err := s.repo.CommitDelivery(ctx, claimed.DeliveryKey, claimID, targetMessageID, deliveredAt)
	if err != nil {
		return err
	}
	return s.finalizeVisitorDelivery(ctx, prepared, committed, deliveredAt)
}

// ExecuteOwner owns the durable owner→visitor delivery state machine. The
// durable mapping captured by PrepareOwnerReply remains the authorization
// authority and is revalidated again immediately before transport. Message
// content stays outside the repository; OwnerTransport reloads the source
// owner message by identity and emits a new bot-authored message.
func (s *Service) ExecuteOwner(ctx context.Context, prepared PreparedIngress, transport OwnerTransport) error {
	if transport == nil || s == nil || s.repo == nil {
		return ErrUnavailable
	}
	if prepared.direction != DeliveryOwnerToVisitor {
		return ErrUnsupportedDelivery
	}
	if err := s.RevalidatePrepared(ctx, prepared); err != nil {
		return err
	}

	now := s.now().UTC()
	randomID, err := s.randomID()
	if err != nil {
		return err
	}
	intent, err := s.ensureDelivery(ctx, DeliveryIntent{
		DeliveryKey: DeliveryKey{
			Direction:       DeliveryOwnerToVisitor,
			SourceChatID:    prepared.sourceChatID,
			SourceMessageID: prepared.sourceMessageID,
		},
		TargetChatID: prepared.targetChatID,
		RandomID:     randomID,
		CreatedAt:    now,
		UpdatedAt:    now,
		ExpiresAt:    now.Add(DefaultDeliveryRetention),
	}, now)
	if err != nil {
		return err
	}
	if intent.Expired(now) {
		return ErrDeliveryExpired
	}
	if intent.Completed() {
		return nil
	}

	claimID, err := s.claimID()
	if err != nil {
		return err
	}
	claimed, err := s.repo.ClaimDelivery(
		ctx,
		intent.DeliveryKey,
		now,
		claimID,
		now.Add(DeliveryClaimTTL),
	)
	if err != nil {
		if errors.Is(err, ErrDeliveryCompleted) {
			return nil
		}
		return err
	}

	if err := s.RevalidatePrepared(ctx, prepared); err != nil {
		releaseErr := s.repo.ReleaseDelivery(ctx, claimed.DeliveryKey, claimID, s.now().UTC(), err.Error())
		if releaseErr != nil {
			return errors.Join(err, releaseErr)
		}
		return err
	}

	targetMessageID, sendErr := transport.SendOwnerReply(ctx, OwnerSend{
		SourceChatID:    prepared.sourceChatID,
		SourceMessageID: prepared.sourceMessageID,
		TargetChatID:    prepared.targetChatID,
		RandomID:        claimed.RandomID,
	})
	if sendErr != nil {
		releaseErr := s.repo.ReleaseDelivery(ctx, claimed.DeliveryKey, claimID, s.now().UTC(), sendErr.Error())
		if releaseErr != nil {
			return errors.Join(sendErr, releaseErr)
		}
		return sendErr
	}
	if targetMessageID <= 0 {
		sendErr = ErrDeliveryConflict
		releaseErr := s.repo.ReleaseDelivery(ctx, claimed.DeliveryKey, claimID, s.now().UTC(), sendErr.Error())
		if releaseErr != nil {
			return errors.Join(sendErr, releaseErr)
		}
		return sendErr
	}

	_, err = s.repo.CommitDelivery(ctx, claimed.DeliveryKey, claimID, targetMessageID, s.now().UTC())
	return err
}

var _ AudienceRegistry = (*Service)(nil)
var _ Ingress = (*Service)(nil)
var _ VisitorExecutor = (*Service)(nil)
var _ OwnerExecutor = (*Service)(nil)
