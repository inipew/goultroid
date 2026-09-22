package command_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/plugins/info"
	"go.uber.org/zap"
)

type p7eRoleResolver struct {
	tasks       *p7cTaskClient
	cached      core.GroupActorPrincipal
	fresh       core.GroupActorPrincipal
	cachedErr   error
	freshErr    error
	cachedCalls int
	freshCalls  int
}

func (r *p7eRoleResolver) ResolveGroupRole(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.cachedCalls++
	if r.cachedErr != nil {
		return core.GroupRoleSnapshot{}, r.cachedErr
	}
	principal := r.cached
	principal.UserID = request.UserID
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

func (r *p7eRoleResolver) ResolveGroupRoleFresh(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.freshCalls++
	if r.freshErr != nil {
		return core.GroupRoleSnapshot{}, r.freshErr
	}
	principal := r.fresh
	principal.UserID = request.UserID
	return core.GroupRoleSnapshot{Principal: principal}, nil
}

func p7eChatInfoCommand(t *testing.T) core.Command {
	t.Helper()
	for _, candidate := range info.New().Commands() {
		if candidate.Name == "chatinfo" {
			return candidate
		}
	}
	t.Fatal("chatinfo command not found")
	return core.Command{}
}

func p7eRouter(
	t *testing.T,
	taskClient *p7cTaskClient,
	roleResolver core.GroupRoleResolver,
	query command.GroupQueryReader,
) *command.Router {
	t.Helper()
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(taskClient)
	router.SetGroupRoleResolver(roleResolver)
	router.SetGroupQueryReader(query)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(p7eChatInfoCommand(t)); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)
	return router
}

func TestP7EStaleAdminRoleGetsSafeDeniedFeedbackAfterAdmission(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7cRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		fresh:  p7cVerified(core.GroupActorRoleMember, core.GroupAdminRights{}),
	}
	query := &managerReadQueryStub{}
	router := p7eRouter(t, client, resolver, query)
	fake := &fakeInteraction{}

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 21},
		fake,
	)
	if !errors.Is(err, core.ErrGroupAuthorizationDenied) {
		t.Fatalf("stale-role error=%v, want ErrGroupAuthorizationDenied", err)
	}
	if client.submits != 1 || resolver.cachedCalls != 1 || resolver.freshCalls != 1 {
		t.Fatalf("stale-role path submits=%d cached=%d fresh=%d, want 1/1/1",
			client.submits, resolver.cachedCalls, resolver.freshCalls)
	}
	if query.calls != 0 {
		t.Fatalf("stale-role denial reached read query %d times", query.calls)
	}
	if !strings.Contains(fake.lastSentText, "not authorized") {
		t.Fatalf("stale-role feedback=%q, want safe authorization denial", fake.lastSentText)
	}
	if !core.IsUserSafeText(fake.lastSentText) {
		t.Fatalf("stale-role feedback is not user-safe: %q", fake.lastSentText)
	}
}

func TestP7EPreflightVerificationFailureIsUnavailableNotPermissionDenied(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7eRoleResolver{
		tasks:     client,
		cachedErr: fmt.Errorf("%w: participant verification token=secret", core.ErrUnavailable),
	}
	query := &managerReadQueryStub{}
	router := p7eRouter(t, client, resolver, query)
	fake := &fakeInteraction{}

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 22},
		fake,
	)
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("preflight verification error=%v, want ErrUnavailable", err)
	}
	if client.submits != 0 || resolver.cachedCalls != 1 || resolver.freshCalls != 0 || query.calls != 0 {
		t.Fatalf("preflight verification leaked work submits=%d cached=%d fresh=%d query=%d",
			client.submits, resolver.cachedCalls, resolver.freshCalls, query.calls)
	}
	if !strings.Contains(fake.lastSentText, "temporarily unavailable") {
		t.Fatalf("verification feedback=%q, want temporary-unavailable UX", fake.lastSentText)
	}
	if strings.Contains(fake.lastSentText, "not authorized") {
		t.Fatalf("verification failure was misreported as permission denial: %q", fake.lastSentText)
	}
	for _, leaked := range []string{"participant verification", "token", "secret"} {
		if strings.Contains(strings.ToLower(fake.lastSentText), strings.ToLower(leaked)) {
			t.Fatalf("verification feedback leaked internal detail %q: %q", leaked, fake.lastSentText)
		}
	}
}

