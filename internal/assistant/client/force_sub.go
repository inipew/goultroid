package client

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"go.uber.org/zap"
)

const (
	forceSubCacheCapacity    = 2048
	forceSubMaxVerifications = 32
	forceSubMemberTTL        = 10 * time.Minute
	forceSubNonMemberTTL     = time.Minute
	forceSubGuidanceCooldown = time.Minute
)

type forceSubDecision struct {
	Allowed             bool
	JoinRequired        bool
	VerificationBlocked bool
	Config              pmrelay.ForceSubConfig
}

type forceSubMembershipGate interface {
	Check(context.Context, int64) (forceSubDecision, error)
}

type forceSubGuidanceLimiter interface {
	GuidanceDue(int64, int64) bool
	ClaimGuidance(int64, int64) bool
}

type forceSubAPI interface {
	ContactsResolveUsername(context.Context, *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error)
	ChannelsGetParticipant(context.Context, *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error)
}

type forceSubCacheEntry struct {
	userID    int64
	member    bool
	expiresAt time.Time
}

type telegramForceSubGate struct {
	policy   pmrelay.ForceSubPolicy
	api      forceSubAPI
	resolver peer.Resolver
	logger   *zap.Logger
	now      func() time.Time

	mu                sync.Mutex
	revision          int64
	channel           *tg.InputChannel
	entries           map[int64]*list.Element
	lru               *list.List
	guidance          map[int64]*list.Element
	guidanceLRU       *list.List
	cacheCapacity     int
	memberTTL         time.Duration
	nonMemberTTL      time.Duration
	guidanceCooldown  time.Duration
	verificationSlots chan struct{}
}

