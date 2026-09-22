package client

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type p7iRuleIngressStub struct {
	interested      bool
	interestedCalls int
	handleCalls     int
	last            *core.MessageEnvelope
}

func (s *p7iRuleIngressStub) Interested(int64) bool {
	s.interestedCalls++
	return s.interested
}

func (s *p7iRuleIngressStub) Handle(_ context.Context, message *core.MessageEnvelope) error {
	s.handleCalls++
	s.last = message
	return nil
}

type p7iTaskClient struct {
	calls int
	run   bool
	spec  tasks.WorkSpec
}

func (c *p7iTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.calls++
	c.spec = spec
	if c.run && spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (*p7iTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}

func (*p7iTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }

func (*p7iTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func p7iDispatchMessage(
	t *testing.T,
	deps UpdateHandlerDeps,
	message *tg.Message,
	chats []tg.ChatClass,
	users []tg.UserClass,
) {
	t.Helper()
	dispatcher := tg.NewUpdateDispatcher()
	RegisterUpdateHandlers(&dispatcher, deps)
	err := dispatcher.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: message}},
		Chats:   chats,
		Users:   users,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP7IInactiveGroupStopsBeforeCacheResolverAndTask(t *testing.T) {
	rules := &p7iRuleIngressStub{}
	resolver := &groupServiceResolverStub{resolved: &tg.InputPeerChat{ChatID: 77}}
	tasksClient := &p7iTaskClient{run: true}
	cacheCalls := 0

	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:     zap.NewNop(),
		GroupRules: rules,
		Resolver:   resolver,
		Tasks:      tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	}, &tg.Message{
		ID:      100,
		PeerID:  &tg.PeerChat{ChatID: 77},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "ordinary group text",
	}, nil, nil)

	if rules.interestedCalls != 1 || rules.handleCalls != 0 {
		t.Fatalf("rules interested/handle=%d/%d, want 1/0",
			rules.interestedCalls, rules.handleCalls)
	}
	if cacheCalls != 0 || resolver.calls != 0 || tasksClient.calls != 0 {
		t.Fatalf("cold path cache=%d resolver=%d tasks=%d, want 0/0/0",
			cacheCalls, resolver.calls, tasksClient.calls)
	}
}

func TestP7IInterestedGroupUsesOneOrderedTaskAndResolvesInsideTask(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: true}
	resolver := &groupServiceResolverStub{resolved: &tg.InputPeerChat{ChatID: 77}}
	tasksClient := &p7iTaskClient{run: false}
	cacheCalls := 0

	deps := UpdateHandlerDeps{
		Logger:     zap.NewNop(),
		GroupRules: rules,
		Resolver:   resolver,
		Tasks:      tasksClient,
		SelfID:     func() int64 { return 999 },
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	}
	message := &tg.Message{
		ID:      101,
		Date:    1234,
		PeerID:  &tg.PeerChat{ChatID: 77},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "matched later",
	}
	p7iDispatchMessage(t, deps, message, nil, []tg.UserClass{
		&tg.User{ID: 42, FirstName: "Alice"},
	})

	if rules.interestedCalls != 1 || tasksClient.calls != 1 {
		t.Fatalf("interested/tasks=%d/%d, want 1/1", rules.interestedCalls, tasksClient.calls)
	}
	if cacheCalls != 1 {
		t.Fatalf("cache calls=%d, want 1 only after interest hit", cacheCalls)
	}
	if resolver.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("pre-task resolver=%d handle=%d, want 0/0", resolver.calls, rules.handleCalls)
	}
	if tasksClient.spec.Pool != tasks.PoolID("interactive") ||
		tasksClient.spec.Class != tasks.PriorityInteractive ||
		tasksClient.spec.OrderingKey != "chat:77" {
		t.Fatalf("task admission pool=%q class=%q ordering=%q",
			tasksClient.spec.Pool, tasksClient.spec.Class, tasksClient.spec.OrderingKey)
	}

	if err := tasksClient.spec.Handler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || rules.handleCalls != 1 {
		t.Fatalf("in-task resolver=%d handle=%d, want 1/1", resolver.calls, rules.handleCalls)
	}
	if rules.last == nil || rules.last.ChatID != 77 || rules.last.Sender.ID != 42 ||
		rules.last.Text != "matched later" || !rules.last.IsGroup() {
		t.Fatalf("rule envelope=%+v", rules.last)
	}
}

