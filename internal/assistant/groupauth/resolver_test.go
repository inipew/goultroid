package groupauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
)

type roleAPIStub struct {
	mu               sync.Mutex
	channel          map[int64]tg.ChannelParticipantClass
	fullChat         map[int64]*tg.MessagesChatFull
	participantErr   error
	fullChatErr      error
	participantCalls int
	fullChatCalls    int
}

func (a *roleAPIStub) ChannelsGetParticipant(
	_ context.Context,
	req *tg.ChannelsGetParticipantRequest,
) (*tg.ChannelsChannelParticipant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.participantCalls++
	if a.participantErr != nil {
		return nil, a.participantErr
	}
	user, ok := req.Participant.(*tg.InputPeerUser)
	if !ok || user.UserID <= 0 {
		return nil, errors.New("unexpected participant peer")
	}
	participant, ok := a.channel[user.UserID]
	if !ok {
		return nil, tgerr.New(400, "USER_NOT_PARTICIPANT")
	}
	return &tg.ChannelsChannelParticipant{Participant: participant}, nil
}

func (a *roleAPIStub) MessagesGetFullChat(_ context.Context, chatID int64) (*tg.MessagesChatFull, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fullChatCalls++
	if a.fullChatErr != nil {
		return nil, a.fullChatErr
	}
	return a.fullChat[chatID], nil
}

func (a *roleAPIStub) calls() (participant, full int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.participantCalls, a.fullChatCalls
}

func rolePeerResolver(userIDs ...int64) *peer.DefaultResolver {
	cache := peer.NewMemoryCache()
	entities := tg.Entities{Users: make(map[int64]*tg.User)}
	for _, userID := range userIDs {
		entities.Users[userID] = &tg.User{ID: userID, AccessHash: userID*100 + 7}
	}
	cache.CacheEntities(entities)
	return peer.NewResolver(cache)
}

func supergroupRequest(userID int64) core.GroupRoleRequest {
	return core.GroupRoleRequest{
		ChatID: 99,
		Kind:   core.ChatKindSupergroup,
		Peer:   &tg.InputPeerChannel{ChannelID: 99, AccessHash: 12345},
		UserID: userID,
	}
}

func TestTelegramRoleResolverSupergroupRoleMapping(t *testing.T) {
	api := &roleAPIStub{channel: map[int64]tg.ChannelParticipantClass{
		1: &tg.ChannelParticipant{UserID: 1},
		2: &tg.ChannelParticipantAdmin{
			UserID:     2,
			CanEdit:    true,
			PromotedBy: 77,
			AdminRights: tg.ChatAdminRights{
				DeleteMessages: true,
				BanUsers:       true,
				PinMessages:    true,
			},
		},
		3: &tg.ChannelParticipantCreator{UserID: 3, AdminRights: tg.ChatAdminRights{
			AddAdmins:    true,
			ManageTopics: true,
		}},
		4: &tg.ChannelParticipantBanned{
			Peer:         &tg.PeerUser{UserID: 4},
			BannedRights: tg.ChatBannedRights{SendMessages: true},
		},
		5: &tg.ChannelParticipantBanned{
			Peer:         &tg.PeerUser{UserID: 5},
			BannedRights: tg.ChatBannedRights{ViewMessages: true},
		},
		6: &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: 6}},
	}}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(1, 2, 3, 4, 5, 6))

	tests := []struct {
		userID int64
		role   core.GroupActorRole
		check  func(core.GroupActorPrincipal) bool
	}{
		{1, core.GroupActorRoleMember, nil},
		{2, core.GroupActorRoleAdministrator, func(p core.GroupActorPrincipal) bool {
			return p.Rights.DeleteMessages && p.Rights.BanUsers && p.Rights.PinMessages &&
				p.CanEdit && p.PromotedBy == 77
		}},
		{3, core.GroupActorRoleCreator, func(p core.GroupActorPrincipal) bool {
			return p.Rights.AddAdmins && p.Rights.ManageTopics
		}},
		{4, core.GroupActorRoleRestricted, nil},
		{5, core.GroupActorRoleBanned, nil},
		{6, core.GroupActorRoleLeft, nil},
	}

	for _, tc := range tests {
		snapshot, err := resolver.ResolveGroupRoleFresh(context.Background(), supergroupRequest(tc.userID))
		if err != nil {
			t.Fatalf("user %d: %v", tc.userID, err)
		}
		if !snapshot.Principal.Verified || snapshot.Principal.Role != tc.role {
			t.Fatalf("user %d principal=%+v, want role=%s", tc.userID, snapshot.Principal, tc.role)
		}
		if tc.check != nil && !tc.check(snapshot.Principal) {
			t.Fatalf("user %d rights not mapped: %+v", tc.userID, snapshot.Principal.Rights)
		}
	}
}

