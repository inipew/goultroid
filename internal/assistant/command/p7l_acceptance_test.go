package command_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type p7lRejectTaskClient struct {
	submits int
	err     error
}

func (c *p7lRejectTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	c.submits++
	return nil, c.err
}
func (*p7lRejectTaskClient) Cancel(id tasks.TaskID, cause tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Accepted: false, Reason: cause}, nil
}
func (*p7lRejectTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*p7lRejectTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestP7LAssistantGroupSurfaceMatrix(t *testing.T) {
	tests := []struct {
		name      string
		peer      tg.InputPeerClass
		chat      core.Chat
		wantError error
	}{
		{
			name: "private rejected",
			peer: &tg.InputPeerUser{UserID: 7, AccessHash: 70},
			chat: core.Chat{ID: 7, Type: string(core.ChatKindPrivate)},
			wantError: core.ErrGroupOnly,
		},
		{
			name: "basic group accepted",
			peer: &tg.InputPeerChat{ChatID: 55},
			chat: core.Chat{ID: 55, Type: string(core.ChatKindGroup)},
		},
		{
			name: "supergroup accepted",
			peer: &tg.InputPeerChannel{ChannelID: 66, AccessHash: 660},
			chat: core.Chat{ID: 66, Type: string(core.ChatKindSupergroup), AccessHash: 660},
		},
		{
			name: "broadcast channel rejected",
			peer: &tg.InputPeerChannel{ChannelID: 77, AccessHash: 770},
			chat: core.Chat{ID: 77, Type: string(core.ChatKindChannel), AccessHash: 770},
			wantError: core.ErrGroupOnly,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := command.NewRouter(zap.NewNop())
			called := false
			coreRouter := core.NewRouter(".")
			if err := coreRouter.Register(core.Command{
				Name:       "surfaceprobe",
				Surfaces:   execution.SurfaceAssistant,
				Permission: core.PermissionEveryone,
				GroupOnly:  true,
				Handler: func(*core.Context) error {
					called = true
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			router.SetCoreRouter(coreRouter)

			err := router.DispatchMessageContext(
				context.Background(),
				42,
				tc.peer,
				"/surfaceprobe",
				command.MessageContext{Chat: tc.chat, MessageID: 1},
				&fakeInteraction{},
			)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("dispatch error=%v want %v", err, tc.wantError)
				}
				if called {
					t.Fatal("rejected surface reached handler")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("accepted group surface did not reach handler")
			}
		})
	}
}

func TestP7LGlobalPrivilegeDoesNotReplaceTelegramGroupRole(t *testing.T) {
	const (
		ownerID  int64 = 1
		sudoID   int64 = 2
		adminID  int64 = 3
		memberID int64 = 4
	)
	tests := []struct {
		name    string
		userID  int64
		role    core.GroupActorRole
		rights  core.GroupAdminRights
		allowed bool
	}{
		{name: "owner but member denied", userID: ownerID, role: core.GroupActorRoleMember},
		{name: "sudo but member denied", userID: sudoID, role: core.GroupActorRoleMember},
		{
			name: "telegram admin allowed", userID: adminID,
			role: core.GroupActorRoleAdministrator,
			rights: core.GroupAdminRights{DeleteMessages: true},
			allowed: true,
		},
		{name: "ordinary member denied", userID: memberID, role: core.GroupActorRoleMember},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &p7cTaskClient{}
			principal := p7cVerified(tc.role, tc.rights)
			resolver := &p7cRoleResolver{
				tasks: client,
				cached: principal,
				fresh: principal,
			}
			router := command.NewRouter(zap.NewNop())
			router.SetOwner(ownerID, func() []int64 { return []int64{sudoID} })
			router.SetTasks(client)
			router.SetGroupRoleResolver(resolver)

			called := false
			coreRouter := core.NewRouter(".")
			if err := coreRouter.Register(core.Command{
				Name:       "roleaccept",
				Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
				Surfaces:   execution.SurfaceAssistant,
				Permission: core.PermissionEveryone,
				GroupOnly:  true,
				GroupAuthorization: core.GroupAuthorizationRequirement{
					Level: core.GroupAuthorizationAdministrator,
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

			err := p7cDispatch(t, router, tc.userID, "/roleaccept")
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				if !called || client.submits != 1 || resolver.cachedCalls != 1 || resolver.freshCalls != 1 {
					t.Fatalf("allowed path called=%v submits=%d cached=%d fresh=%d",
						called, client.submits, resolver.cachedCalls, resolver.freshCalls)
				}
				return
			}
			if !errors.Is(err, core.ErrGroupAuthorizationDenied) {
				t.Fatalf("denied path error=%v", err)
			}
			if called || client.submits != 0 || resolver.cachedCalls != 1 || resolver.freshCalls != 0 {
				t.Fatalf("denied path called=%v submits=%d cached=%d fresh=%d",
					called, client.submits, resolver.cachedCalls, resolver.freshCalls)
			}
		})
	}
}

func TestP7LTaskAdmissionRejectionNeverExecutesGroupHandler(t *testing.T) {
	rejection := tasks.NewAdmissionError(tasks.ReasonOwnerQueueFull, tasks.ErrOwnerQueueFull)
	taskClient := &p7lRejectTaskClient{err: rejection}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(taskClient)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "rejectprobe",
		Surfaces:   execution.SurfaceAssistant,
		Permission: core.PermissionEveryone,
		GroupOnly:  true,
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	err := router.DispatchMessageContext(
		context.Background(),
		42,
		&tg.InputPeerChannel{ChannelID: 99, AccessHash: 199},
		"/rejectprobe",
		command.MessageContext{
			Chat: core.Chat{ID: 99, Type: string(core.ChatKindSupergroup), AccessHash: 199},
			MessageID: 7,
		},
		&fakeInteraction{},
	)
	if !errors.Is(err, tasks.ErrOwnerQueueFull) {
		t.Fatalf("admission error=%v want owner queue full", err)
	}
	if taskClient.submits != 1 || called {
		t.Fatalf("rejected work submits=%d called=%v", taskClient.submits, called)
	}
}

func TestP7LHighCardinalityTopicsShareChatQuotaButKeepOrderingIdentity(t *testing.T) {
	taskClient := &p7cTaskClient{}
	router := command.NewRouter(zap.NewNop())
	router.SetTasks(taskClient)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "topicaccept",
		Surfaces:   execution.SurfaceAssistant,
		Permission: core.PermissionEveryone,
		GroupOnly:  true,
		Handler:    func(*core.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	const chatID int64 = 9001
	peer := &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 19001}
	for topicID := 1; topicID <= 512; topicID++ {
		senderID := int64(100000 + topicID)
		err := router.DispatchMessageContext(
			context.Background(),
			senderID,
			peer,
			"/topicaccept",
			command.MessageContext{
				Chat: core.Chat{
					ID: chatID, Type: string(core.ChatKindSupergroup), AccessHash: peer.AccessHash,
				},
				MessageID: topicID,
				TopicID: topicID,
			},
			&fakeInteraction{},
		)
		if err != nil {
			t.Fatalf("topic %d: %v", topicID, err)
		}
		if taskClient.last.QuotaOwner != tasks.OwnerID("telegram:chat:9001") {
			t.Fatalf("topic %d quota=%q", topicID, taskClient.last.QuotaOwner)
		}
		wantOrdering := fmt.Sprintf("chat:%d:topic:%d", chatID, topicID)
		if taskClient.last.OrderingKey != wantOrdering {
			t.Fatalf("topic %d ordering=%q want %q", topicID, taskClient.last.OrderingKey, wantOrdering)
		}
	}
}
