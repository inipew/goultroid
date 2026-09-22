package command_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/plugins/info"
	"go.uber.org/zap"
)

type managerReadQueryStub struct {
	calls int
	err   error
}

func (q *managerReadQueryStub) GetFullChat(context.Context, tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	q.calls++
	if q.err != nil {
		return nil, q.err
	}
	return &tg.MessagesChatFull{
		FullChat: &tg.ChannelFull{
			ID:                55,
			ParticipantsCount: 321,
			AdminsCount:       7,
			SlowmodeSeconds:   15,
			About:             "P7-D manager canary",
		},
	}, nil
}

func TestP7DGroupInfoCanaryUsesContextualAuthTaskEngineAndReadQuery(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks: client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{
			DeleteMessages: true,
		}),
		fresh: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{
			DeleteMessages: true,
		}),
	}
	query := &managerReadQueryStub{}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupQueryReader(query)

	var chatInfo core.Command
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			chatInfo = candidate
			break
		}
	}
	if chatInfo.Name == "" {
		t.Fatal("chatinfo command not found")
	}
	if chatInfo.Permission != core.PermissionEveryone ||
		chatInfo.GroupAuthorization.Level != core.GroupAuthorizationAdministrator {
		t.Fatalf("unexpected manager metadata: permission=%v auth=%+v", chatInfo.Permission, chatInfo.GroupAuthorization)
	}

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(chatInfo); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/groupinfo",
		command.MessageContext{
			Chat:      core.Chat{ID: 55, Type: "supergroup", Title: "Manager Group", AccessHash: 8},
			MessageID: 10,
			TopicID:   4,
		},
		fake,
	)
	if err != nil {
		t.Fatalf("groupinfo dispatch: %v", err)
	}

	if client.submits != 1 || resolver.cachedCalls != 1 || resolver.freshCalls != 1 {
		t.Fatalf("execution path submits=%d cached=%d fresh=%d", client.submits, resolver.cachedCalls, resolver.freshCalls)
	}
	if !resolver.cachedBeforeSubmit || !resolver.freshAfterSubmission {
		t.Fatalf("authorization ordering preflight_before_submit=%v fresh_after_submit=%v",
			resolver.cachedBeforeSubmit, resolver.freshAfterSubmission)
	}
	if query.calls != 1 {
		t.Fatalf("group query calls=%d, want 1", query.calls)
	}

	for _, want := range []string{
		"Manager Group",
		"321",
		"7",
		"15s",
		"administrator",
		"Topic ID",
	} {
		if !strings.Contains(fake.lastSentText, want) {
			t.Fatalf("groupinfo response missing %q: %s", want, fake.lastSentText)
		}
	}
}

func TestP7DGroupInfoMemberIsDeniedBeforeTaskAndQuery(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleMember, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	query := &managerReadQueryStub{}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupQueryReader(query)

	var chatInfo core.Command
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			chatInfo = candidate
			break
		}
	}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(chatInfo); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 10},
		fake,
	)
	if err == nil || !strings.Contains(err.Error(), "contextual group authorization denied") {
		t.Fatalf("member denial error=%v", err)
	}
	if client.submits != 0 || resolver.freshCalls != 0 || query.calls != 0 {
		t.Fatalf("member denial leaked past preflight submits=%d fresh=%d query=%d",
			client.submits, resolver.freshCalls, query.calls)
	}
	if !strings.Contains(fake.lastSentText, "not authorized") {
		t.Fatalf("member denial did not receive safe feedback: %q", fake.lastSentText)
	}
}

func TestP7DGroupInfoCreatorIsAccepted(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleCreator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleCreator, core.GroupAdminRights{}),
	}
	query := &managerReadQueryStub{}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupQueryReader(query)

	var chatInfo core.Command
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			chatInfo = candidate
			break
		}
	}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(chatInfo); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup", Title: "Creator Group"}, MessageID: 11},
		fake,
	)
	if err != nil {
		t.Fatalf("creator chatinfo dispatch: %v", err)
	}
	if client.submits != 1 || resolver.cachedCalls != 1 || resolver.freshCalls != 1 || query.calls != 1 {
		t.Fatalf("creator execution path submits=%d cached=%d fresh=%d query=%d",
			client.submits, resolver.cachedCalls, resolver.freshCalls, query.calls)
	}
	if !strings.Contains(fake.lastSentText, "creator") {
		t.Fatalf("creator role not rendered: %q", fake.lastSentText)
	}
}

func TestP7DGroupInfoWrongBotTargetIsIgnoredBeforeAuthorization(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	query := &managerReadQueryStub{}

	router := command.NewRouter(zap.NewNop())
	router.SetBotUsername("Assistant")
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupQueryReader(query)

	var chatInfo core.Command
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			chatInfo = candidate
			break
		}
	}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(chatInfo); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo@OtherBot",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 12},
		fake,
	)
	if err != nil {
		t.Fatalf("other-bot command should be ignored: %v", err)
	}
	if client.submits != 0 || resolver.cachedCalls != 0 || resolver.freshCalls != 0 || query.calls != 0 {
		t.Fatalf("other-bot command leaked into manager plane submits=%d cached=%d fresh=%d query=%d",
			client.submits, resolver.cachedCalls, resolver.freshCalls, query.calls)
	}
	if fake.lastSentText != "" {
		t.Fatalf("other-bot command produced response: %q", fake.lastSentText)
	}
}

func TestP7DGroupInfoBroadcastChannelFailsClosedBeforeRoleRPC(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	query := &managerReadQueryStub{}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupQueryReader(query)

	var chatInfo core.Command
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			chatInfo = candidate
			break
		}
	}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(chatInfo); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "channel"}, MessageID: 13},
		&fakeInteraction{},
	)
	if err == nil || !strings.Contains(err.Error(), "can only be used in groups") {
		t.Fatalf("broadcast channel error=%v", err)
	}
	if client.submits != 0 || resolver.cachedCalls != 0 || resolver.freshCalls != 0 || query.calls != 0 {
		t.Fatalf("broadcast channel leaked into manager execution submits=%d cached=%d fresh=%d query=%d",
			client.submits, resolver.cachedCalls, resolver.freshCalls, query.calls)
	}
}

func TestP7DGroupInfoReadQueryFailureIsUserSafe(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
	}
	query := &managerReadQueryStub{
		err: fmt.Errorf("%w: CHAT_ADMIN_REQUIRED access_hash=secret", core.ErrUnavailable),
	}

	router := command.NewRouter(zap.NewNop())
	router.SetTasks(client)
	router.SetGroupRoleResolver(resolver)
	router.SetGroupQueryReader(query)

	var chatInfo core.Command
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			chatInfo = candidate
			break
		}
	}
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(chatInfo); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 14},
		fake,
	)
	if err != nil {
		t.Fatalf("query failure should be rendered safely by chatinfo: %v", err)
	}
	if query.calls != 1 || client.submits != 1 || resolver.freshCalls != 1 {
		t.Fatalf("unexpected query failure path query=%d submits=%d fresh=%d",
			query.calls, client.submits, resolver.freshCalls)
	}
	if !strings.Contains(fake.lastSentText, "temporarily unavailable") {
		t.Fatalf("safe availability message missing: %q", fake.lastSentText)
	}
	for _, leaked := range []string{"CHAT_ADMIN_REQUIRED", "access_hash", "secret"} {
		if strings.Contains(fake.lastSentText, leaked) {
			t.Fatalf("query failure leaked internal detail %q: %q", leaked, fake.lastSentText)
		}
	}
}