func newTelegramForceSubGate(
	policy pmrelay.ForceSubPolicy,
	api forceSubAPI,
	resolver peer.Resolver,
	logger *zap.Logger,
) *telegramForceSubGate {
	if policy == nil || api == nil || resolver == nil {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &telegramForceSubGate{
		policy:            policy,
		api:               api,
		resolver:          resolver,
		logger:            logger,
		now:               time.Now,
		entries:           make(map[int64]*list.Element, forceSubCacheCapacity),
		lru:               list.New(),
		guidance:          make(map[int64]*list.Element, forceSubCacheCapacity),
		guidanceLRU:       list.New(),
		cacheCapacity:     forceSubCacheCapacity,
		memberTTL:         forceSubMemberTTL,
		nonMemberTTL:      forceSubNonMemberTTL,
		guidanceCooldown:  forceSubGuidanceCooldown,
		verificationSlots: make(chan struct{}, forceSubMaxVerifications),
	}
}

func (g *telegramForceSubGate) resetForRevisionLocked(revision int64) {
	if g.revision == revision {
		return
	}
	g.revision = revision
	g.channel = nil
	g.entries = make(map[int64]*list.Element, g.cacheCapacity)
	g.lru.Init()
	g.guidance = make(map[int64]*list.Element, g.cacheCapacity)
	g.guidanceLRU.Init()
}

func (g *telegramForceSubGate) getCached(userID, revision int64, now time.Time) (bool, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetForRevisionLocked(revision)
	elem, ok := g.entries[userID]
	if !ok {
		return false, false
	}
	entry := elem.Value.(forceSubCacheEntry)
	if !now.Before(entry.expiresAt) {
		delete(g.entries, userID)
		g.lru.Remove(elem)
		return false, false
	}
	g.lru.MoveToBack(elem)
	return entry.member, true
}

func (g *telegramForceSubGate) putCached(userID, revision int64, member bool, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetForRevisionLocked(revision)

	ttl := g.nonMemberTTL
	if member {
		ttl = g.memberTTL
	}
	entry := forceSubCacheEntry{userID: userID, member: member, expiresAt: now.Add(ttl)}
	if elem, ok := g.entries[userID]; ok {
		elem.Value = entry
		g.lru.MoveToBack(elem)
		return
	}
	if g.cacheCapacity <= 0 {
		return
	}
	if len(g.entries) >= g.cacheCapacity {
		if front := g.lru.Front(); front != nil {
			evicted := front.Value.(forceSubCacheEntry)
			delete(g.entries, evicted.userID)
			g.lru.Remove(front)
		}
	}
	elem := g.lru.PushBack(entry)
	g.entries[userID] = elem
}

type forceSubGuidanceEntry struct {
	userID    int64
	expiresAt time.Time
}

func (g *telegramForceSubGate) GuidanceDue(userID, revision int64) bool {
	if g == nil || userID <= 0 || revision <= 0 {
		return false
	}
	now := g.now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetForRevisionLocked(revision)
	elem, ok := g.guidance[userID]
	if !ok {
		return true
	}
	entry := elem.Value.(forceSubGuidanceEntry)
	if !now.Before(entry.expiresAt) {
		delete(g.guidance, userID)
		g.guidanceLRU.Remove(elem)
		return true
	}
	return false
}

func (g *telegramForceSubGate) ClaimGuidance(userID, revision int64) bool {
	if g == nil || userID <= 0 || revision <= 0 {
		return false
	}
	now := g.now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetForRevisionLocked(revision)

	if elem, ok := g.guidance[userID]; ok {
		entry := elem.Value.(forceSubGuidanceEntry)
		if now.Before(entry.expiresAt) {
			g.guidanceLRU.MoveToBack(elem)
			return false
		}
		delete(g.guidance, userID)
		g.guidanceLRU.Remove(elem)
	}
	if g.cacheCapacity <= 0 {
		return false
	}
	if len(g.guidance) >= g.cacheCapacity {
		if front := g.guidanceLRU.Front(); front != nil {
			evicted := front.Value.(forceSubGuidanceEntry)
			delete(g.guidance, evicted.userID)
			g.guidanceLRU.Remove(front)
		}
	}
	entry := forceSubGuidanceEntry{
		userID:    userID,
		expiresAt: now.Add(g.guidanceCooldown),
	}
	elem := g.guidanceLRU.PushBack(entry)
	g.guidance[userID] = elem
	return true
}

func (g *telegramForceSubGate) resolveChannel(
	ctx context.Context,
	config pmrelay.ForceSubConfig,
) (*tg.InputChannel, error) {
	g.mu.Lock()
	g.resetForRevisionLocked(config.Revision)
	if g.channel != nil {
		channel := *g.channel
		g.mu.Unlock()
		return &channel, nil
	}
	g.mu.Unlock()

	resolved, err := g.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: config.ChannelUsername,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: resolve @%s: %v", pmrelay.ErrForceSubVerify, config.ChannelUsername, err)
	}
	if resolved == nil {
		return nil, fmt.Errorf("%w: resolve @%s returned no peer", pmrelay.ErrForceSubVerify, config.ChannelUsername)
	}
	peerChannel, ok := resolved.Peer.(*tg.PeerChannel)
	if !ok || peerChannel.ChannelID <= 0 {
		return nil, fmt.Errorf("%w: @%s is not a channel/supergroup", pmrelay.ErrForceSubVerify, config.ChannelUsername)
	}
	var input *tg.InputChannel
	for _, chat := range resolved.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok || channel.ID != peerChannel.ChannelID || channel.AccessHash == 0 {
			continue
		}
		input = &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
		break
	}
	if input == nil {
		return nil, fmt.Errorf("%w: channel @%s access hash unavailable", pmrelay.ErrForceSubVerify, config.ChannelUsername)
	}

	g.mu.Lock()
	g.resetForRevisionLocked(config.Revision)
	if g.channel == nil {
		cached := *input
		g.channel = &cached
	}
	channel := *g.channel
	g.mu.Unlock()
	return &channel, nil
}

func participantIsMember(participant tg.ChannelParticipantClass) bool {
	switch value := participant.(type) {
	case nil:
		return false
	case *tg.ChannelParticipantLeft:
		return false
	case *tg.ChannelParticipantBanned:
		// Decoded Telegram values set both the flags and exported field. Reading
		// the field too keeps classification correct for direct test/embedding
		// construction before SetFlags has been called.
		return !(value.Left || value.GetLeft())
	default:
		return true
	}
}