func TestTelegramRoleResolverAuthoritativeNonMemberIsCached(t *testing.T) {
	api := &roleAPIStub{channel: map[int64]tg.ChannelParticipantClass{}}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(42))

	first, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42))
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if !first.Principal.Verified || first.Principal.Role != core.GroupActorRoleLeft || first.Cached {
		t.Fatalf("unexpected first snapshot: %+v", first)
	}

	second, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42))
	if err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if !second.Cached || second.Principal.Role != core.GroupActorRoleLeft {
		t.Fatalf("authoritative non-member was not cached: %+v", second)
	}
	participantCalls, _ := api.calls()
	if participantCalls != 1 {
		t.Fatalf("participant calls=%d, want 1", participantCalls)
	}
}

func TestTelegramRoleResolverVerificationFailureIsNotCached(t *testing.T) {
	api := &roleAPIStub{
		channel:        map[int64]tg.ChannelParticipantClass{},
		participantErr: errors.New("telegram unavailable"),
	}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(42))

	for i := 0; i < 2; i++ {
		_, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42))
		if !errors.Is(err, ErrGroupRoleVerification) || !errors.Is(err, core.ErrUnavailable) {
			t.Fatalf("resolve %d error=%v", i, err)
		}
	}
	participantCalls, _ := api.calls()
	if participantCalls != 2 {
		t.Fatalf("verification failure was cached: participant calls=%d", participantCalls)
	}
}

func TestTelegramRoleResolverFreshBypassesCachedRole(t *testing.T) {
	api := &roleAPIStub{channel: map[int64]tg.ChannelParticipantClass{
		42: &tg.ChannelParticipant{UserID: 42},
	}}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(42))

	first, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42))
	if err != nil || first.Principal.Role != core.GroupActorRoleMember {
		t.Fatalf("initial role=%+v err=%v", first, err)
	}

	api.mu.Lock()
	api.channel[42] = &tg.ChannelParticipantAdmin{
		UserID:      42,
		AdminRights: tg.ChatAdminRights{BanUsers: true},
	}
	api.mu.Unlock()

	cached, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42))
	if err != nil || !cached.Cached || cached.Principal.Role != core.GroupActorRoleMember {
		t.Fatalf("cached role=%+v err=%v", cached, err)
	}

	fresh, err := resolver.ResolveGroupRoleFresh(context.Background(), supergroupRequest(42))
	if err != nil || fresh.Cached || fresh.Principal.Role != core.GroupActorRoleAdministrator || !fresh.Principal.Rights.BanUsers {
		t.Fatalf("fresh role=%+v err=%v", fresh, err)
	}

	updated, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42))
	if err != nil || !updated.Cached || updated.Principal.Role != core.GroupActorRoleAdministrator {
		t.Fatalf("updated cached role=%+v err=%v", updated, err)
	}
	participantCalls, _ := api.calls()
	if participantCalls != 2 {
		t.Fatalf("participant calls=%d, want 2", participantCalls)
	}
}

func TestTelegramRoleResolverCacheIsBoundedLRU(t *testing.T) {
	api := &roleAPIStub{channel: map[int64]tg.ChannelParticipantClass{
		1: &tg.ChannelParticipant{UserID: 1},
		2: &tg.ChannelParticipant{UserID: 2},
		3: &tg.ChannelParticipant{UserID: 3},
	}}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(1, 2, 3))
	resolver.cacheCapacity = 2

	for _, userID := range []int64{1, 2} {
		if _, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(userID)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(3)); err != nil {
		t.Fatal(err)
	}
	if len(resolver.entries) != 2 {
		t.Fatalf("cache size=%d, want 2", len(resolver.entries))
	}
	if _, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(2)); err != nil {
		t.Fatal(err)
	}
	participantCalls, _ := api.calls()
	if participantCalls != 4 {
		t.Fatalf("participant calls=%d, want 4 after LRU eviction", participantCalls)
	}
}

func TestTelegramRoleResolverLazyTTLExpiry(t *testing.T) {
	api := &roleAPIStub{channel: map[int64]tg.ChannelParticipantClass{
		42: &tg.ChannelParticipant{UserID: 42},
	}}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(42))
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { return now }
	resolver.memberTTL = time.Minute

	if _, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := resolver.ResolveGroupRole(context.Background(), supergroupRequest(42)); err != nil {
		t.Fatal(err)
	}
	participantCalls, _ := api.calls()
	if participantCalls != 2 {
		t.Fatalf("expired entry did not refresh lazily: calls=%d", participantCalls)
	}
}

