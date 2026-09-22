package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantgroupauth "github.com/inipew/goultroid/internal/assistant/groupauth"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
)

const maxAssistantPurgeMessages = 1000

type groupMutationAPI interface {
	ChannelsGetParticipant(context.Context, *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error)
	ChannelsEditBanned(context.Context, *tg.ChannelsEditBannedRequest) (tg.UpdatesClass, error)
	ChannelsEditAdmin(context.Context, *tg.ChannelsEditAdminRequest) (tg.UpdatesClass, error)
	ChannelsDeleteMessages(context.Context, *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)

	MessagesDeleteChatUser(context.Context, *tg.MessagesDeleteChatUserRequest) (tg.UpdatesClass, error)
	MessagesEditChatAdmin(context.Context, *tg.MessagesEditChatAdminRequest) (bool, error)
	MessagesUpdatePinnedMessage(context.Context, *tg.MessagesUpdatePinnedMessageRequest) (tg.UpdatesClass, error)
	MessagesEditChatDefaultBannedRights(context.Context, *tg.MessagesEditChatDefaultBannedRightsRequest) (tg.UpdatesClass, error)
	MessagesDeleteMessages(context.Context, *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	MessagesGetHistory(context.Context, *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	MessagesGetReplies(context.Context, *tg.MessagesGetRepliesRequest) (tg.MessagesMessagesClass, error)
}

type managedGroupMutation struct {
	api       groupMutationAPI
	peers     peer.Resolver
	roles     core.GroupRoleResolver
	selfID    func() int64
	now       func() time.Time
}

var _ command.GroupMutationExecutor = (*managedGroupMutation)(nil)

func newManagedGroupMutation(
	api groupMutationAPI,
	peers peer.Resolver,
	roles core.GroupRoleResolver,
	selfID func() int64,
) *managedGroupMutation {
	if api == nil || peers == nil || roles == nil || selfID == nil {
		return nil
	}
	return &managedGroupMutation{
		api: api, peers: peers, roles: roles, selfID: selfID, now: time.Now,
	}
}

func (m *managedGroupMutation) resolvePeer(ctx context.Context, input tg.InputPeerClass) (tg.InputPeerClass, error) {
	if input == nil {
		return nil, fmt.Errorf("%w: mutation peer is nil", core.ErrInvalidArgs)
	}
	switch value := input.(type) {
	case *tg.InputPeerChat:
		if value.ChatID <= 0 {
			return nil, fmt.Errorf("%w: invalid basic-group id", core.ErrInvalidArgs)
		}
		return value, nil
	case *tg.InputPeerChannel:
		if value.ChannelID <= 0 {
			return nil, fmt.Errorf("%w: invalid supergroup id", core.ErrInvalidArgs)
		}
		if value.AccessHash != 0 {
			return value, nil
		}
	case *tg.InputPeerUser:
		if value.UserID <= 0 {
			return nil, fmt.Errorf("%w: invalid target user id", core.ErrInvalidArgs)
		}
		if value.AccessHash != 0 {
			return value, nil
		}
	default:
		return nil, fmt.Errorf("%w: unsupported mutation peer %T", core.ErrUnsupported, input)
	}
	resolved, err := m.peers.ReResolve(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("%w: refresh mutation peer %T: %v", core.ErrUnavailable, input, err)
	}
	return resolved, nil
}

func inputUserID(input tg.InputPeerClass) (int64, error) {
	switch value := input.(type) {
	case *tg.InputPeerUser:
		if value.UserID <= 0 {
			return 0, fmt.Errorf("%w: invalid target user", core.ErrInvalidArgs)
		}
		return value.UserID, nil
	default:
		return 0, fmt.Errorf("%w: target must be a user, got %T", core.ErrInvalidArgs, input)
	}
}

func inputChannel(peer tg.InputPeerClass) (*tg.InputChannel, error) {
	channel, ok := peer.(*tg.InputPeerChannel)
	if !ok || channel.ChannelID <= 0 || channel.AccessHash == 0 {
		return nil, fmt.Errorf("%w: supergroup peer is not resolved", core.ErrUnavailable)
	}
	return &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, nil
}

func inputUser(peer tg.InputPeerClass) (*tg.InputUser, error) {
	user, ok := peer.(*tg.InputPeerUser)
	if !ok || user.UserID <= 0 || user.AccessHash == 0 {
		return nil, fmt.Errorf("%w: target user is not resolved", core.ErrUnavailable)
	}
	return &tg.InputUser{UserID: user.UserID, AccessHash: user.AccessHash}, nil
}

func (m *managedGroupMutation) roleFresh(
	ctx context.Context,
	meta command.GroupMutationContext,
	peer tg.InputPeerClass,
	userID int64,
) (core.GroupActorPrincipal, error) {
	snapshot, err := m.roles.ResolveGroupRoleFresh(ctx, core.GroupRoleRequest{
		ChatID: meta.ChatID,
		Kind:   meta.Kind,
		Peer:   peer,
		UserID: userID,
	})
	if err != nil {
		return core.GroupActorPrincipal{}, err
	}
	if !snapshot.Principal.Verified || snapshot.Principal.UserID != userID {
		return core.GroupActorPrincipal{}, fmt.Errorf("%w: unverified mutation participant", core.ErrUnavailable)
	}
	return snapshot.Principal, nil
}

func (m *managedGroupMutation) botFresh(
	ctx context.Context,
	meta command.GroupMutationContext,
	peer tg.InputPeerClass,
) (core.GroupActorPrincipal, error) {
	selfID := m.selfID()
	if selfID <= 0 {
		return core.GroupActorPrincipal{}, fmt.Errorf("%w: Assistant identity is unavailable", core.ErrUnavailable)
	}
	if meta.Kind == core.ChatKindGroup {
		return m.roleFresh(ctx, meta, peer, selfID)
	}

	channel, err := inputChannel(peer)
	if err != nil {
		return core.GroupActorPrincipal{}, err
	}
	result, err := m.api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
		Channel:     channel,
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		return core.GroupActorPrincipal{}, fmt.Errorf("%w: verify Assistant group role: %v", core.ErrUnavailable, err)
	}
	if result == nil || result.Participant == nil {
		return core.GroupActorPrincipal{}, fmt.Errorf("%w: empty Assistant participant state", core.ErrUnavailable)
	}
	principal, err := assistantgroupauth.MapChannelParticipant(selfID, result.Participant)
	if err != nil {
		return core.GroupActorPrincipal{}, err
	}
	return principal, nil
}

func authorizeMutationPrincipal(
	requirement core.GroupAuthorizationRequirement,
	principal core.GroupActorPrincipal,
	who string,
) error {
	if err := requirement.Authorize(principal); err != nil {
		if errors.Is(err, core.ErrUnavailable) {
			return err
		}
		return fmt.Errorf("%w: %s lacks required Telegram rights", core.ErrGroupMutationDenied, who)
	}
	return nil
}

func (m *managedGroupMutation) validateCoordinates(
	meta command.GroupMutationContext,
	req command.GroupMutationRequest,
) error {
	if meta.ActorID <= 0 || meta.ChatID <= 0 {
		return fmt.Errorf("%w: invalid mutation actor/chat coordinates", core.ErrInvalidArgs)
	}
	switch meta.Kind {
	case core.ChatKindGroup:
		chat, ok := req.Peer.(*tg.InputPeerChat)
		if !ok || chat.ChatID != meta.ChatID {
			return fmt.Errorf("%w: basic-group mutation peer mismatch", core.ErrInvalidArgs)
		}
	case core.ChatKindSupergroup:
		channel, ok := req.Peer.(*tg.InputPeerChannel)
		if !ok || channel.ChannelID != meta.ChatID {
			return fmt.Errorf("%w: supergroup mutation peer mismatch", core.ErrInvalidArgs)
		}
	default:
		return fmt.Errorf("%w: group mutation is unavailable for chat kind %q", core.ErrUnsupported, meta.Kind)
	}
	return nil
}

func validateTargetHierarchy(
	action core.GroupMutationAction,
	kind core.ChatKind,
	actor core.GroupActorPrincipal,
	target core.GroupActorPrincipal,
	botID int64,
) error {
	if target.UserID <= 0 || !target.Verified {
		return fmt.Errorf("%w: target role is not verified", core.ErrUnavailable)
	}
	if target.UserID == actor.UserID || target.UserID == botID {
		return core.ErrGroupMutationTargetProtected
	}
	if target.Role == core.GroupActorRoleCreator {
		return core.ErrGroupMutationTargetProtected
	}
	if target.Role == core.GroupActorRoleAdministrator {
		if actor.Role != core.GroupActorRoleCreator {
			return core.ErrGroupMutationTargetProtected
		}
		if kind == core.ChatKindSupergroup && !target.CanEdit {
			return core.ErrGroupMutationTargetProtected
		}
		switch action {
		case core.GroupMutationPromote, core.GroupMutationDemote:
			// Creator may edit an editable admin.
		default:
			return core.ErrGroupMutationTargetProtected
		}
	}
	return nil
}

func mutationNoop(action core.GroupMutationAction, kind core.ChatKind, target core.GroupActorPrincipal) bool {
	switch action {
	case core.GroupMutationBan:
		return target.Role == core.GroupActorRoleBanned ||
			(kind == core.ChatKindGroup && target.Role == core.GroupActorRoleLeft)
	case core.GroupMutationUnban:
		return target.Role == core.GroupActorRoleLeft || target.Role == core.GroupActorRoleMember
	case core.GroupMutationKick:
		return target.Role == core.GroupActorRoleLeft || target.Role == core.GroupActorRoleBanned
	case core.GroupMutationUnmute:
		return target.Role == core.GroupActorRoleMember || target.Role == core.GroupActorRoleLeft
	case core.GroupMutationDemote:
		return target.Role == core.GroupActorRoleMember ||
			target.Role == core.GroupActorRoleRestricted ||
			target.Role == core.GroupActorRoleLeft
	default:
		return false
	}
}

func normalizeMutationError(action core.GroupMutationAction, err error) error {
	if err == nil {
		return nil
	}
	if tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED") {
		return nil
	}
	if (action == core.GroupMutationUnban ||
		action == core.GroupMutationUnmute ||
		action == core.GroupMutationKick ||
		action == core.GroupMutationDemote) &&
		tgerr.Is(err, "USER_NOT_PARTICIPANT") {
		return nil
	}
	if tgerr.Is(err,
		"CHAT_ADMIN_REQUIRED",
		"BOT_NOT_PARTICIPANT",
		"USER_BOT_INVALID",
		"RIGHT_FORBIDDEN",
	) {
		return fmt.Errorf("%w: Telegram rejected Assistant rights", core.ErrGroupMutationDenied)
	}
	if tgerr.Is(err, "USER_ADMIN_INVALID", "USER_CREATOR") {
		return core.ErrGroupMutationTargetProtected
	}
	if tgerr.Is(err, "PEER_ID_INVALID", "USER_ID_INVALID", "CHANNEL_INVALID", "CHAT_ID_INVALID") {
		return fmt.Errorf("%w: Telegram rejected mutation coordinates", core.ErrInvalidArgs)
	}
	return err
}

