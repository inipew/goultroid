package groupauth

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
)

const (
	defaultCacheCapacity    = 4096
	defaultMaxVerifications = 32
	defaultPrivilegedTTL    = 30 * time.Second
	defaultMemberTTL        = 2 * time.Minute
	defaultNonMemberTTL     = 30 * time.Second
)

var (
	// ErrGroupRoleUnavailable means required resolver dependencies are missing.
	ErrGroupRoleUnavailable = fmt.Errorf("%w: group role resolver unavailable", core.ErrUnavailable)
	// ErrGroupRoleVerification means Telegram/peer state could not be verified.
	ErrGroupRoleVerification = fmt.Errorf("%w: group role verification failed", core.ErrUnavailable)
	// ErrGroupRoleSaturated is a fail-fast local pressure signal. No RPC is
	// issued when all verification slots are already in use.
	ErrGroupRoleSaturated = fmt.Errorf("%w: group role verification capacity saturated", core.ErrResourceLimit)
	// ErrGroupRoleUnsupported marks a chat kind/peer shape this resolver cannot
	// authoritatively evaluate.
	ErrGroupRoleUnsupported = fmt.Errorf("%w: group role resolution unsupported", core.ErrUnsupported)
)

// API is the Assistant-managed Telegram subset required by role resolution.
type API interface {
	ChannelsGetParticipant(context.Context, *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error)
	MessagesGetFullChat(context.Context, int64) (*tg.MessagesChatFull, error)
}

type cacheKey struct {
	chatID int64
	userID int64
	kind   core.ChatKind
}

type cacheEntry struct {
	key       cacheKey
	snapshot  core.GroupRoleSnapshot
	expiresAt time.Time
}

// TelegramRoleResolver resolves chat-scoped participant roles with a bounded,
// lazy LRU cache. It owns no goroutine, ticker, cleanup loop, or wait queue.
type TelegramRoleResolver struct {
	api   API
	peers peer.Resolver
	now   func() time.Time

	mu                sync.Mutex
	entries           map[cacheKey]*list.Element
	lru               *list.List
	cacheCapacity     int
	privilegedTTL     time.Duration
	memberTTL         time.Duration
	nonMemberTTL      time.Duration
	verificationSlots chan struct{}
}

var _ core.GroupRoleResolver = (*TelegramRoleResolver)(nil)

// NewTelegramRoleResolver creates the P7-B resolver. All network calls flow
// through the supplied managed API.
func NewTelegramRoleResolver(api API, peers peer.Resolver) *TelegramRoleResolver {
	if api == nil || peers == nil {
		return nil
	}
	return &TelegramRoleResolver{
		api:               api,
		peers:             peers,
		now:               time.Now,
		entries:           make(map[cacheKey]*list.Element, defaultCacheCapacity),
		lru:               list.New(),
		cacheCapacity:     defaultCacheCapacity,
		privilegedTTL:     defaultPrivilegedTTL,
		memberTTL:         defaultMemberTTL,
		nonMemberTTL:      defaultNonMemberTTL,
		verificationSlots: make(chan struct{}, defaultMaxVerifications),
	}
}

func validateRequest(request core.GroupRoleRequest) error {
	if request.ChatID <= 0 || request.UserID <= 0 {
		return fmt.Errorf("%w: invalid chat/user coordinates", ErrGroupRoleVerification)
	}
	switch request.Kind {
	case core.ChatKindGroup:
		if _, ok := request.Peer.(*tg.InputPeerChat); !ok {
			return fmt.Errorf("%w: basic group requires InputPeerChat, got %T", ErrGroupRoleVerification, request.Peer)
		}
	case core.ChatKindSupergroup:
		if _, ok := request.Peer.(*tg.InputPeerChannel); !ok {
			return fmt.Errorf("%w: supergroup requires InputPeerChannel, got %T", ErrGroupRoleVerification, request.Peer)
		}
	default:
		return fmt.Errorf("%w: chat kind %q", ErrGroupRoleUnsupported, request.Kind)
	}
	return nil
}

func keyFor(request core.GroupRoleRequest) cacheKey {
	return cacheKey{chatID: request.ChatID, userID: request.UserID, kind: request.Kind}
}

