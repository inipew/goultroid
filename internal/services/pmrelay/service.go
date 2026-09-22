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

// Service owns relay admission policy, durable visitor delivery state, and
// reply-routing revalidation. Telegram remains behind the VisitorTransport port.
type Service struct {
	repo    Repository
	ownerID int64
	now     func() time.Time

	mu       sync.RWMutex
	enabled  bool
	revision uint64

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

func (s *Service) policySnapshot() (bool, uint64) {
	if s == nil {
		return false, 0
	}
	s.mu.RLock()
	enabled, revision := s.enabled, s.revision
	s.mu.RUnlock()
	return enabled, revision
}

func (s *Service) PrepareVisitor(_ context.Context, message IngressMessage) (PreparedIngress, bool, error) {
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

// RevalidatePrepared is the only post-admission execution entry point in P6-B.
// Later phases may append durable delivery after this revalidation succeeds.
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
		return nil

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
		return nil
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

func (s *Service) touchRelayAudience(ctx context.Context, visitorID int64, seenAt, now time.Time) error {
	touch := AudienceTouch{UserID: visitorID, Source: AudienceSourceRelay, SeenAt: seenAt}
	if _, err := s.repo.TouchAudience(ctx, touch); !errors.Is(err, ErrAudienceCapacity) {
		return err
	}
	if _, err := s.repo.PruneAudienceBefore(ctx, now.Add(-DefaultAudienceRetention), 64); err != nil {
		return err
	}
	_, err := s.repo.TouchAudience(ctx, touch)
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

var _ Ingress = (*Service)(nil)
var _ VisitorExecutor = (*Service)(nil)