func assistantBanRights(untilDate int) tg.ChatBannedRights {
	return tg.ChatBannedRights{
		ViewMessages:    true,
		SendMessages:    true,
		SendMedia:       true,
		SendStickers:    true,
		SendGifs:        true,
		SendGames:       true,
		SendInline:      true,
		EmbedLinks:      true,
		SendPolls:       true,
		SendPhotos:      true,
		SendVideos:      true,
		SendRoundvideos: true,
		SendAudios:      true,
		SendVoices:      true,
		SendDocs:        true,
		SendPlain:       true,
		UntilDate:       untilDate,
	}
}

func assistantMuteRights(untilDate int) tg.ChatBannedRights {
	rights := assistantBanRights(untilDate)
	rights.ViewMessages = false
	return rights
}

type mutationAuthorization struct {
	actor  core.GroupActorPrincipal
	bot    core.GroupActorPrincipal
	target *core.GroupActorPrincipal
}

func promotedAdminRights(actor, bot core.GroupActorPrincipal) tg.ChatAdminRights {
	// Promotion is a delegation. Never grant a right that either the actor or
	// Assistant bot lacks. Channel-only post/edit rights are deliberately not
	// granted on the supergroup manager plane.
	return tg.ChatAdminRights{
		ChangeInfo:     actor.Rights.ChangeInfo && bot.Rights.ChangeInfo,
		DeleteMessages: actor.Rights.DeleteMessages && bot.Rights.DeleteMessages,
		BanUsers:       actor.Rights.BanUsers && bot.Rights.BanUsers,
		InviteUsers:    actor.Rights.InviteUsers && bot.Rights.InviteUsers,
		PinMessages:    actor.Rights.PinMessages && bot.Rights.PinMessages,
		AddAdmins:      actor.Rights.AddAdmins && bot.Rights.AddAdmins,
		ManageTopics:   actor.Rights.ManageTopics && bot.Rights.ManageTopics,
	}
}