func (r *TelegramRoleResolver) ttlFor(role core.GroupActorRole) time.Duration {
	switch role {
	case core.GroupActorRoleAdministrator, core.GroupActorRoleCreator:
		return r.privilegedTTL
	case core.GroupActorRoleLeft, core.GroupActorRoleBanned:
		return r.nonMemberTTL
	default:
		return r.memberTTL
	}
}

func (r *TelegramRoleResolver) getCached(key cacheKey, now time.Time) (core.GroupRoleSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	elem, ok := r.entries[key]
	if !ok {
		return core.GroupRoleSnapshot{}, false
	}
	entry := elem.Value.(cacheEntry)
	if !now.Before(entry.expiresAt) {
		delete(r.entries, key)
		r.lru.Remove(elem)
		return core.GroupRoleSnapshot{}, false
	}
	r.lru.MoveToBack(elem)
	snapshot := entry.snapshot
	snapshot.Cached = true
	return snapshot, true
}

func (r *TelegramRoleResolver) putCached(key cacheKey, snapshot core.GroupRoleSnapshot) {
	if !snapshot.Principal.Verified || r.cacheCapacity <= 0 {
		return
	}
	now := snapshot.ObservedAt
	if now.IsZero() {
		now = r.now().UTC()
		snapshot.ObservedAt = now
	}
	entry := cacheEntry{
		key:       key,
		snapshot:  snapshot,
		expiresAt: now.Add(r.ttlFor(snapshot.Principal.Role)),
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if elem, ok := r.entries[key]; ok {
		elem.Value = entry
		r.lru.MoveToBack(elem)
		return
	}
	if len(r.entries) >= r.cacheCapacity {
		if front := r.lru.Front(); front != nil {
			evicted := front.Value.(cacheEntry)
			delete(r.entries, evicted.key)
			r.lru.Remove(front)
		}
	}
	elem := r.lru.PushBack(entry)
	r.entries[key] = elem
}

// ResolveGroupRole uses a bounded cached observation when available.
func (r *TelegramRoleResolver) ResolveGroupRole(ctx context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return r.resolve(ctx, request, true)
}

// ResolveGroupRoleFresh bypasses cached authorization state and performs a new
// authoritative lookup. Successful results refresh the bounded cache.
func (r *TelegramRoleResolver) ResolveGroupRoleFresh(ctx context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return r.resolve(ctx, request, false)
}

func (r *TelegramRoleResolver) resolve(ctx context.Context, request core.GroupRoleRequest, useCache bool) (core.GroupRoleSnapshot, error) {
	if r == nil || r.api == nil || r.peers == nil {
		return core.GroupRoleSnapshot{}, ErrGroupRoleUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return core.GroupRoleSnapshot{}, err
	}
	if err := validateRequest(request); err != nil {
		return core.GroupRoleSnapshot{}, err
	}

	key := keyFor(request)
	if useCache {
		if snapshot, ok := r.getCached(key, r.now().UTC()); ok {
			return snapshot, nil
		}
	}

	if r.verificationSlots == nil {
		return core.GroupRoleSnapshot{}, ErrGroupRoleUnavailable
	}
	select {
	case r.verificationSlots <- struct{}{}:
		defer func() { <-r.verificationSlots }()
	default:
		return core.GroupRoleSnapshot{}, ErrGroupRoleSaturated
	}

	// Recheck after bounded admission in case another lookup populated the cache.
	if useCache {
		if snapshot, ok := r.getCached(key, r.now().UTC()); ok {
			return snapshot, nil
		}
	}

	var (
		snapshot core.GroupRoleSnapshot
		err      error
	)
	switch request.Kind {
	case core.ChatKindSupergroup:
		snapshot, err = r.verifySupergroup(ctx, request)
	case core.ChatKindGroup:
		snapshot, err = r.verifyBasicGroup(ctx, request)
	default:
		err = fmt.Errorf("%w: chat kind %q", ErrGroupRoleUnsupported, request.Kind)
	}
	if err != nil {
		// Verification failures are deliberately not cached.
		return core.GroupRoleSnapshot{}, err
	}
	snapshot.Cached = false
	r.putCached(key, snapshot)
	return snapshot, nil
}

func (r *TelegramRoleResolver) resolveChannel(ctx context.Context, request core.GroupRoleRequest) (*tg.InputChannel, error) {
	input, ok := request.Peer.(*tg.InputPeerChannel)
	if !ok || input.ChannelID != request.ChatID {
		return nil, fmt.Errorf("%w: mismatched supergroup peer", ErrGroupRoleVerification)
	}
	if input.AccessHash != 0 {
		return &tg.InputChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash}, nil
	}
	refreshed, err := r.peers.ReResolve(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("%w: refresh supergroup %d: %v", ErrGroupRoleVerification, request.ChatID, err)
	}
	channel, ok := refreshed.(*tg.InputPeerChannel)
	if !ok || channel.AccessHash == 0 || channel.ChannelID != request.ChatID {
		return nil, fmt.Errorf("%w: refreshed supergroup peer is invalid", ErrGroupRoleVerification)
	}
	return &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, nil
}

