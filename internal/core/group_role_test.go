package core

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

type groupRoleResolverStub struct {
	snapshot  GroupRoleSnapshot
	freshCall bool
	calls     int
}

func (s *groupRoleResolverStub) ResolveGroupRole(context.Context, GroupRoleRequest) (GroupRoleSnapshot, error) {
	s.calls++
	s.freshCall = false
	return s.snapshot, nil
}

func (s *groupRoleResolverStub) ResolveGroupRoleFresh(context.Context, GroupRoleRequest) (GroupRoleSnapshot, error) {
	s.calls++
	s.freshCall = true
	return s.snapshot, nil
}

func TestContextResolveGroupActorMergesGlobalIdentity(t *testing.T) {
	resolver := &groupRoleResolverStub{snapshot: GroupRoleSnapshot{
		Principal: GroupActorPrincipal{
			UserID:   42,
			Role:     GroupActorRoleAdministrator,
			Rights:   GroupAdminRights{BanUsers: true},
			Verified: true,
		},
		ObservedAt: time.Now(),
	}}
	ctx := &Context{
		Ctx:        context.Background(),
		Source:     ExecutionAssistant,
		Command:    "manager",
		Chat:       &Chat{ID: 99, Type: "supergroup"},
		PeerID:     &tg.InputPeerChannel{ChannelID: 99, AccessHash: 7},
		Sender:     &User{ID: 42},
		Perms:      NewPermissions(42, nil),
		Principal:  &Principal{UserID: 42, IsOwner: true, IsSudo: true, Level: PermissionOwner},
		GroupRoles: resolver,
	}

	snapshot, err := ctx.ResolveGroupActor(false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Principal.Role != GroupActorRoleAdministrator ||
		!snapshot.Principal.Rights.BanUsers ||
		!snapshot.Principal.IsOwner ||
		!snapshot.Principal.IsSudo {
		t.Fatalf("unexpected merged principal: %+v", snapshot.Principal)
	}
	group, ok := ctx.GroupExecution()
	if !ok || group.Actor.Role != GroupActorRoleAdministrator || !group.Actor.Verified {
		t.Fatalf("resolved contextual principal not visible in GroupExecution: %+v", group)
	}
	if resolver.calls != 1 || resolver.freshCall {
		t.Fatalf("unexpected resolver calls=%d fresh=%v", resolver.calls, resolver.freshCall)
	}
}

func TestContextResolveGroupActorFreshUsesFreshPath(t *testing.T) {
	resolver := &groupRoleResolverStub{snapshot: GroupRoleSnapshot{
		Principal: GroupActorPrincipal{UserID: 7, Role: GroupActorRoleMember, Verified: true},
	}}
	ctx := &Context{
		Ctx:        context.Background(),
		Chat:       &Chat{ID: 1, Type: "group"},
		PeerID:     &tg.InputPeerChat{ChatID: 1},
		Sender:     &User{ID: 7},
		GroupRoles: resolver,
	}
	if _, err := ctx.ResolveGroupActor(true); err != nil {
		t.Fatal(err)
	}
	if !resolver.freshCall {
		t.Fatal("fresh role resolution did not bypass cache")
	}
}
