package native

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

type a5GroupRoles struct {
	fresh    core.GroupActorPrincipal
	freshErr error
	requests []core.GroupRoleRequest
	cached   int
}

func (r *a5GroupRoles) ResolveGroupRole(context.Context, core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.cached++
	return core.GroupRoleSnapshot{Principal: core.GroupActorPrincipal{Verified: true, Role: core.GroupActorRoleCreator}}, nil
}

func (r *a5GroupRoles) ResolveGroupRoleFresh(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.requests = append(r.requests, request)
	if r.freshErr != nil {
		return core.GroupRoleSnapshot{}, r.freshErr
	}
	return core.GroupRoleSnapshot{Principal: r.fresh}, nil
}

func a5GroupMutationCase(user, chat int64) (interaction.Session, presentationtelegram.MessageTarget, GroupActionScope) {
	return interaction.Session{
		Binding: interaction.Binding{ActorID: user, ChatID: chat, MessageID: 80},
	}, presentationtelegram.MessageTarget{
		Peer:   &tg.InputPeerChannel{ChannelID: chat, AccessHash: 771},
		ChatID: chat, MessageID: 80,
	}, GroupActionScope{ChatID: chat, Kind: core.ChatKindSupergroup, TopicID: 55}
}

func TestA5CGroupActionAuthorizationFreshRoleAndDemotion(t *testing.T) {
	session, target, scope := a5GroupMutationCase(123, 500)
	roles := &a5GroupRoles{fresh: core.GroupActorPrincipal{
		UserID: 123, Role: core.GroupActorRoleAdministrator, Verified: true,
		Rights: core.GroupAdminRights{DeleteMessages: true},
	}}
	require := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator,
		Rights: core.GroupAdminRights{DeleteMessages: true}}
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, require); err != nil {
		t.Fatal(err)
	}
	if len(roles.requests) != 1 || roles.requests[0].ChatID != 500 ||
		roles.requests[0].UserID != 123 || roles.requests[0].Kind != core.ChatKindSupergroup || roles.cached != 0 {
		t.Fatalf("role request was not authoritative or did not match chat/actor: %+v", roles)
	}
	roles.fresh.Role = core.GroupActorRoleMember
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, require); !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("demoted administrator retained mutation authority: %v", err)
	}
	roles.fresh.Role = core.GroupActorRoleAdministrator
	roles.fresh.Rights.DeleteMessages = false
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, require); !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("missing delete rights still permitted blacklist mutation: %v", err)
	}
}

func TestA5CGroupActionBindingPreventsCrossChatAndInvalidPeer(t *testing.T) {
	session, target, scope := a5GroupMutationCase(123, 500)
	require := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
	cases := []struct {
		name    string
		session interaction.Session
		target  presentationtelegram.MessageTarget
		scope   GroupActionScope
	}{
		{name: "cross chat scope", session: session, target: target, scope: GroupActionScope{ChatID: 600, Kind: core.ChatKindSupergroup}},
		{name: "cross chat target", session: session, target: presentationtelegram.MessageTarget{Peer: &tg.InputPeerChannel{ChannelID: 600, AccessHash: 1}, ChatID: 600, MessageID: 80}, scope: scope},
		{name: "peer identity mismatch", session: session, target: presentationtelegram.MessageTarget{Peer: &tg.InputPeerChannel{ChannelID: 600, AccessHash: 1}, ChatID: 500, MessageID: 80}, scope: scope},
		{name: "missing access hash", session: session, target: presentationtelegram.MessageTarget{Peer: &tg.InputPeerChannel{ChannelID: 500}, ChatID: 500, MessageID: 80}, scope: scope},
		{name: "private spoof", session: session, target: presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: 500, AccessHash: 1}, ChatID: 500, MessageID: 80}, scope: scope},
		{name: "channel scope", session: session, target: target, scope: GroupActionScope{ChatID: 500, Kind: core.ChatKindChannel}},
		{name: "unbound actor", session: interaction.Session{Binding: interaction.Binding{ChatID: 500, MessageID: 80}}, target: target, scope: scope},
		{name: "unbound message", session: interaction.Session{Binding: interaction.Binding{ActorID: 123, ChatID: 500}}, target: target, scope: scope},
		{name: "wrong topic", session: session, target: target, scope: GroupActionScope{ChatID: 500, Kind: core.ChatKindSupergroup, TopicID: -1}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			roles := &a5GroupRoles{fresh: core.GroupActorPrincipal{Verified: true, UserID: 123, Role: core.GroupActorRoleCreator}}
			if err := AuthorizeFreshGroupAction(context.Background(), tt.session, tt.target, tt.scope, roles, require); err == nil {
				t.Fatal("invalid chat actor or target authorized")
			}
			if len(roles.requests) != 0 {
				t.Fatal("invalid group callback reached Telegram role lookup")
			}
		})
	}
}

func TestA5CGroupActionFailsClosedOnRoleOutageAndUnverifiedIdentity(t *testing.T) {
	session, target, scope := a5GroupMutationCase(123, 500)
	require := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
	roles := &a5GroupRoles{freshErr: errors.New("telegram unavailable")}
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, require); err == nil {
		t.Fatal("role RPC failure was accepted")
	}
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, nil, require); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("missing role resolver: %v", err)
	}
	roles.freshErr = nil
	roles.fresh = core.GroupActorPrincipal{UserID: 999, Role: core.GroupActorRoleCreator, Verified: true}
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, require); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("mismatched principal authorized: %v", err)
	}
	roles.fresh.UserID = 123
	roles.fresh.Verified = false
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, require); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("unverified principal authorized: %v", err)
	}
}

func TestA5CGroupActionCannotUseNoopAuthorizationRequirement(t *testing.T) {
	session, target, scope := a5GroupMutationCase(123, 500)
	roles := &a5GroupRoles{fresh: core.GroupActorPrincipal{UserID: 123, Role: core.GroupActorRoleCreator, Verified: true}}
	if err := AuthorizeFreshGroupAction(context.Background(), session, target, scope, roles, core.GroupAuthorizationRequirement{}); !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("empty policy allowed mutation: %v", err)
	}
	if len(roles.requests) != 0 {
		t.Fatal("empty policy must reject before role lookup")
	}
}
