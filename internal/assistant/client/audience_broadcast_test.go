package client

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	broadcastsvc "github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type audienceBroadcastAPI struct {
	sent atomic.Int32
}

func (*audienceBroadcastAPI) MessagesSetBotCallbackAnswer(context.Context, *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	return true, nil
}
func (*audienceBroadcastAPI) MessagesEditMessage(context.Context, *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	return &tg.Updates{}, nil
}
func (*audienceBroadcastAPI) MessagesDeleteMessages(context.Context, *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	return &tg.MessagesAffectedMessages{}, nil
}
func (*audienceBroadcastAPI) ChannelsDeleteMessages(context.Context, *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	return &tg.MessagesAffectedMessages{}, nil
}
func (*audienceBroadcastAPI) MessagesGetMessages(context.Context, []tg.InputMessageClass) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesMessages{}, nil
}
func (*audienceBroadcastAPI) ChannelsGetMessages(context.Context, *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error) {
	return &tg.MessagesChannelMessages{}, nil
}
func (a *audienceBroadcastAPI) MessagesSendMessage(_ context.Context, _ *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	id := a.sent.Add(1)
	return &tg.UpdateShortSentMessage{ID: int(id), Date: int(time.Now().Unix())}, nil
}
func (*audienceBroadcastAPI) MessagesSendMedia(context.Context, *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	return &tg.Updates{}, nil
}
func (*audienceBroadcastAPI) MessagesForwardMessages(context.Context, *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	return &tg.Updates{}, nil
}
func (*audienceBroadcastAPI) MessagesEditInlineBotMessage(context.Context, *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	return true, nil
}

type defaultBroadcastTransport struct {
	core.MockTelegramServicer
	sent atomic.Int32
}

func (t *defaultBroadcastTransport) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	t.sent.Add(1)
	return &tg.Message{ID: int(t.sent.Load())}, nil
}

func TestAssistantBroadcastAudienceUsesExistingBroadcastEngineAndBotSender(t *testing.T) {
	ctx := context.Background()
	registry, _ := newAssistantAudienceRegistry(t)
	for _, userID := range []int64{11, 22, 33} {
		if _, err := registry.TouchAudience(ctx, pmrelay.AudienceTouch{
			UserID: userID, Source: pmrelay.AudienceSourceStart, SeenAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 16, PayloadBudget: 1 << 20},
		},
	})
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	})

	defaultTransport := &defaultBroadcastTransport{}
	broadcast := broadcastsvc.NewService(defaultTransport, zap.NewNop())
	broadcast.SetTasks(engine)

	c := NewAssistantClient(1, "hash", "token", zap.NewNop())
	c.SetAudienceRegistry(registry)
	c.SetBroadcastService(broadcast)
	for _, userID := range []int64{11, 22, 33} {
		c.resolver.Cache().Put(peer.PeerRecord{
			ID: userID, Kind: peer.PeerKindUser, AccessHash: userID * 100,
		})
	}
	api := &audienceBroadcastAPI{}
	c.mu.Lock()
	c.interaction = assistantinteraction.NewClientInteraction(api, zap.NewNop())
	c.mu.Unlock()

	report, err := c.BroadcastAudience(ctx, broadcastsvc.BroadcastRequest{Text: "hello audience"})
	if err != nil {
		t.Fatalf("BroadcastAudience() error=%v", err)
	}
	if report.Total != 3 || report.Sent != 3 || report.Failed != 0 {
		t.Fatalf("audience broadcast report=%+v", report)
	}
	if got := api.sent.Load(); got != 3 {
		t.Fatalf("Assistant MTProto sends=%d, want 3", got)
	}
	if got := defaultTransport.sent.Load(); got != 0 {
		t.Fatalf("default userbot broadcast transport calls=%d, want 0", got)
	}
}

type shrinkingAudienceRegistry struct {
	now time.Time
}

func (r *shrinkingAudienceRegistry) TouchAudience(_ context.Context, touch pmrelay.AudienceTouch) (pmrelay.AudienceMember, error) {
	return pmrelay.AudienceMember{
		UserID: touch.UserID, Sources: touch.Source,
		FirstSeenAt: touch.SeenAt, LastSeenAt: touch.SeenAt,
	}, nil
}

func (r *shrinkingAudienceRegistry) SnapshotAudience(context.Context) (pmrelay.AudienceSnapshot, error) {
	return pmrelay.AudienceSnapshot{MaxSequence: 2, Total: 2}, nil
}

func (r *shrinkingAudienceRegistry) ListAudienceSnapshot(
	_ context.Context,
	_ pmrelay.AudienceSnapshot,
	after int64,
	_ int,
) ([]pmrelay.AudienceMember, int64, error) {
	if after > 0 {
		return nil, 2, nil
	}
	return []pmrelay.AudienceMember{{
		UserID:      11,
		Sources:     pmrelay.AudienceSourceStart,
		FirstSeenAt: r.now,
		LastSeenAt:  r.now,
	}}, 2, nil
}

func TestAssistantBroadcastAudienceAccountsForPrunedSnapshotMember(t *testing.T) {
	ctx := context.Background()
	engine := taskengine.NewEngine(taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {Concurrency: 2, BacklogLimit: 16, PayloadBudget: 1 << 20},
		},
	})
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = engine.Stop(stopCtx)
	})

	broadcast := broadcastsvc.NewService(&defaultBroadcastTransport{}, zap.NewNop())
	broadcast.SetTasks(engine)

	c := NewAssistantClient(1, "hash", "token", zap.NewNop())
	c.SetAudienceRegistry(&shrinkingAudienceRegistry{now: time.Now().UTC()})
	c.SetBroadcastService(broadcast)
	c.resolver.Cache().Put(peer.PeerRecord{
		ID: 11, Kind: peer.PeerKindUser, AccessHash: 1100,
	})
	api := &audienceBroadcastAPI{}
	c.mu.Lock()
	c.interaction = assistantinteraction.NewClientInteraction(api, zap.NewNop())
	c.mu.Unlock()

	report, err := c.BroadcastAudience(ctx, broadcastsvc.BroadcastRequest{Text: "snapshot"})
	if err != nil {
		t.Fatalf("BroadcastAudience(shrunk snapshot) error=%v", err)
	}
	if report.Total != 2 || report.Sent != 1 || report.Failed != 1 {
		t.Fatalf("shrunk snapshot report=%+v, want total=2 sent=1 failed=1", report)
	}
	if got := api.sent.Load(); got != 1 {
		t.Fatalf("Assistant MTProto sends=%d, want 1", got)
	}
}

func TestAssistantBroadcastAudienceRejectsCallerTargetAuthority(t *testing.T) {
	c := NewAssistantClient(1, "hash", "token", zap.NewNop())

	_, err := c.BroadcastAudience(context.Background(), broadcastsvc.BroadcastRequest{
		Targets: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 42}},
		Text:    "invalid targets",
	})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("BroadcastAudience(caller targets) error=%v, want %v", err, core.ErrInvalidArgs)
	}

	_, err = c.BroadcastAudience(context.Background(), broadcastsvc.BroadcastRequest{
		Sender: &core.MockTelegramServicer{},
		Text:   "invalid sender",
	})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("BroadcastAudience(caller sender) error=%v, want %v", err, core.ErrInvalidArgs)
	}
}
