package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/plugins/admin"
	"go.uber.org/zap"
)

type p7gMutationExecutorStub struct {
	tasks            *p7cTaskClient
	calls            int
	calledAfterSubmit bool
	meta             command.GroupMutationContext
	request          command.GroupMutationRequest
	err              error
}

func (m *p7gMutationExecutorStub) Execute(
	_ context.Context,
	meta command.GroupMutationContext,
	request command.GroupMutationRequest,
) (command.GroupMutationResult, error) {
	m.calls++
	m.calledAfterSubmit = m.tasks != nil && m.tasks.submits > 0
	m.meta = meta
	m.request = request
	return command.GroupMutationResult{}, m.err
}

func p7gAdminCommand(t *testing.T, name string) core.Command {
	t.Helper()
	for _, candidate := range admin.New().Commands() {
		if candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("admin command %q not found", name)
	return core.Command{}
}

func TestP7GRealBanCommandUsesContextualAdminAndSharedTaskEngine(t *testing.T) {
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
	mutation := &p7gMutationExecutorStub{tasks: client}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupMutationExecutor(mutation)
	router.SetPeerResolver(&core.MockPeerResolver{
		UserID:   5555,
		UserPeer: &tg.InputPeerUser{UserID: 5555, AccessHash: 12345},
	})

	ban := p7gAdminCommand(t, "ban")
	if ban.Permission != core.PermissionSudo {
		t.Fatalf("Userbot ban permission=%s, want Sudo", ban.Permission)
	}
	if got := ban.EffectivePermission(core.ExecutionAssistant); got != core.PermissionEveryone {
		t.Fatalf("Assistant ban permission=%s, want Everyone", got)
	}
	if !ban.GroupAuthorization.Rights.BanUsers {
		t.Fatalf("Assistant ban lacks contextual BanUsers requirement: %+v", ban.GroupAuthorization)
	}

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(ban); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := router.DispatchMessageContext(
		context.Background(),
		42, // deliberately not configured as Owner/Sudo
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/ban 5555",
		command.MessageContext{
			Chat:      core.Chat{ID: 55, Type: "supergroup"},
			MessageID: 100,
		},
		fake,
	)
	if err != nil {
		t.Fatalf("real /ban dispatch: %v", err)
	}
	if client.submits != 1 {
		t.Fatalf("TaskEngine submissions=%d, want 1", client.submits)
	}
	if resolver.cachedCalls != 1 || resolver.freshCalls != 1 {
		t.Fatalf("P7-C role calls cached=%d fresh=%d, want 1/1",
			resolver.cachedCalls, resolver.freshCalls)
	}
	if mutation.calls != 1 || !mutation.calledAfterSubmit {
		t.Fatalf("mutation calls=%d after-submit=%v", mutation.calls, mutation.calledAfterSubmit)
	}
	if mutation.meta.ActorID != 42 || mutation.meta.ChatID != 55 ||
		mutation.meta.Kind != core.ChatKindSupergroup {
		t.Fatalf("mutation context=%+v", mutation.meta)
	}
	if mutation.request.Action != core.GroupMutationBan {
		t.Fatalf("mutation action=%s, want ban", mutation.request.Action)
	}
	target, ok := mutation.request.Target.(*tg.InputPeerUser)
	if !ok || target.UserID != 5555 || target.AccessHash == 0 {
		t.Fatalf("mutation target=%#v", mutation.request.Target)
	}
	if fake.lastSentText == "" {
		t.Fatal("real /ban produced no success response")
	}
}

func TestP7GRealBanCommandStillFailsPreflightWithoutTelegramRight(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{BanUsers: true}),
	}
	mutation := &p7gMutationExecutorStub{tasks: client}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupMutationExecutor(mutation)
	router.SetPeerResolver(&core.MockPeerResolver{
		UserID:   5555,
		UserPeer: &tg.InputPeerUser{UserID: 5555, AccessHash: 12345},
	})

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(p7gAdminCommand(t, "ban")); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/ban 5555",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 101},
		&fakeInteraction{},
	)
	if err == nil {
		t.Fatal("missing BanUsers right unexpectedly admitted /ban")
	}
	if client.submits != 0 || mutation.calls != 0 {
		t.Fatalf("denied /ban leaked work submits=%d mutations=%d", client.submits, mutation.calls)
	}
}

func TestP7GMutationPortRejectsDirectExecutionWithoutTaskAdmission(t *testing.T) {
	mutation := &p7gMutationExecutorStub{}
	router := command.NewRouter(zap.NewNop())
	router.SetGroupMutationExecutor(mutation)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "directbanprobe",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Handler: func(c *core.Context) error {
			return c.Ban(&tg.InputPeerUser{UserID: 99, AccessHash: 999}, 0)
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/directbanprobe",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 102},
		&fakeInteraction{},
	)
	if !errors.Is(err, command.ErrGroupMutationNotAdmitted) {
		t.Fatalf("direct mutation error=%v, want ErrGroupMutationNotAdmitted", err)
	}
	if mutation.calls != 0 {
		t.Fatalf("direct execution reached mutation executor %d times", mutation.calls)
	}
}

func TestP7GMutationPortActivatesInsideSharedTaskEngine(t *testing.T) {
	client := &p7cTaskClient{}
	mutation := &p7gMutationExecutorStub{tasks: client}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupMutationExecutor(mutation)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "taskbanprobe",
		Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Handler: func(c *core.Context) error {
			return c.Ban(&tg.InputPeerUser{UserID: 99, AccessHash: 999}, 0)
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/taskbanprobe",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 103},
		&fakeInteraction{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.submits != 1 {
		t.Fatalf("TaskEngine submissions=%d, want 1", client.submits)
	}
	if mutation.calls != 1 || !mutation.calledAfterSubmit {
		t.Fatalf("mutation calls=%d after-submit=%v, want 1/true",
			mutation.calls, mutation.calledAfterSubmit)
	}
}
