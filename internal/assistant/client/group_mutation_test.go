package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

type mutationRoleStub struct {
	values map[int64][]core.GroupActorPrincipal
	errs   map[int64]error
	calls  map[int64]int
	events *[]string
}

func (r *mutationRoleStub) ResolveGroupRole(ctx context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return r.ResolveGroupRoleFresh(ctx, req)
}

func (r *mutationRoleStub) ResolveGroupRoleFresh(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	if r.calls == nil {
		r.calls = make(map[int64]int)
	}
	r.calls[req.UserID]++
	if r.events != nil {
		*r.events = append(*r.events, fmt.Sprintf("role:%d", req.UserID))
	}
	if err := r.errs[req.UserID]; err != nil {
		return core.GroupRoleSnapshot{}, err
	}
	values := r.values[req.UserID]
	if len(values) == 0 {
		return core.GroupRoleSnapshot{}, fmt.Errorf("%w: no role for %d", core.ErrUnavailable, req.UserID)
	}
	index := r.calls[req.UserID] - 1
	if index >= len(values) {
		index = len(values) - 1
	}
	principal := values[index]
	principal.UserID = req.UserID
	principal.Verified = true
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

type mutationAPIStub struct {
	events *[]string

	botParticipant tg.ChannelParticipantClass
	botErr         error

	editBannedErr   error
	editBannedCalls int
	editBannedReqs  []*tg.ChannelsEditBannedRequest

	editAdminErr   error
	editAdminCalls int
	editAdminReqs  []*tg.ChannelsEditAdminRequest

	deleteChatUserErr   error
	deleteChatUserCalls int

	editChatAdminErr   error
	editChatAdminCalls int

	pinErr   error
	pinCalls int

	defaultRightsErr   error
	defaultRightsCalls int

	getRepliesCalls int
	getHistoryCalls int
	replyPages      []tg.MessagesMessagesClass
	historyPages    []tg.MessagesMessagesClass

	channelDeleteCalls int
	channelDeleteIDs   [][]int
	messageDeleteCalls int
	messageDeleteIDs   [][]int
}

func (a *mutationAPIStub) event(value string) {
	if a.events != nil {
		*a.events = append(*a.events, value)
	}
}

func (a *mutationAPIStub) ChannelsGetParticipant(
	context.Context,
	*tg.ChannelsGetParticipantRequest,
) (*tg.ChannelsChannelParticipant, error) {
	a.event("bot")
	if a.botErr != nil {
		return nil, a.botErr
	}
	return &tg.ChannelsChannelParticipant{Participant: a.botParticipant}, nil
}

func (a *mutationAPIStub) ChannelsEditBanned(
	_ context.Context,
	req *tg.ChannelsEditBannedRequest,
) (tg.UpdatesClass, error) {
	a.editBannedCalls++
	a.editBannedReqs = append(a.editBannedReqs, req)
	a.event("rpc:editBanned")
	return &tg.Updates{}, a.editBannedErr
}

func (a *mutationAPIStub) ChannelsEditAdmin(
	_ context.Context,
	req *tg.ChannelsEditAdminRequest,
) (tg.UpdatesClass, error) {
	a.editAdminCalls++
	a.editAdminReqs = append(a.editAdminReqs, req)
	a.event("rpc:editAdmin")
	return &tg.Updates{}, a.editAdminErr
}

func (a *mutationAPIStub) ChannelsDeleteMessages(
	_ context.Context,
	req *tg.ChannelsDeleteMessagesRequest,
) (*tg.MessagesAffectedMessages, error) {
	a.channelDeleteCalls++
	a.channelDeleteIDs = append(a.channelDeleteIDs, append([]int(nil), req.ID...))
	a.event("rpc:channelDelete")
	return &tg.MessagesAffectedMessages{}, nil
}

func (a *mutationAPIStub) MessagesDeleteChatUser(
	context.Context,
	*tg.MessagesDeleteChatUserRequest,
) (tg.UpdatesClass, error) {
	a.deleteChatUserCalls++
	a.event("rpc:deleteChatUser")
	return &tg.Updates{}, a.deleteChatUserErr
}

func (a *mutationAPIStub) MessagesEditChatAdmin(
	context.Context,
	*tg.MessagesEditChatAdminRequest,
) (bool, error) {
	a.editChatAdminCalls++
	a.event("rpc:editChatAdmin")
	return true, a.editChatAdminErr
}

func (a *mutationAPIStub) MessagesUpdatePinnedMessage(
	context.Context,
	*tg.MessagesUpdatePinnedMessageRequest,
) (tg.UpdatesClass, error) {
	a.pinCalls++
	a.event("rpc:pin")
	return &tg.Updates{}, a.pinErr
}

func (a *mutationAPIStub) MessagesEditChatDefaultBannedRights(
	context.Context,
	*tg.MessagesEditChatDefaultBannedRightsRequest,
) (tg.UpdatesClass, error) {
	a.defaultRightsCalls++
	a.event("rpc:defaultRights")
	return &tg.Updates{}, a.defaultRightsErr
}

func (a *mutationAPIStub) MessagesDeleteMessages(
	_ context.Context,
	req *tg.MessagesDeleteMessagesRequest,
) (*tg.MessagesAffectedMessages, error) {
	a.messageDeleteCalls++
	a.messageDeleteIDs = append(a.messageDeleteIDs, append([]int(nil), req.ID...))
	a.event("rpc:messageDelete")
	return &tg.MessagesAffectedMessages{}, nil
}

func (a *mutationAPIStub) MessagesGetHistory(
	context.Context,
	*tg.MessagesGetHistoryRequest,
) (tg.MessagesMessagesClass, error) {
	index := a.getHistoryCalls
	a.getHistoryCalls++
	if index < len(a.historyPages) {
		return a.historyPages[index], nil
	}
	return &tg.MessagesMessages{}, nil
}

func (a *mutationAPIStub) MessagesGetReplies(
	context.Context,
	*tg.MessagesGetRepliesRequest,
) (tg.MessagesMessagesClass, error) {
	index := a.getRepliesCalls
	a.getRepliesCalls++
	if index < len(a.replyPages) {
		return a.replyPages[index], nil
	}
	return &tg.MessagesMessages{}, nil
}

func mutationPrincipal(role core.GroupActorRole, rights core.GroupAdminRights) core.GroupActorPrincipal {
	return core.GroupActorPrincipal{Role: role, Rights: rights, Verified: true}
}

func banAdmin() core.GroupActorPrincipal {
	return mutationPrincipal(core.GroupActorRoleAdministrator, core.GroupAdminRights{BanUsers: true})
}

func mutationBotParticipant(rights tg.ChatAdminRights) tg.ChannelParticipantClass {
	return &tg.ChannelParticipantAdmin{
		Self:        true,
		UserID:      20,
		CanEdit:     true,
		AdminRights: rights,
	}
}

func newMutationService(
	api *mutationAPIStub,
	roles *mutationRoleStub,
) *managedGroupMutation {
	return newManagedGroupMutation(
		api,
		peer.NewResolver(peer.NewMemoryCache()),
		roles,
		func() int64 { return 20 },
	)
}

func supergroupMutationFixture() (
	command.GroupMutationContext,
	*tg.InputPeerChannel,
	*tg.InputPeerUser,
) {
	return command.GroupMutationContext{
			ActorID: 10,
			ChatID:  99,
			Kind:    core.ChatKindSupergroup,
		},
		&tg.InputPeerChannel{ChannelID: 99, AccessHash: 9900},
		&tg.InputPeerUser{UserID: 30, AccessHash: 3000}
}

func assertEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events=%v, want %v", got, want)
		}
	}
}

