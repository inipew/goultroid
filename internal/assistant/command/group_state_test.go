package command_test

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

type p7fCommandStateStore struct {
	casCalls int
	lastCAS  core.GroupStateCAS
}

func (*p7fCommandStateStore) Get(context.Context, core.GroupStateKey) (core.GroupStateRecord, error) {
	return core.GroupStateRecord{}, core.ErrNotFound
}

func (s *p7fCommandStateStore) CompareAndSwap(_ context.Context, req core.GroupStateCAS) (core.GroupStateRecord, error) {
	s.casCalls++
	s.lastCAS = req
	return core.GroupStateRecord{
		GroupStateKey: req.GroupStateKey,
		Value:         append([]byte(nil), req.Value...),
		Revision:      req.ExpectedRevision + 1,
		UpdatedBy:     req.UpdatedBy,
		UpdatedAt:     req.UpdatedAt,
		ExpiresAt:     req.ExpiresAt,
	}, nil
}

func (*p7fCommandStateStore) DeleteCompareAndSwap(context.Context, core.GroupStateDelete) error {
	return nil
}

func (*p7fCommandStateStore) PruneExpired(context.Context, time.Time, int) (int, error) { return 0, nil }
func (*p7fCommandStateStore) Count(context.Context) (int, error)                        { return 0, nil }

type p7fSequentialRoleResolver struct {
	cached     core.GroupActorPrincipal
	fresh      []core.GroupActorPrincipal
	cachedCall int
	freshCall  int
}

func (r *p7fSequentialRoleResolver) ResolveGroupRole(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.cachedCall++
	principal := r.cached
	principal.UserID = req.UserID
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

func (r *p7fSequentialRoleResolver) ResolveGroupRoleFresh(_ context.Context, req core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	index := r.freshCall
	r.freshCall++
	if index >= len(r.fresh) {
		index = len(r.fresh) - 1
	}
	principal := r.fresh[index]
	principal.UserID = req.UserID
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

func p7fPrincipal(role core.GroupActorRole) core.GroupActorPrincipal {
	return core.GroupActorPrincipal{Role: role, Verified: true}
}

func TestP7FGroupStateWriteRevalidatesAgainImmediatelyBeforePersistence(t *testing.T) {
	taskClient := &p7cTaskClient{}
	roles := &p7fSequentialRoleResolver{
		cached: p7fPrincipal(core.GroupActorRoleAdministrator),
		fresh: []core.GroupActorPrincipal{
			p7fPrincipal(core.GroupActorRoleAdministrator),
			p7fPrincipal(core.GroupActorRoleAdministrator),
		},
	}
	state := &p7fCommandStateStore{}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(taskClient)
	router.SetGroupRoleResolver(roles)
	router.SetGroupStateStore(state)

	requirement := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:               "groupstatewrite",
		Invocation:         core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:           execution.SurfaceAssistant,
		GroupOnly:          true,
		GroupAuthorization: requirement,
		Handler: func(c *core.Context) error {
			_, err := c.CompareAndSwapGroupState(
				requirement,
				"manager",
				"mode",
				0,
				[]byte("strict"),
				0,
			)
			return err
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/groupstatewrite",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 1},
		&fakeInteraction{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if taskClient.submits != 1 || roles.cachedCall != 1 || roles.freshCall != 2 {
		t.Fatalf("execution counts submits=%d cached=%d fresh=%d, want 1/1/2",
			taskClient.submits, roles.cachedCall, roles.freshCall)
	}
	if state.casCalls != 1 {
		t.Fatalf("state CAS calls=%d, want 1", state.casCalls)
	}
	if state.lastCAS.ChatID != 55 || state.lastCAS.Namespace != "manager" ||
		state.lastCAS.Key != "mode" || state.lastCAS.UpdatedBy != 42 {
		t.Fatalf("unexpected persisted coordinate=%+v", state.lastCAS)
	}
}

func TestP7FGroupStateWriteStopsWhenRoleRevokedBeforePersistence(t *testing.T) {
	taskClient := &p7cTaskClient{}
	roles := &p7fSequentialRoleResolver{
		cached: p7fPrincipal(core.GroupActorRoleAdministrator),
		fresh: []core.GroupActorPrincipal{
			p7fPrincipal(core.GroupActorRoleAdministrator),
			p7fPrincipal(core.GroupActorRoleMember),
		},
	}
	state := &p7fCommandStateStore{}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(taskClient)
	router.SetGroupRoleResolver(roles)
	router.SetGroupStateStore(state)

	requirement := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:               "groupstaterevoked",
		Invocation:         core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:           execution.SurfaceAssistant,
		GroupOnly:          true,
		GroupAuthorization: requirement,
		Handler: func(c *core.Context) error {
			_, err := c.CompareAndSwapGroupState(
				requirement,
				"manager",
				"mode",
				0,
				[]byte("must-not-commit"),
				0,
			)
			return err
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/groupstaterevoked",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 2},
		&fakeInteraction{},
	)
	if err == nil {
		t.Fatal("role revocation before persistence unexpectedly succeeded")
	}
	if roles.cachedCall != 1 || roles.freshCall != 2 || taskClient.submits != 1 {
		t.Fatalf("revocation counts submits=%d cached=%d fresh=%d",
			taskClient.submits, roles.cachedCall, roles.freshCall)
	}
	if state.casCalls != 0 {
		t.Fatalf("revoked authority reached persistence %d times", state.casCalls)
	}
}