func (m *managedGroupMutation) authorizeForRPC(
	ctx context.Context,
	meta command.GroupMutationContext,
	peer tg.InputPeerClass,
	action core.GroupMutationAction,
	targetID int64,
) (mutationAuthorization, error) {
	requirement, err := core.GroupMutationRequirement(action)
	if err != nil {
		return mutationAuthorization{}, err
	}

	bot, err := m.botFresh(ctx, meta, peer)
	if err != nil {
		return mutationAuthorization{}, err
	}
	if err := authorizeMutationPrincipal(requirement, bot, "Assistant bot"); err != nil {
		return mutationAuthorization{}, err
	}

	actor, err := m.roleFresh(ctx, meta, peer, meta.ActorID)
	if err != nil {
		return mutationAuthorization{}, err
	}
	if err := authorizeMutationPrincipal(requirement, actor, "actor"); err != nil {
		return mutationAuthorization{}, err
	}

	authorization := mutationAuthorization{actor: actor, bot: bot}
	if targetID > 0 {
		// Target is resolved last so hierarchy/no-op decisions use the freshest
		// participant snapshot immediately before the physical mutation RPC.
		target, err := m.roleFresh(ctx, meta, peer, targetID)
		if err != nil {
			return mutationAuthorization{}, err
		}
		if err := validateTargetHierarchy(action, meta.Kind, actor, target, m.selfID()); err != nil {
			return mutationAuthorization{}, err
		}
		authorization.target = &target
	}
	return authorization, nil
}