func TestTelegramRoleResolverSaturationFailsWithoutRPC(t *testing.T) {
	api := &roleAPIStub{channel: map[int64]tg.ChannelParticipantClass{
		42: &tg.ChannelParticipant{UserID: 42},
	}}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver(42))
	resolver.verificationSlots = make(chan struct{}, 1)
	resolver.verificationSlots <- struct{}{}

	_, err := resolver.ResolveGroupRoleFresh(context.Background(), supergroupRequest(42))
	if !errors.Is(err, ErrGroupRoleSaturated) || !errors.Is(err, core.ErrResourceLimit) {
		t.Fatalf("saturation error=%v", err)
	}
	participantCalls, fullCalls := api.calls()
	if participantCalls != 0 || fullCalls != 0 {
		t.Fatalf("saturated resolver issued RPCs: participant=%d full=%d", participantCalls, fullCalls)
	}
}

func TestTelegramRoleResolverBasicGroupUsesManagedFullChat(t *testing.T) {
	api := &roleAPIStub{
		channel: map[int64]tg.ChannelParticipantClass{},
		fullChat: map[int64]*tg.MessagesChatFull{
			55: {
				FullChat: &tg.ChatFull{
					ID: 55,
					Participants: &tg.ChatParticipants{
						ChatID: 55,
						Participants: []tg.ChatParticipantClass{
							&tg.ChatParticipant{UserID: 1},
							&tg.ChatParticipantAdmin{UserID: 2},
							&tg.ChatParticipantCreator{UserID: 3},
						},
					},
				},
			},
		},
	}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver())

	for _, tc := range []struct {
		userID int64
		role   core.GroupActorRole
		check  func(core.GroupActorPrincipal) bool
	}{
		{1, core.GroupActorRoleMember, nil},
		{2, core.GroupActorRoleAdministrator, func(p core.GroupActorPrincipal) bool {
			return p.Rights.BanUsers && p.Rights.DeleteMessages && p.Rights.PinMessages &&
				!p.Rights.AddAdmins
		}},
		{3, core.GroupActorRoleCreator, func(p core.GroupActorPrincipal) bool {
			return p.Rights.BanUsers && p.Rights.DeleteMessages && p.Rights.PinMessages &&
				p.Rights.AddAdmins
		}},
		{9, core.GroupActorRoleLeft, nil},
	} {
		snapshot, err := resolver.ResolveGroupRoleFresh(context.Background(), core.GroupRoleRequest{
			ChatID: 55,
			Kind:   core.ChatKindGroup,
			Peer:   &tg.InputPeerChat{ChatID: 55},
			UserID: tc.userID,
		})
		if err != nil {
			t.Fatalf("user %d: %v", tc.userID, err)
		}
		if !snapshot.Principal.Verified || snapshot.Principal.Role != tc.role {
			t.Fatalf("user %d principal=%+v want role=%s", tc.userID, snapshot.Principal, tc.role)
		}
		if tc.check != nil && !tc.check(snapshot.Principal) {
			t.Fatalf("user %d effective basic-group rights=%+v", tc.userID, snapshot.Principal.Rights)
		}
	}
	participantCalls, fullCalls := api.calls()
	if participantCalls != 0 || fullCalls != 4 {
		t.Fatalf("unexpected RPC lane participant=%d full=%d", participantCalls, fullCalls)
	}
}

func TestTelegramRoleResolverForbiddenBasicGroupIsVerificationFailure(t *testing.T) {
	api := &roleAPIStub{
		channel: map[int64]tg.ChannelParticipantClass{},
		fullChat: map[int64]*tg.MessagesChatFull{
			55: {
				FullChat: &tg.ChatFull{
					ID:           55,
					Participants: &tg.ChatParticipantsForbidden{ChatID: 55},
				},
			},
		},
	}
	resolver := NewTelegramRoleResolver(api, rolePeerResolver())
	_, err := resolver.ResolveGroupRoleFresh(context.Background(), core.GroupRoleRequest{
		ChatID: 55,
		Kind:   core.ChatKindGroup,
		Peer:   &tg.InputPeerChat{ChatID: 55},
		UserID: 9,
	})
	if !errors.Is(err, ErrGroupRoleVerification) {
		t.Fatalf("forbidden participant list error=%v", err)
	}
}