func TestP7GSupergroupBanRevalidatesTargetBotActorBeforeRPC(t *testing.T) {
	var events []string
	api := &mutationAPIStub{
		events:         &events,
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
	}
	roles := &mutationRoleStub{
		events: &events,
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan,
		Peer:   chat,
		Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.editBannedCalls != 1 {
		t.Fatalf("editBanned calls=%d, want 1", api.editBannedCalls)
	}
	assertEvents(t, events, "bot", "role:10", "role:30", "rpc:editBanned")
	if !api.editBannedReqs[0].BannedRights.ViewMessages {
		t.Fatal("ban RPC did not carry full ban rights")
	}
}

func TestP7GBotRightsFailurePreventsPhysicalMutation(t *testing.T) {
	var events []string
	api := &mutationAPIStub{
		events:         &events,
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{}),
	}
	roles := &mutationRoleStub{
		events: &events,
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	})
	if !errors.Is(err, core.ErrGroupMutationDenied) {
		t.Fatalf("bot-right error=%v, want ErrGroupMutationDenied", err)
	}
	if api.editBannedCalls != 0 {
		t.Fatalf("bot-right failure issued %d mutations", api.editBannedCalls)
	}
	assertEvents(t, events, "bot")
}

func TestP7GActorRightsFailurePreventsPhysicalMutation(t *testing.T) {
	var events []string
	api := &mutationAPIStub{
		events:         &events,
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
	}
	roles := &mutationRoleStub{
		events: &events,
		values: map[int64][]core.GroupActorPrincipal{
			10: {mutationPrincipal(core.GroupActorRoleAdministrator, core.GroupAdminRights{})},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	})
	if !errors.Is(err, core.ErrGroupMutationDenied) {
		t.Fatalf("actor-right error=%v, want ErrGroupMutationDenied", err)
	}
	if api.editBannedCalls != 0 {
		t.Fatalf("actor-right failure issued %d mutations", api.editBannedCalls)
	}
	assertEvents(t, events, "bot", "role:10")
}