func (g *telegramForceSubGate) verifyMembership(
	ctx context.Context,
	config pmrelay.ForceSubConfig,
	userID int64,
) (bool, error) {
	if g.verificationSlots == nil {
		return false, fmt.Errorf("%w: membership verifier is unavailable", pmrelay.ErrForceSubVerify)
	}
	select {
	case g.verificationSlots <- struct{}{}:
		defer func() { <-g.verificationSlots }()
	default:
		return false, fmt.Errorf("%w: membership verification capacity saturated", pmrelay.ErrForceSubVerify)
	}
	channel, err := g.resolveChannel(ctx, config)
	if err != nil {
		return false, err
	}
	peerClass, err := g.resolver.Resolve(ctx, &tg.PeerUser{UserID: userID}, userID, tg.Entities{})
	if err != nil {
		return false, fmt.Errorf("%w: resolve visitor %d: %v", pmrelay.ErrForceSubVerify, userID, err)
	}
	participant, err := g.api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
		Channel:     channel,
		Participant: peerClass,
	})
	if err != nil {
		if tgerr.Is(err, "USER_NOT_PARTICIPANT") {
			return false, nil
		}
		return false, fmt.Errorf("%w: channels.getParticipant(%d,@%s): %v",
			pmrelay.ErrForceSubVerify, userID, config.ChannelUsername, err)
	}
	if participant == nil {
		return false, fmt.Errorf("%w: empty channels.getParticipant response", pmrelay.ErrForceSubVerify)
	}
	return participantIsMember(participant.Participant), nil
}

func (g *telegramForceSubGate) Check(ctx context.Context, userID int64) (forceSubDecision, error) {
	if g == nil || g.policy == nil || g.api == nil || g.resolver == nil || userID <= 0 {
		return forceSubDecision{}, pmrelay.ErrUnavailable
	}
	config, err := g.policy.ForceSubConfig(ctx)
	if err != nil {
		return forceSubDecision{}, err
	}
	if !config.Enabled {
		return forceSubDecision{Allowed: true, Config: config}, nil
	}

	now := g.now().UTC()
	if member, ok := g.getCached(userID, config.Revision, now); ok {
		if member {
			return forceSubDecision{Allowed: true, Config: config}, nil
		}
		return forceSubDecision{JoinRequired: true, Config: config}, nil
	}

	member, verifyErr := g.verifyMembership(ctx, config, userID)
	if verifyErr != nil {
		if config.FailureMode == pmrelay.ForceSubFailOpen {
			g.logger.Warn("assistant: force-sub verification failed open",
				zap.Int64("user_id", userID),
				zap.String("channel", config.ChannelUsername),
				zap.Int64("revision", config.Revision),
				zap.Error(verifyErr),
			)
			return forceSubDecision{Allowed: true, Config: config}, nil
		}
		return forceSubDecision{
			VerificationBlocked: true,
			Config:              config,
		}, verifyErr
	}

	g.putCached(userID, config.Revision, member, now)
	if member {
		return forceSubDecision{Allowed: true, Config: config}, nil
	}
	return forceSubDecision{JoinRequired: true, Config: config}, nil
}

type forceSubGuidanceTransport interface {
	SendForceSubGuidance(context.Context, int64, pmrelay.ForceSubConfig, bool) error
}

func (t *telegramRelayVisitorTransport) SendForceSubGuidance(
	ctx context.Context,
	userID int64,
	config pmrelay.ForceSubConfig,
	verificationUnavailable bool,
) error {
	if t == nil || t.interaction == nil || userID <= 0 {
		return pmrelay.ErrUnavailable
	}
	peerClass, err := t.resolveUser(ctx, userID)
	if err != nil {
		return err
	}
	channel := core.EscapeHTML("@" + config.ChannelUsername)
	text := "🔒 <b>Membership required</b>\n\n" +
		"Join " + channel + " first, then send your message again."
	if verificationUnavailable {
		text = "⚠️ <b>Membership verification unavailable</b>\n\n" +
			"This relay uses fail-closed verification. Join " + channel +
			" and retry shortly."
	}
	markup := &tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardButtonRow{{
			Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonURL{Text: "Join channel", URL: config.JoinURL},
			},
		}},
	}
	_, err = t.interaction.SendMessage(ctx, peerClass, text, markup)
	if err != nil {
		return fmt.Errorf("send force-sub guidance: %w", err)
	}
	return nil
}

var _ forceSubMembershipGate = (*telegramForceSubGate)(nil)
var _ forceSubGuidanceLimiter = (*telegramForceSubGate)(nil)
var _ forceSubGuidanceTransport = (*telegramRelayVisitorTransport)(nil)