func (m *managedGroupMutation) targetedPeer(
	ctx context.Context,
	meta command.GroupMutationContext,
	targetPeer tg.InputPeerClass,
) (tg.InputPeerClass, int64, error) {
	targetPeer, err := m.resolvePeer(ctx, targetPeer)
	if err != nil {
		return nil, 0, err
	}
	targetID, err := inputUserID(targetPeer)
	if err != nil {
		return nil, 0, err
	}
	if targetID == meta.ActorID || targetID == m.selfID() {
		return nil, 0, core.ErrGroupMutationTargetProtected
	}
	return targetPeer, targetID, nil
}

func (m *managedGroupMutation) Execute(
	ctx context.Context,
	meta command.GroupMutationContext,
	req command.GroupMutationRequest,
) (command.GroupMutationResult, error) {
	if m == nil || m.api == nil || m.peers == nil || m.roles == nil || m.selfID == nil {
		return command.GroupMutationResult{}, command.ErrGroupMutationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return command.GroupMutationResult{}, err
	}
	if _, err := core.GroupMutationRequirement(req.Action); err != nil {
		return command.GroupMutationResult{}, err
	}
	if err := m.validateCoordinates(meta, req); err != nil {
		return command.GroupMutationResult{}, err
	}

	peer, err := m.resolvePeer(ctx, req.Peer)
	if err != nil {
		return command.GroupMutationResult{}, err
	}
	req.Peer = peer

	var authorization mutationAuthorization
	if core.GroupMutationTargetsParticipant(req.Action) {
		targetPeer, targetID, err := m.targetedPeer(ctx, meta, req.Target)
		if err != nil {
			return command.GroupMutationResult{}, err
		}
		req.Target = targetPeer
		authorization, err = m.authorizeForRPC(ctx, meta, peer, req.Action, targetID)
		if err != nil {
			return command.GroupMutationResult{}, err
		}
		if authorization.target != nil && mutationNoop(req.Action, meta.Kind, *authorization.target) {
			return command.GroupMutationResult{}, nil
		}
	} else if req.Action != core.GroupMutationPurge {
		authorization, err = m.authorizeForRPC(ctx, meta, peer, req.Action, 0)
		if err != nil {
			return command.GroupMutationResult{}, err
		}
	}

	switch req.Action {
	case core.GroupMutationBan:
		return command.GroupMutationResult{}, m.ban(ctx, meta.Kind, req)
	case core.GroupMutationUnban:
		return command.GroupMutationResult{}, m.unban(ctx, meta.Kind, req)
	case core.GroupMutationKick:
		return command.GroupMutationResult{}, m.kick(ctx, meta, req)
	case core.GroupMutationMute:
		return command.GroupMutationResult{}, m.mute(ctx, meta.Kind, req)
	case core.GroupMutationUnmute:
		return command.GroupMutationResult{}, m.unmute(ctx, meta.Kind, req)
	case core.GroupMutationPin:
		return command.GroupMutationResult{}, m.pin(ctx, req, false)
	case core.GroupMutationUnpin:
		return command.GroupMutationResult{}, m.pin(ctx, req, true)
	case core.GroupMutationPurge:
		deleted, purgeErr := m.purge(ctx, meta, req)
		return command.GroupMutationResult{Deleted: deleted}, purgeErr
	case core.GroupMutationPromote:
		return command.GroupMutationResult{}, m.promote(ctx, meta.Kind, req, authorization)
	case core.GroupMutationDemote:
		return command.GroupMutationResult{}, m.demote(ctx, meta.Kind, req)
	case core.GroupMutationDefaultPermissions:
		return command.GroupMutationResult{}, m.editDefaultPermissions(ctx, req)
	default:
		return command.GroupMutationResult{}, fmt.Errorf("%w: unsupported group mutation %q", core.ErrUnsupported, req.Action)
	}
}