func TestP7GProtectsCreatorAndHigherAdminTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actor  core.GroupActorPrincipal
		target core.GroupActorPrincipal
		action core.GroupMutationAction
	}{
		{
			name:   "creator target",
			actor:  banAdmin(),
			target: mutationPrincipal(core.GroupActorRoleCreator, core.GroupAdminRights{}),
			action: core.GroupMutationBan,
		},
		{
			name:   "admin cannot ban admin",
			actor:  banAdmin(),
			target: mutationPrincipal(core.GroupActorRoleAdministrator, core.GroupAdminRights{BanUsers: true}),
			action: core.GroupMutationBan,
		},
		{
			name: "creator cannot demote uneditable admin",
			actor: mutationPrincipal(core.GroupActorRoleCreator, core.GroupAdminRights{
				AddAdmins: true,
			}),
			target: core.GroupActorPrincipal{
				Role:     core.GroupActorRoleAdministrator,
				Rights:   core.GroupAdminRights{AddAdmins: true},
				CanEdit:  false,
				Verified: true,
			},
			action: core.GroupMutationDemote,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			botRights := tg.ChatAdminRights{BanUsers: true, AddAdmins: true}
			api := &mutationAPIStub{
				events:         &events,
				botParticipant: mutationBotParticipant(botRights),
			}
			roles := &mutationRoleStub{
				events: &events,
				values: map[int64][]core.GroupActorPrincipal{
					10: {tc.actor},
					30: {tc.target},
				},
			}
			service := newMutationService(api, roles)
			meta, chat, targetPeer := supergroupMutationFixture()

			_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
				Action: tc.action, Peer: chat, Target: targetPeer,
			})
			if !errors.Is(err, core.ErrGroupMutationTargetProtected) {
				t.Fatalf("hierarchy error=%v, want ErrGroupMutationTargetProtected", err)
			}
			if api.editBannedCalls != 0 || api.editAdminCalls != 0 {
				t.Fatalf("protected target reached mutation: banned=%d admin=%d",
					api.editBannedCalls, api.editAdminCalls)
			}
		})
	}
}

func TestP7GDesiredStateNoopStillRevalidatesActorAndBot(t *testing.T) {
	var events []string
	api := &mutationAPIStub{
		events:         &events,
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
	}
	roles := &mutationRoleStub{
		events: &events,
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleBanned, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.editBannedCalls != 0 {
		t.Fatalf("desired-state no-op issued %d mutations", api.editBannedCalls)
	}
	assertEvents(t, events, "bot", "role:10", "role:30")
}

func TestP7GChatNotModifiedIsIdempotentSuccess(t *testing.T) {
	api := &mutationAPIStub{
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
		editBannedErr:  tgerr.New(400, "CHAT_NOT_MODIFIED"),
	}
	roles := &mutationRoleStub{
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	if _, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	}); err != nil {
		t.Fatalf("CHAT_NOT_MODIFIED should be success: %v", err)
	}
	if api.editBannedCalls != 1 {
		t.Fatalf("editBanned calls=%d, want 1", api.editBannedCalls)
	}
}

func TestP7GKickRevalidatesBeforeBothPhysicalRPCs(t *testing.T) {
	var events []string
	api := &mutationAPIStub{
		events:         &events,
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
	}
	roles := &mutationRoleStub{
		events: &events,
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin(), banAdmin()},
			30: {
				mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{}),
				mutationPrincipal(core.GroupActorRoleBanned, core.GroupAdminRights{}),
			},
		},
	}
	service := newMutationService(api, roles)
	service.now = func() time.Time { return time.Unix(1000, 0) }
	meta, chat, target := supergroupMutationFixture()

	if _, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationKick, Peer: chat, Target: target,
	}); err != nil {
		t.Fatal(err)
	}
	if api.editBannedCalls != 2 {
		t.Fatalf("kick physical mutations=%d, want 2", api.editBannedCalls)
	}
	if !api.editBannedReqs[0].BannedRights.ViewMessages ||
		api.editBannedReqs[0].BannedRights.UntilDate != 1060 {
		t.Fatalf("first kick request rights=%+v", api.editBannedReqs[0].BannedRights)
	}
	if !api.editBannedReqs[1].BannedRights.Zero() {
		t.Fatalf("second kick request should clear rights: %+v", api.editBannedReqs[1].BannedRights)
	}
	assertEvents(t, events,
		"bot", "role:10", "role:30", "rpc:editBanned",
		"bot", "role:10", "role:30", "rpc:editBanned",
	)
}