func (r *TelegramRoleResolver) resolveParticipantPeer(ctx context.Context, userID int64) (tg.InputPeerClass, error) {
	resolved, err := r.peers.Resolve(ctx, &tg.PeerUser{UserID: userID}, userID, tg.Entities{})
	if err != nil {
		return nil, fmt.Errorf("%w: resolve actor %d: %v", ErrGroupRoleVerification, userID, err)
	}
	if _, ok := resolved.(*tg.InputPeerUser); !ok {
		return nil, fmt.Errorf("%w: actor %d resolved to %T", ErrGroupRoleVerification, userID, resolved)
	}
	return resolved, nil
}

func adminRights(rights tg.ChatAdminRights) core.GroupAdminRights {
	return core.GroupAdminRights{
		ChangeInfo:     rights.ChangeInfo,
		DeleteMessages: rights.DeleteMessages,
		BanUsers:       rights.BanUsers,
		InviteUsers:    rights.InviteUsers,
		PinMessages:    rights.PinMessages,
		AddAdmins:      rights.AddAdmins,
		ManageTopics:   rights.ManageTopics,
	}
}

// MapChannelParticipant maps one authoritative Telegram participant into the
// transport-neutral manager principal, including hierarchy metadata required by
// P7-G target protection.
func MapChannelParticipant(userID int64, participant tg.ChannelParticipantClass) (core.GroupActorPrincipal, error) {
	principal := core.GroupActorPrincipal{UserID: userID, Verified: true}
	switch value := participant.(type) {
	case *tg.ChannelParticipant, *tg.ChannelParticipantSelf:
		principal.Role = core.GroupActorRoleMember
	case *tg.ChannelParticipantCreator:
		principal.Role = core.GroupActorRoleCreator
		principal.Rights = adminRights(value.AdminRights)
	case *tg.ChannelParticipantAdmin:
		principal.Role = core.GroupActorRoleAdministrator
		principal.Rights = adminRights(value.AdminRights)
		principal.CanEdit = value.CanEdit
		principal.PromotedBy = value.PromotedBy
	case *tg.ChannelParticipantBanned:
		switch {
		case value.BannedRights.ViewMessages:
			principal.Role = core.GroupActorRoleBanned
		case value.Left || value.GetLeft():
			principal.Role = core.GroupActorRoleLeft
		default:
			principal.Role = core.GroupActorRoleRestricted
		}
	case *tg.ChannelParticipantLeft:
		principal.Role = core.GroupActorRoleLeft
	case nil:
		return core.GroupActorPrincipal{}, fmt.Errorf("%w: nil channel participant", ErrGroupRoleVerification)
	default:
		return core.GroupActorPrincipal{}, fmt.Errorf("%w: unsupported participant %T", ErrGroupRoleVerification, participant)
	}
	return principal, nil
}

func (r *TelegramRoleResolver) verifySupergroup(ctx context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	channel, err := r.resolveChannel(ctx, request)
	if err != nil {
		return core.GroupRoleSnapshot{}, err
	}
	participantPeer, err := r.resolveParticipantPeer(ctx, request.UserID)
	if err != nil {
		return core.GroupRoleSnapshot{}, err
	}
	result, err := r.api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
		Channel:     channel,
		Participant: participantPeer,
	})
	if err != nil {
		if tgerr.Is(err, "USER_NOT_PARTICIPANT") {
			return core.GroupRoleSnapshot{
				Principal:  core.GroupActorPrincipal{UserID: request.UserID, Role: core.GroupActorRoleLeft, Verified: true},
				ObservedAt: r.now().UTC(),
			}, nil
		}
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: channels.getParticipant(chat=%d,user=%d): %v", ErrGroupRoleVerification, request.ChatID, request.UserID, err)
	}
	if result == nil || result.Participant == nil {
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: empty channels.getParticipant response", ErrGroupRoleVerification)
	}
	principal, err := MapChannelParticipant(request.UserID, result.Participant)
	if err != nil {
		return core.GroupRoleSnapshot{}, err
	}
	return core.GroupRoleSnapshot{Principal: principal, ObservedAt: r.now().UTC()}, nil
}

