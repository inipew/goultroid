package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

func TestCommandRouter_AssistantGroupContextPreservesSupergroupAndTopic(t *testing.T) {
	const ownerID int64 = 42
	r := command.NewRouter(zap.NewNop())
	r.SetOwner(ownerID, nil)

	var got core.GroupExecutionContext
	var groupOK bool
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "groupinfo",
		Permission: core.PermissionOwner,
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Handler: func(c *core.Context) error {
			got, groupOK = c.GroupExecution()
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	peer := &tg.InputPeerChannel{ChannelID: 99, AccessHash: 123}
	err := r.DispatchMessageContext(
		context.Background(),
		ownerID,
		peer,
		"/groupinfo",
		command.MessageContext{
			Chat:      core.Chat{ID: 99, Type: "supergroup", Title: "Test Group", AccessHash: 123},
			MessageID: 10,
			TopicID:   7,
		},
		&fakeInteraction{},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !groupOK {
		t.Fatal("expected canonical group execution context")
	}
	if got.Kind != core.ChatKindSupergroup || got.ChatID != 99 || got.TopicID != 7 {
		t.Fatalf("unexpected group context: %+v", got)
	}
	if got.Actor.UserID != ownerID || !got.Actor.IsOwner || !got.Actor.IsSudo {
		t.Fatalf("unexpected actor context: %+v", got.Actor)
	}
	if got.Actor.Role != core.GroupActorRoleUnknown || got.Actor.Verified {
		t.Fatalf("P7-A must not manufacture role authority: %+v", got.Actor)
	}
}

func TestCommandRouter_AssistantGroupOnlyRejectsBroadcastChannel(t *testing.T) {
	called := false
	r := command.NewRouter(zap.NewNop())
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "manager",
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Permission: core.PermissionEveryone,
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := r.DispatchMessageContext(
		context.Background(),
		7,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/manager",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "channel"}, MessageID: 1},
		&fakeInteraction{},
	)
	if !errors.Is(err, core.ErrGroupOnly) {
		t.Fatalf("expected ErrGroupOnly for broadcast channel, got %v", err)
	}
	if called {
		t.Fatal("broadcast channel reached group-only handler")
	}
}

func TestCommandRouter_AssistantPrivateOnlyDoesNotLeakIntoGroup(t *testing.T) {
	called := false
	r := command.NewRouter(zap.NewNop())
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:        "privatecmd",
		Surfaces:    execution.SurfaceAssistant,
		PrivateOnly: true,
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := r.DispatchMessageContext(
		context.Background(),
		7,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/privatecmd",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 1},
		&fakeInteraction{},
	)
	if !errors.Is(err, core.ErrPrivateOnly) {
		t.Fatalf("expected ErrPrivateOnly in group, got %v", err)
	}
	if called {
		t.Fatal("private-only handler leaked into group plane")
	}
}

func TestCommandRouter_AssistantGroupMutationFailsClosedBeforeP7G(t *testing.T) {
	const ownerID int64 = 42
	r := command.NewRouter(zap.NewNop())
	r.SetOwner(ownerID, nil)
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "banlike",
		Surfaces:   execution.SurfaceAssistant,
		Permission: core.PermissionOwner,
		GroupOnly:  true,
		Handler: func(c *core.Context) error {
			return c.Ban(&tg.InputPeerUser{UserID: 9, AccessHash: 11}, 0)
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := r.DispatchMessageContext(
		context.Background(),
		ownerID,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/banlike",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 1},
		&fakeInteraction{},
	)
	if !errors.Is(err, command.ErrGroupMutationUnavailable) {
		t.Fatalf("expected fail-closed Assistant mutation fence, got %v", err)
	}
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("expected mutation fence to classify as unavailable, got %v", err)
	}
}

type commandGroupRoleResolverStub struct {
	calls int
}

func (s *commandGroupRoleResolverStub) ResolveGroupRole(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	s.calls++
	return core.GroupRoleSnapshot{
		Principal: core.GroupActorPrincipal{
			UserID:   request.UserID,
			Role:     core.GroupActorRoleAdministrator,
			Rights:   core.GroupAdminRights{DeleteMessages: true},
			Verified: true,
		},
	}, nil
}

func (s *commandGroupRoleResolverStub) ResolveGroupRoleFresh(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return s.ResolveGroupRole(context.Background(), request)
}

func TestCommandRouter_GroupRoleResolverIsAvailableLazilyToCanonicalHandler(t *testing.T) {
	roleResolver := &commandGroupRoleResolverStub{}
	r := command.NewRouter(zap.NewNop())
	r.SetGroupRoleResolver(roleResolver)

	var resolved core.GroupRoleSnapshot
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "roleprobe",
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Permission: core.PermissionEveryone,
		Handler: func(c *core.Context) error {
			var err error
			resolved, err = c.ResolveGroupActor(false)
			return err
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := r.DispatchMessageContext(
		context.Background(),
		7,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/roleprobe",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 1},
		&fakeInteraction{},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if roleResolver.calls != 1 ||
		resolved.Principal.Role != core.GroupActorRoleAdministrator ||
		!resolved.Principal.Rights.DeleteMessages {
		t.Fatalf("resolver calls=%d snapshot=%+v", roleResolver.calls, resolved)
	}
}

func TestCommandRouter_P7JCarriesMediaAndSelfContextToHandler(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	var got *core.Context
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "contextprobe",
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Permission: core.PermissionEveryone,
		Handler: func(c *core.Context) error {
			got = c
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := r.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 99, AccessHash: 123},
		"/contextprobe @helper",
		command.MessageContext{
			Chat:          core.Chat{ID: 99, Type: "supergroup", AccessHash: 123},
			MessageID:     70,
			TopicID:       7,
			Media:         &core.MediaInfo{Type: "webpage", WebURL: "https://example.com"},
			GroupedID:     44,
			Self:          core.User{ID: 999, Username: "helper", IsBot: true},
			MentionedSelf: true,
			ReplyPeer:     core.PeerRef{Kind: core.PeerKindChannel, ID: 99, AccessHash: 123},
		},
		&fakeInteraction{},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got == nil || got.Message == nil {
		t.Fatal("canonical handler did not receive context")
	}
	if got.Message.Media == nil || got.Message.Media.Type != "webpage" || got.Message.GroupedID != 44 {
		t.Fatalf("media/grouped context=%+v grouped=%d", got.Message.Media, got.Message.GroupedID)
	}
	if !got.MentionedSelf() || got.Self == nil || got.Self.ID != 999 || got.Self.Username != "helper" {
		t.Fatalf("self context mentioned=%v self=%+v", got.MentionedSelf(), got.Self)
	}
	if got.Message.ReplyPeer.ID != 99 {
		t.Fatalf("reply peer=%+v", got.Message.ReplyPeer)
	}
}