func (m *managedGroupMutation) ban(ctx context.Context, kind core.ChatKind, req command.GroupMutationRequest) error {
	switch kind {
	case core.ChatKindSupergroup:
		channel, err := inputChannel(req.Peer)
		if err != nil {
			return err
		}
		_, err = m.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel: channel, Participant: req.Target, BannedRights: assistantBanRights(req.UntilDate),
		})
		return normalizeMutationError(req.Action, err)
	case core.ChatKindGroup:
		user, err := inputUser(req.Target)
		if err != nil {
			return err
		}
		_, err = m.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
			ChatID: req.Peer.(*tg.InputPeerChat).ChatID,
			UserID: user,
		})
		if err != nil && tgerr.Is(err, "USER_NOT_PARTICIPANT") {
			// Basic-group ban/kick is a removal desired state. If a retry or
			// concurrent actor already removed the target, the state is reached.
			return nil
		}
		return normalizeMutationError(req.Action, err)
	default:
		return core.ErrUnsupported
	}
}

func (m *managedGroupMutation) unban(ctx context.Context, kind core.ChatKind, req command.GroupMutationRequest) error {
	if kind != core.ChatKindSupergroup {
		return fmt.Errorf("%w: unban requires a supergroup", core.ErrUnsupported)
	}
	channel, err := inputChannel(req.Peer)
	if err != nil {
		return err
	}
	_, err = m.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel: channel, Participant: req.Target, BannedRights: tg.ChatBannedRights{},
	})
	return normalizeMutationError(req.Action, err)
}

