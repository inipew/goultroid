package command_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type p7cTicket struct {
	id   tasks.TaskID
	res  tasks.TaskResult
	done chan struct{}
}

func (t *p7cTicket) TaskID() tasks.TaskID                           { return t.id }
func (t *p7cTicket) State() tasks.TaskState                         { return tasks.StateCompleted }
func (t *p7cTicket) Done() <-chan struct{}                          { return t.done }
func (t *p7cTicket) Result() (tasks.TaskResult, bool)               { return t.res, true }
func (t *p7cTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.res, nil }

type p7cTaskClient struct {
	submits int
	last    tasks.WorkSpec
}

func (c *p7cTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.submits++
	c.last = spec
	res := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err := spec.Handler(ctx); err != nil {
		res.Outcome = tasks.OutcomeFailed
		res.Failure.Message = err.Error()
	}
	done := make(chan struct{})
	close(done)
	return &p7cTicket{id: spec.ID, res: res, done: done}, nil
}

func (c *p7cTaskClient) Cancel(id tasks.TaskID, cause tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Accepted: true, Reason: cause}, nil
}

func (c *p7cTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *p7cTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type p7cRoleResolver struct {
	tasks                *p7cTaskClient
	cached               core.GroupActorPrincipal
	fresh                core.GroupActorPrincipal
	cachedCalls          int
	freshCalls           int
	cachedBeforeSubmit   bool
	freshAfterSubmission bool
}

func (r *p7cRoleResolver) ResolveGroupRole(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.cachedCalls++
	r.cachedBeforeSubmit = r.tasks == nil || r.tasks.submits == 0
	principal := r.cached
	principal.UserID = request.UserID
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

func (r *p7cRoleResolver) ResolveGroupRoleFresh(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.freshCalls++
	r.freshAfterSubmission = r.tasks != nil && r.tasks.submits > 0
	principal := r.fresh
	principal.UserID = request.UserID
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

func p7cVerified(role core.GroupActorRole, rights core.GroupAdminRights) core.GroupActorPrincipal {
	return core.GroupActorPrincipal{Role: role, Rights: rights, Verified: true}
}

func p7cDispatch(
	t *testing.T,
	router *command.Router,
	userID int64,
	commandText string,
) error {
	t.Helper()
	return router.DispatchMessageContext(
		context.Background(),
		userID,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		commandText,
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 1},
		&fakeInteraction{},
	)
}

func TestP7CContextualAuthorizationCachedPreflightThenFreshAfterAdmission(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks: client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{
			BanUsers: true,
		}),
		fresh: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{
			BanUsers: true,
		}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "admincheck",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		GroupAuthorization: core.GroupAuthorizationRequirement{
			Level:  core.GroupAuthorizationAdministrator,
			Rights: core.GroupAdminRights{BanUsers: true},
		},
		Handler: func(c *core.Context) error {
			called = true
			group, ok := c.GroupExecution()
			if !ok || !group.Actor.Verified || group.Actor.Role != core.GroupActorRoleAdministrator || !group.Actor.Rights.BanUsers {
				t.Fatalf("fresh contextual principal not visible to handler: %+v", group)
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	if err := p7cDispatch(t, router, 42, "/admincheck"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("authorized handler did not execute")
	}
	if resolver.cachedCalls != 1 || resolver.freshCalls != 1 {
		t.Fatalf("resolver calls cached=%d fresh=%d, want 1/1", resolver.cachedCalls, resolver.freshCalls)
	}
	if !resolver.cachedBeforeSubmit {
		t.Fatal("cached preflight did not happen before TaskEngine submission")
	}
	if !resolver.freshAfterSubmission {
		t.Fatal("fresh revalidation did not happen after TaskEngine admission/submission")
	}
}

func TestP7CPreflightDenialPreventsTaskAdmission(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleMember, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "adminonly",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		GroupAuthorization: core.GroupAuthorizationRequirement{
			Level: core.GroupAuthorizationAdministrator,
		},
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := p7cDispatch(t, router, 42, "/adminonly")
	if !errors.Is(err, core.ErrGroupAuthorizationDenied) || !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("preflight error=%v", err)
	}
	if client.submits != 0 || resolver.freshCalls != 0 || called {
		t.Fatalf("denied preflight leaked into task execution: submits=%d fresh=%d called=%v", client.submits, resolver.freshCalls, called)
	}
}

func TestP7CFreshRevalidationRejectsRoleDowngradeAfterAdmission(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{DeleteMessages: true}),
		fresh:  p7cVerified(core.GroupActorRoleMember, core.GroupAdminRights{}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "deletecheck",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		GroupAuthorization: core.GroupAuthorizationRequirement{
			Level:  core.GroupAuthorizationAdministrator,
			Rights: core.GroupAdminRights{DeleteMessages: true},
		},
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := p7cDispatch(t, router, 42, "/deletecheck")
	if !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("fresh downgrade error=%v, want ErrGroupAuthorizationDenied", err)
	}
	if client.submits != 1 || resolver.cachedCalls != 1 || resolver.freshCalls != 1 {
		t.Fatalf("unexpected execution counts submits=%d cached=%d fresh=%d", client.submits, resolver.cachedCalls, resolver.freshCalls)
	}
	if !resolver.freshAfterSubmission {
		t.Fatal("fresh downgrade was checked before TaskEngine admission")
	}
	if called {
		t.Fatal("handler executed after fresh authorization downgrade")
	}
}

func TestP7COwnerDoesNotBypassContextualGroupRole(t *testing.T) {
	const ownerID int64 = 42
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleMember, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetOwner(ownerID, nil)
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "ownernotadmin",
		Permission: core.PermissionOwner,
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		GroupAuthorization: core.GroupAuthorizationRequirement{
			Level: core.GroupAuthorizationAdministrator,
		},
		Handler: func(*core.Context) error {
			t.Fatal("owner must not bypass Telegram group role")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	if err := p7cDispatch(t, router, ownerID, "/ownernotadmin"); !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("owner contextual authorization error=%v", err)
	}
	if client.submits != 0 {
		t.Fatalf("owner bypass admitted task count=%d", client.submits)
	}
}

func TestP7CContextualAuthorizationRequiresTaskEngine(t *testing.T) {
	resolver := &p7cRoleResolver{
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetGroupRoleResolver(resolver)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "needsengine",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		GroupAuthorization: core.GroupAuthorizationRequirement{
			Level: core.GroupAuthorizationAdministrator,
		},
		Handler: func(*core.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := p7cDispatch(t, router, 42, "/needsengine")
	if !errors.Is(err, command.ErrTasksNotConfigured) {
		t.Fatalf("missing TaskEngine error=%v", err)
	}
	if resolver.cachedCalls != 0 || resolver.freshCalls != 0 {
		t.Fatalf("missing TaskEngine should fail before role RPC: cached=%d fresh=%d", resolver.cachedCalls, resolver.freshCalls)
	}
}

func TestP7CDoesNotOpenAssistantGroupMutationTransport(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks: client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{
			BanUsers: true,
		}),
		fresh: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{
			BanUsers: true,
		}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "banfenced",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		GroupAuthorization: core.GroupAuthorizationRequirement{
			Level:  core.GroupAuthorizationAdministrator,
			Rights: core.GroupAdminRights{BanUsers: true},
		},
		Handler: func(c *core.Context) error {
			return c.Ban(&tg.InputPeerUser{UserID: 9, AccessHash: 11}, 0)
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := p7cDispatch(t, router, 42, "/banfenced")
	if err == nil || !strings.Contains(err.Error(), "assistant group mutation transport is not configured") {
		t.Fatalf("P7-G mutation fence unexpectedly opened: %v", err)
	}
	if resolver.cachedCalls != 1 || resolver.freshCalls != 1 || client.submits != 1 {
		t.Fatalf("authorization path did not complete before mutation fence: cached=%d fresh=%d submits=%d", resolver.cachedCalls, resolver.freshCalls, client.submits)
	}
}

func TestP7CCommandsWithoutContextualMetadataStayLazy(t *testing.T) {
	resolver := &p7cRoleResolver{
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	router := command.NewRouter(zap.NewNop())
	router.SetGroupRoleResolver(resolver)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "plainread",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	if err := p7cDispatch(t, router, 42, "/plainread"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("plain command did not execute")
	}
	if resolver.cachedCalls != 0 || resolver.freshCalls != 0 {
		t.Fatalf("plain command unexpectedly resolved group role: cached=%d fresh=%d", resolver.cachedCalls, resolver.freshCalls)
	}
}
