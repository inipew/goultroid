package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type p7fRoleResolver struct {
	principal  GroupActorPrincipal
	cachedCall int
	freshCall  int
}

func (r *p7fRoleResolver) ResolveGroupRole(_ context.Context, req GroupRoleRequest) (GroupRoleSnapshot, error) {
	r.cachedCall++
	principal := r.principal
	principal.UserID = req.UserID
	return GroupRoleSnapshot{Principal: principal}, nil
}

func (r *p7fRoleResolver) ResolveGroupRoleFresh(_ context.Context, req GroupRoleRequest) (GroupRoleSnapshot, error) {
	r.freshCall++
	principal := r.principal
	principal.UserID = req.UserID
	return GroupRoleSnapshot{Principal: principal}, nil
}

type p7fStateStore struct {
	getKey      GroupStateKey
	cas         GroupStateCAS
	del         GroupStateDelete
	getCalls    int
	casCalls    int
	deleteCalls int
}

func (s *p7fStateStore) Get(_ context.Context, key GroupStateKey) (GroupStateRecord, error) {
	s.getCalls++
	s.getKey = key
	return GroupStateRecord{GroupStateKey: key, Revision: 2, Value: []byte("value")}, nil
}

func (s *p7fStateStore) CompareAndSwap(_ context.Context, _ GroupStateWriteGrant, req GroupStateCAS) (GroupStateRecord, error) {
	s.casCalls++
	s.cas = req
	return GroupStateRecord{
		GroupStateKey: req.GroupStateKey,
		Value:         append([]byte(nil), req.Value...),
		Revision:      req.ExpectedRevision + 1,
		UpdatedBy:     req.UpdatedBy,
		UpdatedAt:     req.UpdatedAt,
		ExpiresAt:     req.ExpiresAt,
	}, nil
}

func (s *p7fStateStore) DeleteCompareAndSwap(_ context.Context, _ GroupStateWriteGrant, req GroupStateDelete) error {
	s.deleteCalls++
	s.del = req
	return nil
}

func (*p7fStateStore) PruneExpired(context.Context, time.Time, int) (int, error) { return 0, nil }
func (*p7fStateStore) Count(context.Context) (int, error)                        { return 0, nil }

func newP7FContext(role GroupActorRole, rights GroupAdminRights) (*Context, *p7fRoleResolver, *p7fStateStore) {
	resolver := &p7fRoleResolver{
		principal: GroupActorPrincipal{
			Role:     role,
			Rights:   rights,
			Verified: true,
		},
	}
	store := &p7fStateStore{}
	ctx := &Context{
		Ctx:    context.Background(),
		Source: ExecutionAssistant,
		Chat:   &Chat{ID: 99, Type: "supergroup"},
		Sender: &User{ID: 42},
		Principal: &Principal{
			UserID:  42,
			IsOwner: true,
			IsSudo:  true,
			Level:   PermissionOwner,
		},
		GroupRoles: resolver,
	}
	AttachGroupStateStore(ctx, store)
	return ctx, resolver, store
}

func TestContextGroupStateReadIsExplicitlyChatScoped(t *testing.T) {
	ctx, resolver, store := newP7FContext(GroupActorRoleMember, GroupAdminRights{})
	record, err := ctx.GetGroupState(" Manager ", " MODE ")
	if err != nil {
		t.Fatal(err)
	}
	if record.ChatID != 99 || store.getKey.ChatID != 99 ||
		store.getKey.Namespace != "manager" || store.getKey.Key != "mode" {
		t.Fatalf("unexpected group-state coordinate record=%+v key=%+v", record, store.getKey)
	}
	if resolver.cachedCall != 0 || resolver.freshCall != 0 {
		t.Fatalf("read unexpectedly resolved contextual role cached=%d fresh=%d", resolver.cachedCall, resolver.freshCall)
	}
}

func TestContextGroupStateCASFreshAuthorizesImmediatelyBeforeStore(t *testing.T) {
	ctx, resolver, store := newP7FContext(
		GroupActorRoleAdministrator,
		GroupAdminRights{BanUsers: true},
	)
	requirement := GroupAuthorizationRequirement{
		Level:  GroupAuthorizationAdministrator,
		Rights: GroupAdminRights{BanUsers: true},
	}

	record, err := ctx.CompareAndSwapGroupState(
		requirement,
		"moderation",
		"mode",
		0,
		[]byte("strict"),
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.cachedCall != 0 || resolver.freshCall != 1 {
		t.Fatalf("write auth calls cached=%d fresh=%d, want 0/1", resolver.cachedCall, resolver.freshCall)
	}
	if store.casCalls != 1 {
		t.Fatalf("CAS calls=%d, want 1", store.casCalls)
	}
	if store.cas.ChatID != 99 || store.cas.UpdatedBy != 42 ||
		store.cas.Namespace != "moderation" || store.cas.Key != "mode" ||
		string(store.cas.Value) != "strict" || store.cas.ExpiresAt == nil {
		t.Fatalf("unexpected CAS request=%+v", store.cas)
	}
	if record.Revision != 1 {
		t.Fatalf("record revision=%d, want 1", record.Revision)
	}
}

func TestContextGroupStateOwnerSudoCannotBypassTelegramAdminRole(t *testing.T) {
	ctx, resolver, store := newP7FContext(GroupActorRoleMember, GroupAdminRights{})
	_, err := ctx.CompareAndSwapGroupState(
		GroupAuthorizationRequirement{Level: GroupAuthorizationAdministrator},
		"moderation",
		"mode",
		0,
		[]byte("strict"),
		0,
	)
	if !errors.Is(err, ErrGroupAuthorizationDenied) {
		t.Fatalf("member owner/sudo write error=%v, want ErrGroupAuthorizationDenied", err)
	}
	if resolver.freshCall != 1 {
		t.Fatalf("fresh calls=%d, want 1", resolver.freshCall)
	}
	if store.casCalls != 0 {
		t.Fatalf("unauthorized write reached store %d times", store.casCalls)
	}
}

func TestContextGroupStateRejectsMemberLevelWriteRequirement(t *testing.T) {
	ctx, resolver, store := newP7FContext(GroupActorRoleAdministrator, GroupAdminRights{})
	_, err := ctx.CompareAndSwapGroupState(
		GroupAuthorizationRequirement{Level: GroupAuthorizationMember},
		"manager",
		"state",
		0,
		[]byte("x"),
		0,
	)
	if !errors.Is(err, ErrGroupAuthorizationDenied) {
		t.Fatalf("member-level write requirement error=%v", err)
	}
	if resolver.freshCall != 0 || store.casCalls != 0 {
		t.Fatalf("invalid write requirement did work fresh=%d cas=%d", resolver.freshCall, store.casCalls)
	}
}

func TestContextGroupStateDeleteFreshAuthorizesAndUsesExactRevision(t *testing.T) {
	ctx, resolver, store := newP7FContext(GroupActorRoleCreator, GroupAdminRights{})
	err := ctx.DeleteGroupState(
		GroupAuthorizationRequirement{Level: GroupAuthorizationCreator},
		"manager",
		"state",
		7,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.freshCall != 1 || store.deleteCalls != 1 {
		t.Fatalf("delete path fresh=%d store=%d", resolver.freshCall, store.deleteCalls)
	}
	if store.del.ChatID != 99 || store.del.ExpectedRevision != 7 || store.del.DeletedBy != 42 {
		t.Fatalf("delete request=%+v", store.del)
	}
}