func TestP7ISlashPrivateAndBroadcastDoNotEnterRulePlane(t *testing.T) {
	tests := []struct {
		name    string
		message *tg.Message
		chats   []tg.ChatClass
	}{
		{
			name: "slash group command",
			message: &tg.Message{
				ID: 102, PeerID: &tg.PeerChat{ChatID: 77},
				FromID: &tg.PeerUser{UserID: 42}, Message: "/help",
			},
		},
		{
			name: "private text",
			message: &tg.Message{
				ID: 103, PeerID: &tg.PeerUser{UserID: 42},
				FromID: &tg.PeerUser{UserID: 42}, Message: "hello",
			},
		},
		{
			name: "broadcast text",
			message: &tg.Message{
				ID: 104, PeerID: &tg.PeerChannel{ChannelID: 88},
				FromID: &tg.PeerUser{UserID: 42}, Message: "announcement",
			},
			chats: []tg.ChatClass{
				&tg.Channel{ID: 88, AccessHash: 188, Megagroup: false},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := &p7iRuleIngressStub{interested: true}
			p7iDispatchMessage(t, UpdateHandlerDeps{
				Logger:     zap.NewNop(),
				GroupRules: rules,
			}, tc.message, tc.chats, nil)
			if rules.interestedCalls != 0 || rules.handleCalls != 0 {
				t.Fatalf("rule plane touched interested=%d handle=%d",
					rules.interestedCalls, rules.handleCalls)
			}
		})
	}
}


func TestP7IActiveUnknownSupergroupDefersClassificationUntilTask(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: true}
	resolver := &groupServiceResolverStub{
		resolved: &tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	}
	tasksClient := &p7iTaskClient{run: false}
	cacheCalls := 0

	message := &tg.Message{
		ID:      105,
		Date:    1234,
		PeerID:  &tg.PeerChannel{ChannelID: 88},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "active rule text",
	}
	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:     zap.NewNop(),
		GroupRules: rules,
		Resolver:   resolver,
		Tasks:      tasksClient,
		SelfID:     func() int64 { return 999 },
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	}, message, nil, []tg.UserClass{
		&tg.User{ID: 42, FirstName: "Alice"},
	})

	if rules.interestedCalls != 1 || tasksClient.calls != 1 {
		t.Fatalf("unknown supergroup interested/tasks=%d/%d, want 1/1",
			rules.interestedCalls, tasksClient.calls)
	}
	if resolver.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("unknown supergroup did work before task resolver=%d handle=%d",
			resolver.calls, rules.handleCalls)
	}
	if cacheCalls != 1 {
		t.Fatalf("entity cache calls=%d, want 1 after interest hit", cacheCalls)
	}

	if err := tasksClient.spec.Handler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || rules.handleCalls != 1 {
		t.Fatalf("unknown supergroup in-task resolver/handle=%d/%d, want 1/1",
			resolver.calls, rules.handleCalls)
	}
	if rules.last == nil || rules.last.Chat.Kind() != core.ChatKindSupergroup ||
		rules.last.Peer.Kind != core.PeerKindChannel {
		t.Fatalf("unknown supergroup envelope=%+v", rules.last)
	}
}

func TestP7IInactiveUnknownChannelStopsBeforeTaskAndResolver(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: false}
	resolver := &groupServiceResolverStub{
		resolved: &tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	}
	tasksClient := &p7iTaskClient{run: true}
	cacheCalls := 0

	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:     zap.NewNop(),
		GroupRules: rules,
		Resolver:   resolver,
		Tasks:      tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	}, &tg.Message{
		ID:      106,
		PeerID:  &tg.PeerChannel{ChannelID: 88},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "cold unknown channel",
	}, nil, nil)

	if rules.interestedCalls != 1 {
		t.Fatalf("interest calls=%d, want 1", rules.interestedCalls)
	}
	if cacheCalls != 0 || resolver.calls != 0 || tasksClient.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("inactive unknown channel cache=%d resolver=%d tasks=%d handle=%d, want 0/0/0/0",
			cacheCalls, resolver.calls, tasksClient.calls, rules.handleCalls)
	}
}

func TestP7IKnownBroadcastStillFailsClosedBeforeInterest(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: true}
	resolver := &groupServiceResolverStub{
		resolved: &tg.InputPeerChannel{ChannelID: 88, AccessHash: 188},
	}
	tasksClient := &p7iTaskClient{run: true}

	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:     zap.NewNop(),
		GroupRules: rules,
		Resolver:   resolver,
		Tasks:      tasksClient,
	}, &tg.Message{
		ID:      107,
		PeerID:  &tg.PeerChannel{ChannelID: 88},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "known broadcast",
	}, []tg.ChatClass{
		&tg.Channel{ID: 88, AccessHash: 188, Megagroup: false},
	}, nil)

	if rules.interestedCalls != 0 || resolver.calls != 0 ||
		tasksClient.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("known broadcast entered rule plane interested=%d resolver=%d tasks=%d handle=%d",
			rules.interestedCalls, resolver.calls, tasksClient.calls, rules.handleCalls)
	}
}