func (m *managedGroupMutation) kick(
	ctx context.Context,
	meta command.GroupMutationContext,
	req command.GroupMutationRequest,
) error {
	if meta.Kind == core.ChatKindGroup {
		return m.ban(ctx, meta.Kind, req)
	}
	if meta.Kind != core.ChatKindSupergroup {
		return core.ErrUnsupported
	}
	channel, err := inputChannel(req.Peer)
	if err != nil {
		return err
	}
	kickUntil := int(m.now().Unix() + 60)
	if _, err := m.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel: channel,
		Participant: req.Target,
		BannedRights: tg.ChatBannedRights{ViewMessages: true, UntilDate: kickUntil},
	}); err != nil {
		return normalizeMutationError(req.Action, err)
	}

	targetID, err := inputUserID(req.Target)
	if err != nil {
		return err
	}
	// Kick is a two-step desired-state operation. The unban step is a second
	// physical mutation and therefore gets a second fresh actor/bot/target gate.
	if _, err := m.authorizeForRPC(ctx, meta, req.Peer, req.Action, targetID); err != nil {
		return err
	}
	_, err = m.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel: channel,
		Participant: req.Target,
		BannedRights: tg.ChatBannedRights{},
	})
	return normalizeMutationError(req.Action, err)
}

func (m *managedGroupMutation) mute(ctx context.Context, kind core.ChatKind, req command.GroupMutationRequest) error {
	if kind != core.ChatKindSupergroup {
		return fmt.Errorf("%w: mute requires a supergroup", core.ErrUnsupported)
	}
	channel, err := inputChannel(req.Peer)
	if err != nil {
		return err
	}
	_, err = m.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel: channel, Participant: req.Target, BannedRights: assistantMuteRights(req.UntilDate),
	})
	return normalizeMutationError(req.Action, err)
}

func (m *managedGroupMutation) unmute(ctx context.Context, kind core.ChatKind, req command.GroupMutationRequest) error {
	if kind != core.ChatKindSupergroup {
		return fmt.Errorf("%w: unmute requires a supergroup", core.ErrUnsupported)
	}
	channel, err := inputChannel(req.Peer)
	if err != nil {
		return err
	}
	_, err = m.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel: channel, Participant: req.Target, BannedRights: tg.ChatBannedRights{},
	})
	return normalizeMutationError(req.Action, err)
}

func (m *managedGroupMutation) pin(ctx context.Context, req command.GroupMutationRequest, unpin bool) error {
	if req.MessageID <= 0 {
		return fmt.Errorf("%w: pin requires a positive message id", core.ErrInvalidArgs)
	}
	request := &tg.MessagesUpdatePinnedMessageRequest{
		Peer: req.Peer, ID: req.MessageID, Silent: req.Silent, Unpin: unpin,
	}
	if req.Silent {
		request.SetSilent(true)
	}
	if unpin {
		request.SetUnpin(true)
	}
	_, err := m.api.MessagesUpdatePinnedMessage(ctx, request)
	return normalizeMutationError(req.Action, err)
}

func (m *managedGroupMutation) promote(
	ctx context.Context,
	kind core.ChatKind,
	req command.GroupMutationRequest,
	authorization mutationAuthorization,
) error {
	user, err := inputUser(req.Target)
	if err != nil {
		return err
	}
	switch kind {
	case core.ChatKindSupergroup:
		channel, err := inputChannel(req.Peer)
		if err != nil {
			return err
		}
		request := &tg.ChannelsEditAdminRequest{
			Channel:     channel,
			UserID:      user,
			AdminRights: promotedAdminRights(authorization.actor, authorization.bot),
		}
		if req.Title != "" {
			request.SetRank(req.Title)
		}
		_, err = m.api.ChannelsEditAdmin(ctx, request)
		return normalizeMutationError(req.Action, err)
	case core.ChatKindGroup:
		_, err := m.api.MessagesEditChatAdmin(ctx, &tg.MessagesEditChatAdminRequest{
			ChatID: req.Peer.(*tg.InputPeerChat).ChatID, UserID: user, IsAdmin: true,
		})
		return normalizeMutationError(req.Action, err)
	default:
		return core.ErrUnsupported
	}
}

func (m *managedGroupMutation) demote(ctx context.Context, kind core.ChatKind, req command.GroupMutationRequest) error {
	user, err := inputUser(req.Target)
	if err != nil {
		return err
	}
	switch kind {
	case core.ChatKindSupergroup:
		channel, err := inputChannel(req.Peer)
		if err != nil {
			return err
		}
		_, err = m.api.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{
			Channel: channel, UserID: user, AdminRights: tg.ChatAdminRights{},
		})
		return normalizeMutationError(req.Action, err)
	case core.ChatKindGroup:
		_, err := m.api.MessagesEditChatAdmin(ctx, &tg.MessagesEditChatAdminRequest{
			ChatID: req.Peer.(*tg.InputPeerChat).ChatID, UserID: user, IsAdmin: false,
		})
		return normalizeMutationError(req.Action, err)
	default:
		return core.ErrUnsupported
	}
}