func TestP7GPurgeIsTopicAwareBoundedAndRevalidatesDeleteRPC(t *testing.T) {
	var events []string
	api := &mutationAPIStub{
		events:         &events,
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{DeleteMessages: true}),
		replyPages: []tg.MessagesMessagesClass{
			&tg.MessagesMessages{Messages: []tg.MessageClass{
				&tg.Message{ID: 11},
			}},
			&tg.MessagesMessages{},
		},
	}
	deleteAdmin := mutationPrincipal(
		core.GroupActorRoleAdministrator,
		core.GroupAdminRights{DeleteMessages: true},
	)
	roles := &mutationRoleStub{
		events: &events,
		values: map[int64][]core.GroupActorPrincipal{
			10: {deleteAdmin, deleteAdmin},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, _ := supergroupMutationFixture()

	result, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action:  core.GroupMutationPurge,
		Peer:    chat,
		TopicID: 42,
		FromID:  10,
		ToID:    12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 3 {
		t.Fatalf("deleted=%d, want 3", result.Deleted)
	}
	if api.getRepliesCalls == 0 || api.getHistoryCalls != 0 {
		t.Fatalf("topic purge replies=%d history=%d", api.getRepliesCalls, api.getHistoryCalls)
	}
	if api.channelDeleteCalls != 1 {
		t.Fatalf("channel delete calls=%d, want 1", api.channelDeleteCalls)
	}
	if len(api.channelDeleteIDs[0]) != 3 {
		t.Fatalf("delete ids=%v, want 3 ids", api.channelDeleteIDs[0])
	}
	if roles.calls[10] != 2 {
		t.Fatalf("actor fresh calls=%d, want 2 (scan + delete RPC)", roles.calls[10])
	}
}

func TestP7GBasicGroupBanUsesManagedDeleteChatUser(t *testing.T) {
	api := &mutationAPIStub{}
	roles := &mutationRoleStub{
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			20: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta := command.GroupMutationContext{ActorID: 10, ChatID: 55, Kind: core.ChatKindGroup}
	chat := &tg.InputPeerChat{ChatID: 55}
	target := &tg.InputPeerUser{UserID: 30, AccessHash: 3000}

	if _, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	}); err != nil {
		t.Fatal(err)
	}
	if api.deleteChatUserCalls != 1 || api.editBannedCalls != 0 {
		t.Fatalf("basic ban deleteChatUser=%d editBanned=%d",
			api.deleteChatUserCalls, api.editBannedCalls)
	}
}

func TestP7GVerificationFailureDoesNotBecomePermissionDenial(t *testing.T) {
	api := &mutationAPIStub{
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
	}
	verifyErr := fmt.Errorf("%w: Telegram verification failed", core.ErrUnavailable)
	roles := &mutationRoleStub{
		errs: map[int64]error{10: verifyErr},
		values: map[int64][]core.GroupActorPrincipal{
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	})
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("verification error=%v, want ErrUnavailable", err)
	}
	if errors.Is(err, core.ErrGroupMutationDenied) {
		t.Fatalf("verification failure was converted to mutation denial: %v", err)
	}
	if api.editBannedCalls != 0 {
		t.Fatalf("verification failure issued mutation")
	}
}

func TestP7GTelegramAdminInvalidMapsToProtectedTarget(t *testing.T) {
	api := &mutationAPIStub{
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
		editBannedErr:  tgerr.New(400, "USER_ADMIN_INVALID"),
	}
	roles := &mutationRoleStub{
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	_, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan, Peer: chat, Target: target,
	})
	if !errors.Is(err, core.ErrGroupMutationTargetProtected) {
		t.Fatalf("USER_ADMIN_INVALID error=%v, want target-protected", err)
	}
}