func basicParticipant(userID int64, participant tg.ChatParticipantClass) (core.GroupActorPrincipal, bool, error) {
	basicAdminRights := core.GroupAdminRights{
		ChangeInfo:     true,
		DeleteMessages: true,
		BanUsers:       true,
		InviteUsers:    true,
		PinMessages:    true,
	}
	switch value := participant.(type) {
	case *tg.ChatParticipant:
		if value.UserID != userID {
			return core.GroupActorPrincipal{}, false, nil
		}
		return core.GroupActorPrincipal{UserID: userID, Role: core.GroupActorRoleMember, Verified: true}, true, nil
	case *tg.ChatParticipantAdmin:
		if value.UserID != userID {
			return core.GroupActorPrincipal{}, false, nil
		}
		return core.GroupActorPrincipal{
			UserID: userID, Role: core.GroupActorRoleAdministrator,
			Rights: basicAdminRights, Verified: true,
		}, true, nil
	case *tg.ChatParticipantCreator:
		if value.UserID != userID {
			return core.GroupActorPrincipal{}, false, nil
		}
		creatorRights := basicAdminRights
		creatorRights.AddAdmins = true
		return core.GroupActorPrincipal{
			UserID: userID, Role: core.GroupActorRoleCreator,
			Rights: creatorRights, Verified: true,
		}, true, nil
	case nil:
		return core.GroupActorPrincipal{}, false, fmt.Errorf("%w: nil basic-group participant", ErrGroupRoleVerification)
	default:
		return core.GroupActorPrincipal{}, false, nil
	}
}

func (r *TelegramRoleResolver) verifyBasicGroup(ctx context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	result, err := r.api.MessagesGetFullChat(ctx, request.ChatID)
	if err != nil {
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: messages.getFullChat(chat=%d): %v", ErrGroupRoleVerification, request.ChatID, err)
	}
	if result == nil {
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: empty messages.getFullChat response", ErrGroupRoleVerification)
	}
	full, ok := result.FullChat.(*tg.ChatFull)
	if !ok || full == nil || full.Participants == nil {
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: basic-group participant state unavailable", ErrGroupRoleVerification)
	}

	switch participants := full.Participants.(type) {
	case *tg.ChatParticipants:
		for _, participant := range participants.Participants {
			principal, matched, mapErr := basicParticipant(request.UserID, participant)
			if mapErr != nil {
				return core.GroupRoleSnapshot{}, mapErr
			}
			if matched {
				return core.GroupRoleSnapshot{Principal: principal, ObservedAt: r.now().UTC()}, nil
			}
		}
		return core.GroupRoleSnapshot{
			Principal:  core.GroupActorPrincipal{UserID: request.UserID, Role: core.GroupActorRoleLeft, Verified: true},
			ObservedAt: r.now().UTC(),
		}, nil
	case *tg.ChatParticipantsForbidden:
		if participants.SelfParticipant == nil {
			return core.GroupRoleSnapshot{}, fmt.Errorf("%w: basic-group participant list is forbidden", ErrGroupRoleVerification)
		}
		principal, matched, mapErr := basicParticipant(request.UserID, participants.SelfParticipant)
		if mapErr != nil {
			return core.GroupRoleSnapshot{}, mapErr
		}
		if !matched {
			return core.GroupRoleSnapshot{}, fmt.Errorf("%w: forbidden participant list cannot verify actor %d", ErrGroupRoleVerification, request.UserID)
		}
		return core.GroupRoleSnapshot{Principal: principal, ObservedAt: r.now().UTC()}, nil
	default:
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: unexpected basic-group participant state %T", ErrGroupRoleVerification, full.Participants)
	}
}