func TestP7EFreshVerificationFailureAfterAdmissionIsUnavailable(t *testing.T) {
	client := &p7cTaskClient{}
	resolver := &p7eRoleResolver{
		tasks:  client,
		cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
		freshErr: fmt.Errorf(
			"%w: channels.getParticipant access_hash=secret",
			core.ErrUnavailable,
		),
	}
	query := &managerReadQueryStub{}
	router := p7eRouter(t, client, resolver, query)
	fake := &fakeInteraction{}

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
		"/chatinfo",
		command.MessageContext{Chat: core.Chat{ID: 55, Type: "supergroup"}, MessageID: 23},
		fake,
	)
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("fresh verification error=%v, want ErrUnavailable", err)
	}
	if client.submits != 1 || resolver.cachedCalls != 1 || resolver.freshCalls != 1 || query.calls != 0 {
		t.Fatalf("fresh verification path submits=%d cached=%d fresh=%d query=%d",
			client.submits, resolver.cachedCalls, resolver.freshCalls, query.calls)
	}
	if !strings.Contains(fake.lastSentText, "temporarily unavailable") {
		t.Fatalf("fresh verification feedback=%q, want temporary-unavailable UX", fake.lastSentText)
	}
	if strings.Contains(fake.lastSentText, "not authorized") {
		t.Fatalf("fresh verification failure was misreported as permission denial: %q", fake.lastSentText)
	}
	if !core.IsUserSafeText(fake.lastSentText) {
		t.Fatalf("fresh verification feedback is not user-safe: %q", fake.lastSentText)
	}
}

func TestP7EUnsupportedManagerChatsReturnSafeGroupOnlyFeedback(t *testing.T) {
	tests := []struct {
		name string
		peer tg.InputPeerClass
		chat core.Chat
	}{
		{
			name: "private",
			peer: &tg.InputPeerUser{UserID: 42, AccessHash: 8},
			chat: core.Chat{ID: 42, Type: "private"},
		},
		{
			name: "broadcast channel",
			peer: &tg.InputPeerChannel{ChannelID: 55, AccessHash: 8},
			chat: core.Chat{ID: 55, Type: "channel"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &p7cTaskClient{}
			resolver := &p7eRoleResolver{
				tasks:  client,
				cached: p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
				fresh:  p7cVerified(core.GroupActorRoleAdministrator, core.GroupAdminRights{}),
			}
			query := &managerReadQueryStub{}
			router := p7eRouter(t, client, resolver, query)
			fake := &fakeInteraction{}

			err := router.DispatchMessageContext(
				context.Background(),
				42,
				tc.peer,
				"/chatinfo",
				command.MessageContext{Chat: tc.chat, MessageID: 24},
				fake,
			)
			if !errors.Is(err, core.ErrGroupOnly) {
				t.Fatalf("unsupported chat error=%v, want ErrGroupOnly", err)
			}
			if client.submits != 0 || resolver.cachedCalls != 0 || resolver.freshCalls != 0 || query.calls != 0 {
				t.Fatalf("unsupported chat leaked work submits=%d cached=%d fresh=%d query=%d",
					client.submits, resolver.cachedCalls, resolver.freshCalls, query.calls)
			}
			if !strings.Contains(fake.lastSentText, "only be used in groups") {
				t.Fatalf("unsupported chat feedback=%q, want group-only UX", fake.lastSentText)
			}
			if !core.IsUserSafeText(fake.lastSentText) {
				t.Fatalf("unsupported chat feedback is not user-safe: %q", fake.lastSentText)
			}
		})
	}
}