func TestP7GPromoteDelegatesOnlyActorBotIntersection(t *testing.T) {
	api := &mutationAPIStub{
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{
			ChangeInfo:     true,
			DeleteMessages: true,
			BanUsers:       true,
			InviteUsers:    false,
			PinMessages:    true,
			AddAdmins:      true,
			ManageTopics:   false,
		}),
	}
	roles := &mutationRoleStub{
		values: map[int64][]core.GroupActorPrincipal{
			10: {
				mutationPrincipal(core.GroupActorRoleAdministrator, core.GroupAdminRights{
					ChangeInfo:     true,
					DeleteMessages: false,
					BanUsers:       true,
					InviteUsers:    true,
					PinMessages:    true,
					AddAdmins:      true,
					ManageTopics:   true,
				}),
			},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	if _, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationPromote,
		Peer:   chat,
		Target: target,
		Title:  "helper",
	}); err != nil {
		t.Fatal(err)
	}
	if api.editAdminCalls != 1 || len(api.editAdminReqs) != 1 {
		t.Fatalf("editAdmin calls=%d reqs=%d, want 1/1", api.editAdminCalls, len(api.editAdminReqs))
	}
	rights := api.editAdminReqs[0].AdminRights
	if !rights.ChangeInfo || !rights.BanUsers || !rights.PinMessages || !rights.AddAdmins {
		t.Fatalf("intersection rights missing expected grants: %+v", rights)
	}
	if rights.DeleteMessages || rights.InviteUsers || rights.ManageTopics ||
		rights.PostMessages || rights.EditMessages {
		t.Fatalf("promotion delegated rights outside actor/bot intersection: %+v", rights)
	}
}

func TestP7GBasicGroupBanUserNotParticipantIsIdempotentSuccess(t *testing.T) {
	api := &mutationAPIStub{
		deleteChatUserErr: tgerr.New(400, "USER_NOT_PARTICIPANT"),
	}
	roles := &mutationRoleStub{
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			20: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta := command.GroupMutationContext{ActorID: 10, ChatID: 55, Kind: core.ChatKindGroup}
	chat := &tg.InputPeerChat{ChatID: 55}
	target := &tg.InputPeerUser{UserID: 30, AccessHash: 3000}

	if _, err := service.Execute(context.Background(), meta, command.GroupMutationRequest{
		Action: core.GroupMutationBan,
		Peer:   chat,
		Target: target,
	}); err != nil {
		t.Fatalf("USER_NOT_PARTICIPANT should be desired-state success for basic-group ban: %v", err)
	}
	if api.deleteChatUserCalls != 1 {
		t.Fatalf("deleteChatUser calls=%d, want 1", api.deleteChatUserCalls)
	}
}


func TestP7LBotRightsChangedAfterAdmissionPreventPhysicalMutation(t *testing.T) {
	api := &mutationAPIStub{
		botParticipant: mutationBotParticipant(tg.ChatAdminRights{BanUsers: true}),
	}
	roles := &mutationRoleStub{
		values: map[int64][]core.GroupActorPrincipal{
			10: {banAdmin()},
			30: {mutationPrincipal(core.GroupActorRoleMember, core.GroupAdminRights{})},
		},
	}
	service := newMutationService(api, roles)
	meta, chat, target := supergroupMutationFixture()

	controller := admission.NewController(map[tasks.PoolID]admission.PoolConfig{
		"interactive": {BacklogLimit: 8, PayloadBudget: 1 << 20},
	})
	spec := tasks.WorkSpec{
		ID:          "p7l:bot-rights-race",
		QuotaOwner:  "telegram:chat:55",
		Pool:        "interactive",
		Class:       tasks.PriorityInteractive,
		OrderingKey: "chat:55",
		Handler: func(ctx context.Context) error {
			_, err := service.Execute(ctx, meta, command.GroupMutationRequest{
				Action: core.GroupMutationBan,
				Peer:   chat,
				Target: target,
			})
			return err
		},
	}
	if err := controller.CanAdmit(spec, 0); err != nil {
		t.Fatalf("admission: %v", err)
	}
	controller.Enqueue(&admission.QueueEntry{Spec: spec})

	// The command has been admitted/queued while the bot had BanUsers. Telegram
	// rights are revoked before dispatch; the managed mutation must observe the
	// new bot state and suppress the physical RPC.
	api.botParticipant = mutationBotParticipant(tg.ChatAdminRights{})

	entry, err := controller.SelectCandidate("interactive")
	if err != nil {
		t.Fatalf("select admitted mutation: %v", err)
	}
	err = entry.Spec.Handler(context.Background())
	if !errors.Is(err, core.ErrGroupMutationDenied) {
		t.Fatalf("post-admission bot-right downgrade error=%v want ErrGroupMutationDenied", err)
	}
	if api.editBannedCalls != 0 {
		t.Fatalf("revoked bot rights still issued %d physical mutations", api.editBannedCalls)
	}
	controller.OnTaskTerminal(entry.Spec)
}
