package client

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
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

type p7iChatClassifierStub struct {
	calls int
	chat  core.Chat
	err   error
}

func (s *p7iChatClassifierStub) Classify(
	_ context.Context,
	message *tg.Message,
	_ tg.Entities,
	inputPeer tg.InputPeerClass,
) (core.Chat, error) {
	s.calls++
	if s.err != nil {
		return core.Chat{}, s.err
	}
	if s.chat.ID != 0 {
		return s.chat, nil
	}
	switch peer := inputPeer.(type) {
	case *tg.InputPeerChat:
		return core.Chat{ID: peer.ChatID, Type: string(core.ChatKindGroup)}, nil
	case *tg.InputPeerChannel:
		return core.Chat{
			ID: peer.ChannelID, Type: string(core.ChatKindSupergroup), AccessHash: peer.AccessHash,
		}, nil
	default:
		return core.Chat{}, core.ErrGroupOnly
	}
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
	err   error
	spec  tasks.WorkSpec
}

func (c *p7iTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.calls++
	c.spec = spec
	if c.err != nil {
		return nil, c.err
	}
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
	if deps.GroupRules != nil && deps.GroupRuleChats == nil {
		deps.GroupRuleChats = &p7iChatClassifierStub{}
	}
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
	classifier := &p7iChatClassifierStub{}
	tasksClient := &p7iTaskClient{run: false}
	cacheCalls := 0

	deps := UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		GroupRuleChats: classifier,
		Resolver:       resolver,
		Tasks:          tasksClient,
		SelfID:         func() int64 { return 999 },
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
	classifier := &p7iChatClassifierStub{
		chat: core.Chat{ID: 88, Type: string(core.ChatKindSupergroup), AccessHash: 188},
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
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		GroupRuleChats: classifier,
		Resolver:       resolver,
		Tasks:          tasksClient,
		SelfID:         func() int64 { return 999 },
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
	if resolver.calls != 0 || classifier.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("unknown supergroup did work before task resolver=%d classifier=%d handle=%d",
			resolver.calls, classifier.calls, rules.handleCalls)
	}
	if cacheCalls != 1 {
		t.Fatalf("entity cache calls=%d, want 1 after interest hit", cacheCalls)
	}

	if err := tasksClient.spec.Handler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || classifier.calls != 1 || rules.handleCalls != 1 {
		t.Fatalf("unknown supergroup in-task resolver/classifier/handle=%d/%d/%d, want 1/1/1",
			resolver.calls, classifier.calls, rules.handleCalls)
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

func TestP7IGlobalPrivilegedSenderStopsBeforeInterestCacheAndTask(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: true}
	resolver := &groupServiceResolverStub{resolved: &tg.InputPeerChat{ChatID: 77}}
	tasksClient := &p7iTaskClient{run: true}
	cacheCalls := 0
	privilegedCalls := 0

	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:     zap.NewNop(),
		GroupRules: rules,
		GlobalPrivileged: func(userID int64) bool {
			privilegedCalls++
			return userID == 42
		},
		Resolver: resolver,
		Tasks:    tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	}, &tg.Message{
		ID:      108,
		PeerID:  &tg.PeerChat{ChatID: 77},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "owner or sudo ordinary text",
	}, nil, nil)

	if privilegedCalls != 1 {
		t.Fatalf("privileged checks=%d, want 1", privilegedCalls)
	}
	if rules.interestedCalls != 0 || rules.handleCalls != 0 {
		t.Fatalf("privileged sender touched rule interest/handle=%d/%d, want 0/0",
			rules.interestedCalls, rules.handleCalls)
	}
	if cacheCalls != 0 || resolver.calls != 0 || tasksClient.calls != 0 {
		t.Fatalf("privileged cold bypass cache=%d resolver=%d tasks=%d, want 0/0/0",
			cacheCalls, resolver.calls, tasksClient.calls)
	}
}