func (m *managedGroupMutation) editDefaultPermissions(ctx context.Context, req command.GroupMutationRequest) error {
	_, err := m.api.MessagesEditChatDefaultBannedRights(ctx, &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer: req.Peer, BannedRights: req.DefaultRights,
	})
	return normalizeMutationError(req.Action, err)
}

func messagesFromResult(result tg.MessagesMessagesClass) []tg.MessageClass {
	if result == nil {
		return nil
	}
	if modified, ok := result.AsModified(); ok {
		return modified.GetMessages()
	}
	return nil
}

func (m *managedGroupMutation) collectPurgeIDs(
	ctx context.Context,
	req command.GroupMutationRequest,
) ([]int, error) {
	minID, maxID := req.FromID, req.ToID
	if minID <= 0 || maxID <= 0 {
		return nil, fmt.Errorf("%w: purge requires positive message bounds", core.ErrInvalidArgs)
	}
	if minID > maxID {
		minID, maxID = maxID, minID
	}
	ids := map[int]struct{}{minID: {}, maxID: {}}
	offsetID := maxID + 1

	for len(ids) < maxAssistantPurgeMessages {
		var (
			result tg.MessagesMessagesClass
			err    error
		)
		if req.TopicID > 0 {
			result, err = m.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
				Peer: req.Peer, MsgID: req.TopicID, OffsetID: offsetID,
				MinID: minID - 1, MaxID: maxID + 1, Limit: 100,
			})
		} else {
			result, err = m.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer: req.Peer, OffsetID: offsetID,
				MinID: minID - 1, MaxID: maxID + 1, Limit: 100,
			})
		}
		if err != nil {
			return nil, err
		}
		messages := messagesFromResult(result)
		if len(messages) == 0 {
			break
		}
		lowest := offsetID
		newFound := 0
		for _, item := range messages {
			msg, ok := item.(*tg.Message)
			if !ok {
				continue
			}
			if msg.ID < lowest {
				lowest = msg.ID
			}
			if msg.ID >= minID && msg.ID <= maxID {
				if _, exists := ids[msg.ID]; !exists {
					ids[msg.ID] = struct{}{}
					newFound++
					if len(ids) >= maxAssistantPurgeMessages {
						break
					}
				}
			}
		}
		if lowest >= offsetID || lowest <= minID || newFound == 0 {
			break
		}
		offsetID = lowest
	}

	all := make([]int, 0, len(ids))
	for id := range ids {
		all = append(all, id)
	}
	sort.Ints(all)
	return all, nil
}

func (m *managedGroupMutation) purge(
	ctx context.Context,
	meta command.GroupMutationContext,
	req command.GroupMutationRequest,
) (int, error) {
	if _, err := m.authorizeForRPC(ctx, meta, req.Peer, req.Action, 0); err != nil {
		return 0, err
	}
	ids, err := m.collectPurgeIDs(ctx, req)
	if err != nil {
		return 0, err
	}

	total := 0
	const chunkSize = 100
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		// Each physical delete RPC gets a fresh actor+bot authorization fence.
		if _, err := m.authorizeForRPC(ctx, meta, req.Peer, req.Action, 0); err != nil {
			return total, err
		}

		chunk := ids[start:end]
		switch meta.Kind {
		case core.ChatKindSupergroup:
			channel, err := inputChannel(req.Peer)
			if err != nil {
				return total, err
			}
			_, err = m.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: channel, ID: chunk,
			})
			if err := normalizeMutationError(req.Action, err); err != nil {
				return total, err
			}
		case core.ChatKindGroup:
			_, err := m.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
				Revoke: true, ID: chunk,
			})
			if err := normalizeMutationError(req.Action, err); err != nil {
				return total, err
			}
		default:
			return total, core.ErrUnsupported
		}
		total += len(chunk)
	}
	return total, nil
}
