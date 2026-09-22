package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	broadcastsvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/pmrelay"
)

type assistantAudienceTargetSource struct {
	registry         pmrelay.AudienceRegistry
	snapshot         pmrelay.AudienceSnapshot
	cursor           int64
	yielded          int
	missingRemaining int
	mu               sync.Mutex
}

func newAssistantAudienceTargetSource(
	ctx context.Context,
	registry pmrelay.AudienceRegistry,
) (*assistantAudienceTargetSource, error) {
	if registry == nil {
		return nil, pmrelay.ErrUnavailable
	}
	snapshot, err := registry.SnapshotAudience(ctx)
	if err != nil {
		return nil, err
	}
	return &assistantAudienceTargetSource{registry: registry, snapshot: snapshot}, nil
}

func (s *assistantAudienceTargetSource) Total() int {
	if s == nil {
		return 0
	}
	return s.snapshot.Total
}

func (s *assistantAudienceTargetSource) Next(
	ctx context.Context,
	limit int,
) ([]tg.InputPeerClass, bool, error) {
	if s == nil || s.registry == nil {
		return nil, true, pmrelay.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.missingRemaining > 0 {
		count := s.missingRemaining
		if limit > 0 && count > limit {
			count = limit
		}
		targets := make([]tg.InputPeerClass, count)
		s.missingRemaining -= count
		return targets, s.missingRemaining == 0, nil
	}
	if s.cursor >= s.snapshot.MaxSequence {
		return nil, true, nil
	}

	members, next, err := s.registry.ListAudienceSnapshot(ctx, s.snapshot, s.cursor, limit)
	if err != nil {
		return nil, false, err
	}
	if next < s.cursor {
		return nil, false, fmt.Errorf("assistant audience keyset moved backwards")
	}
	targets := make([]tg.InputPeerClass, 0, len(members))
	for _, member := range members {
		if member.UserID > 0 {
			targets = append(targets, &tg.InputPeerUser{UserID: member.UserID})
			s.yielded++
		}
	}
	s.cursor = next
	reachedEnd := len(members) == 0 || s.cursor >= s.snapshot.MaxSequence
	if reachedEnd && s.yielded < s.snapshot.Total {
		s.missingRemaining = s.snapshot.Total - s.yielded
		count := s.missingRemaining
		if limit > 0 && count > limit {
			count = limit
		}
		targets = append(targets, make([]tg.InputPeerClass, count)...)
		s.missingRemaining -= count
		return targets, s.missingRemaining == 0, nil
	}
	return targets, reachedEnd, nil
}

type assistantAudienceBroadcastServicer struct {
	unsupportedTelegramServicer
	client *AssistantClient
}

func (s *assistantAudienceBroadcastServicer) resolve(
	ctx context.Context,
	target tg.InputPeerClass,
) (tg.InputPeerClass, error) {
	if s == nil || s.client == nil || target == nil {
		return nil, core.ErrUnavailable
	}
	if user, ok := target.(*tg.InputPeerUser); ok {
		if user.UserID <= 0 {
			return nil, core.ErrInvalidArgs
		}
		if user.AccessHash != 0 {
			return user, nil
		}
		s.client.mu.RLock()
		resolver := s.client.resolver
		s.client.mu.RUnlock()
		if resolver == nil {
			return nil, core.ErrUnavailable
		}
		return resolver.Resolve(ctx, &tg.PeerUser{UserID: user.UserID}, user.UserID, tg.Entities{})
	}
	return target, nil
}

func (s *assistantAudienceBroadcastServicer) interaction() *assistantinteraction.ClientInteraction {
	if s == nil || s.client == nil {
		return nil
	}
	s.client.mu.RLock()
	defer s.client.mu.RUnlock()
	return s.client.interaction
}

func (s *assistantAudienceBroadcastServicer) SendMessage(
	ctx context.Context,
	target tg.InputPeerClass,
	text string,
) (*tg.Message, error) {
	peer, err := s.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	inter := s.interaction()
	if inter == nil {
		return nil, core.ErrUnavailable
	}
	return inter.SendMessage(ctx, peer, text, nil)
}

func (s *assistantAudienceBroadcastServicer) SendMedia(
	ctx context.Context,
	target tg.InputPeerClass,
	mediaType, filePath, caption string,
) (*tg.Message, error) {
	peer, err := s.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	inter := s.interaction()
	if inter == nil {
		return nil, core.ErrUnavailable
	}
	return inter.SendMedia(ctx, peer, mediaType, filePath, caption)
}

// BroadcastAudience snapshots current Assistant audience membership and streams
// it through the existing bounded Broadcast service. The request must not carry
// its own target/source/sender; those are owned by this Assistant data plane.
func (c *AssistantClient) BroadcastAudience(
	ctx context.Context,
	req broadcastsvc.BroadcastRequest,
) (*broadcastsvc.BroadcastReport, error) {
	if c == nil {
		return nil, core.ErrUnavailable
	}
	if len(req.Targets) != 0 || req.TargetSource != nil || req.Sender != nil {
		return nil, fmt.Errorf("%w: assistant audience broadcast owns target selection and sender", core.ErrInvalidArgs)
	}
	c.mu.RLock()
	registry := c.audience
	service := c.broadcast
	c.mu.RUnlock()
	if registry == nil || service == nil {
		return nil, core.ErrUnavailable
	}
	source, err := newAssistantAudienceTargetSource(ctx, registry)
	if err != nil {
		return nil, err
	}
	req.TargetSource = source
	req.Sender = &assistantAudienceBroadcastServicer{client: c}
	return service.Broadcast(ctx, req)
}