func TestP7JGroupRuleOrderingIsTopicScoped(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: true}
	tasksClient := &p7iTaskClient{}
	resolver := &groupServiceResolverStub{resolved: &tg.InputPeerChannel{ChannelID: 77, AccessHash: 99}}

	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		Resolver:       resolver,
		Tasks:          tasksClient,
		GroupRuleChats: &p7iChatClassifierStub{},
	}, &tg.Message{
		ID:      701,
		PeerID:  &tg.PeerChannel{ChannelID: 77},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "ordinary topic text",
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:   true,
			ReplyToMsgID: 510,
			ReplyToTopID: 500,
		},
	}, []tg.ChatClass{&tg.Channel{
		ID:         77,
		AccessHash: 99,
		Megagroup:  true,
	}}, []tg.UserClass{&tg.User{ID: 42}})

	if tasksClient.calls != 1 {
		t.Fatalf("TaskEngine calls=%d want 1", tasksClient.calls)
	}
	if tasksClient.spec.OrderingKey != "chat:77:topic:500" {
		t.Fatalf("ordering=%q want chat:77:topic:500", tasksClient.spec.OrderingKey)
	}
}

func TestP7LGroupRuleAdmissionRejectionStopsBeforeExecution(t *testing.T) {
	rules := &p7iRuleIngressStub{interested: true}
	resolver := &groupServiceResolverStub{resolved: &tg.InputPeerChat{ChatID: 77}}
	classifier := &p7iChatClassifierStub{}
	tasksClient := &p7iTaskClient{
		err: tasks.NewAdmissionError(tasks.ReasonOwnerQueueFull, tasks.ErrOwnerQueueFull),
	}
	cacheCalls := 0

	p7iDispatchMessage(t, UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		GroupRuleChats: classifier,
		Resolver:       resolver,
		Tasks:          tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	}, &tg.Message{
		ID:      801,
		PeerID:  &tg.PeerChat{ChatID: 77},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "candidate rule text",
	}, nil, nil)

	if rules.interestedCalls != 1 || tasksClient.calls != 1 {
		t.Fatalf("interested/tasks=%d/%d want 1/1", rules.interestedCalls, tasksClient.calls)
	}
	if cacheCalls != 1 {
		t.Fatalf("entity cache calls=%d want 1 after interest hit", cacheCalls)
	}
	if resolver.calls != 0 || classifier.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("rejected task executed resolver/classifier/handle=%d/%d/%d",
			resolver.calls, classifier.calls, rules.handleCalls)
	}
}

func TestP7LHighCardinalityIrrelevantGroupsStayCold(t *testing.T) {
	rules := &p7iRuleIngressStub{}
	resolver := &groupServiceResolverStub{}
	tasksClient := &p7iTaskClient{}
	cacheCalls := 0

	dispatcher := tg.NewUpdateDispatcher()
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		GroupRuleChats: &p7iChatClassifierStub{},
		Resolver:       resolver,
		Tasks:          tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	})

	ctx := context.Background()
	const total = 2048
	for i := 0; i < total; i++ {
		chatID := int64(100000 + i)
		err := dispatcher.Handle(ctx, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{
				ID:      i + 1,
				PeerID:  &tg.PeerChat{ChatID: chatID},
				FromID:  &tg.PeerUser{UserID: int64(200000 + i)},
				Message: "ordinary irrelevant group text",
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if rules.interestedCalls != total {
		t.Fatalf("interest checks=%d want %d", rules.interestedCalls, total)
	}
	if cacheCalls != 0 || resolver.calls != 0 || tasksClient.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("high-cardinality cold path cache=%d resolver=%d tasks=%d handle=%d",
			cacheCalls, resolver.calls, tasksClient.calls, rules.handleCalls)
	}
}

func BenchmarkP7LIrrelevantGroupMessageHotPath(b *testing.B) {
	rules := &p7iRuleIngressStub{}
	resolver := &groupServiceResolverStub{}
	tasksClient := &p7iTaskClient{}
	cacheCalls := 0

	dispatcher := tg.NewUpdateDispatcher()
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		GroupRuleChats: &p7iChatClassifierStub{},
		Resolver:       resolver,
		Tasks:          tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	})
	update := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{
			ID:      1,
			PeerID:  &tg.PeerChat{ChatID: 77},
			FromID:  &tg.PeerUser{UserID: 42},
			Message: "ordinary irrelevant group text",
		}}},
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dispatcher.Handle(ctx, update); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if cacheCalls != 0 || resolver.calls != 0 || tasksClient.calls != 0 || rules.handleCalls != 0 {
		b.Fatalf("cold path escaped interest gate cache=%d resolver=%d tasks=%d handle=%d",
			cacheCalls, resolver.calls, tasksClient.calls, rules.handleCalls)
	}
}

