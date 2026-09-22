package pmrelay

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrDisabled       = errors.New("pmrelay: relay disabled")
	ErrPreparedStale  = errors.New("pmrelay: prepared ingress stale")
	ErrMappingExpired = errors.New("pmrelay: mapping expired")
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
// submission. ExecutePrepared must revalidate it immediately before feature
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

// Ingress is the narrow Assistant-facing PM relay contract. Prepare methods are
// read-only. ExecutePrepared is the post-admission authority and revalidates all
// mutable policy/mapping state before any later phase may attach delivery.
type Ingress interface {
	PrepareVisitor(context.Context, IngressMessage) (PreparedIngress, bool, error)
	PrepareOwnerReply(context.Context, IngressMessage) (PreparedIngress, bool, error)
	ExecutePrepared(context.Context, PreparedIngress) error
}

// Service owns relay admission policy and durable reply-routing revalidation.
// P6-B deliberately has no Telegram transport dependency.
type Service struct {
	repo    Repository
	ownerID int64
	now     func() time.Time

	mu       sync.RWMutex
	enabled  bool
	revision uint64
}

func NewService(repo Repository, ownerID int64) *Service {
	return &Service{
		repo:     repo,
		ownerID:  ownerID,
		now:      time.Now,
		revision: 1,
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

// ExecutePrepared is the only post-admission execution entry point in P6-B.
// Later phases may append durable delivery after this revalidation succeeds.
func (s *Service) ExecutePrepared(ctx context.Context, prepared PreparedIngress) error {
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

var _ Ingress = (*Service)(nil)