type p7lProcessFootprint struct {
	goroutines int
	heapAlloc  uint64
	rss        uint64
}

func p7lCurrentRSS() uint64 {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func p7lMeasureProcessFootprint() p7lProcessFootprint {
	runtime.GC()
	debug.FreeOSMemory()
	runtime.Gosched()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return p7lProcessFootprint{
		goroutines: runtime.NumGoroutine(),
		heapAlloc:  stats.HeapAlloc,
		rss:        p7lCurrentRSS(),
	}
}

func TestP7LHighCardinalityColdTrafficDoesNotAmplifyIdleProcessState(t *testing.T) {
	rules := &p7iRuleIngressStub{}
	resolver := &groupServiceResolverStub{}
	tasksClient := &p7iTaskClient{}
	cacheCalls := 0

	dispatcher := tg.NewUpdateDispatcher()
	RegisterUpdateHandlers(&dispatcher, UpdateHandlerDeps{
		Logger:         zap.NewNop(),
		GroupRules:     rules,
		GroupRuleChats: &p7iChatClassifierStub{},
		Resolver:       resolver,
		Tasks:          tasksClient,
		CacheEntities: func(tg.Entities) {
			cacheCalls++
		},
	})

	before := p7lMeasureProcessFootprint()
	ctx := context.Background()
	const total = 8192
	for i := 0; i < total; i++ {
		chatID := int64(500000 + i)
		if err := dispatcher.Handle(ctx, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{
				ID:      i + 1,
				PeerID:  &tg.PeerChat{ChatID: chatID},
				FromID:  &tg.PeerUser{UserID: int64(600000 + i)},
				Message: "irrelevant high-cardinality cold traffic",
			}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	after := p7lMeasureProcessFootprint()

	t.Logf(
		"P7-L idle footprint: groups=%d goroutines=%d->%d heap=%d->%d rss=%d->%d",
		total,
		before.goroutines, after.goroutines,
		before.heapAlloc, after.heapAlloc,
		before.rss, after.rss,
	)

	if cacheCalls != 0 || resolver.calls != 0 || tasksClient.calls != 0 || rules.handleCalls != 0 {
		t.Fatalf("cold traffic escaped interest gate cache=%d resolver=%d tasks=%d handle=%d",
			cacheCalls, resolver.calls, tasksClient.calls, rules.handleCalls)
	}
	if after.goroutines > before.goroutines+2 {
		t.Fatalf("cold high-cardinality traffic amplified goroutines %d -> %d",
			before.goroutines, after.goroutines)
	}
	const maxHeapGrowth = 8 << 20
	if after.heapAlloc > before.heapAlloc+maxHeapGrowth {
		t.Fatalf("cold high-cardinality traffic retained >%d bytes heap: %d -> %d",
			maxHeapGrowth, before.heapAlloc, after.heapAlloc)
	}
	const maxRSSGrowth = 32 << 20
	if before.rss > 0 && after.rss > before.rss+maxRSSGrowth {
		t.Fatalf("cold high-cardinality traffic retained >%d bytes RSS: %d -> %d",
			maxRSSGrowth, before.rss, after.rss)
	}
}
