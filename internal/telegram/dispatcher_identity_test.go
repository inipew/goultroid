package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestTelegramMessageIdentitySeparatesPeerNamespaces(t *testing.T) {
	tests := []struct {
		name string
		peer tg.PeerClass
		want string
	}{
		{name: "user", peer: &tg.PeerUser{UserID: 42}, want: "user:42:9"},
		{name: "chat", peer: &tg.PeerChat{ChatID: 42}, want: "chat:42:9"},
		{name: "channel", peer: &tg.PeerChannel{ChannelID: 42}, want: "channel:42:9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := telegramMessageIdentity(tt.peer, 9)
			if !ok || got != tt.want {
				t.Fatalf("telegramMessageIdentity()=(%q,%v), want (%q,true)", got, ok, tt.want)
			}
		})
	}
}

func TestDispatcherCommandIdentitySeparatesPeerNamespaces(t *testing.T) {
	client := &retryAdmissionTaskClient{}
	d, mgr := newRetryCommandDispatcher(t, client)
	ctx := context.Background()

	chatMessage := &tg.Message{
		ID:      93,
		PeerID:  &tg.PeerChat{ChatID: 42},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".retry",
	}
	channelMessage := &tg.Message{
		ID:      93,
		PeerID:  &tg.PeerChannel{ChannelID: 42},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".retry",
	}
	entities := tg.Entities{Channels: map[int64]*tg.Channel{
		42: {ID: 42, AccessHash: 12345, Title: "same numeric id"},
	}}

	if err := d.dispatch(ctx, tg.Entities{}, chatMessage); err != nil {
		t.Fatalf("chat dispatch: %v", err)
	}
	if err := d.dispatch(ctx, entities, channelMessage); err != nil {
		t.Fatalf("channel dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 2 {
		t.Fatalf("submissions=%d, want 2 distinct typed peer commands", got)
	}
	for _, key := range []string{"msg:chat:42:93", "msg:channel:42:93"} {
		processed, err := mgr.IsProcessedContext(ctx, key)
		if err != nil || !processed {
			t.Fatalf("typed command key %q processed=%v err=%v", key, processed, err)
		}
	}
}

func TestDispatcherCommandIdentityRejectsInvalidPeer(t *testing.T) {
	client := &retryAdmissionTaskClient{}
	d, _ := newRetryCommandDispatcher(t, client)
	msg := &tg.Message{ID: 96, PeerID: &tg.PeerChat{}, FromID: &tg.PeerUser{UserID: 200}, Message: ".retry"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 0 {
		t.Fatalf("invalid peer reached TaskEngine: submissions=%d", got)
	}
}

func TestDispatcherCommandIdentityHonorsLegacyClaimDuringRollout(t *testing.T) {
	client := &retryAdmissionTaskClient{}
	d, mgr := newRetryCommandDispatcher(t, client)
	ctx := context.Background()

	claimed, err := mgr.CheckAndSet(ctx, "msg:42:94", 5*time.Minute)
	if err != nil || !claimed {
		t.Fatalf("seed legacy claim: claimed=%v err=%v", claimed, err)
	}
	msg := &tg.Message{
		ID:      94,
		PeerID:  &tg.PeerChat{ChatID: 42},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".retry",
	}
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 0 {
		t.Fatalf("legacy claimed replay reached TaskEngine: submissions=%d", got)
	}
	processed, err := mgr.IsProcessedContext(ctx, "msg:chat:42:94")
	if err != nil {
		t.Fatalf("typed claim lookup: %v", err)
	}
	if processed {
		t.Fatal("legacy replay unexpectedly created a new typed claim")
	}
}

func TestDispatcherCommandIdentityStopsLegacyLookupAfterRolloutWindow(t *testing.T) {
	client := &retryAdmissionTaskClient{}
	d, mgr := newRetryCommandDispatcher(t, client)
	d.legacyCommandIdentityUntil = time.Now().Add(-time.Second)
	ctx := context.Background()

	claimed, err := mgr.CheckAndSet(ctx, "msg:42:95", 5*time.Minute)
	if err != nil || !claimed {
		t.Fatalf("seed legacy claim: claimed=%v err=%v", claimed, err)
	}
	msg := &tg.Message{
		ID:      95,
		PeerID:  &tg.PeerChat{ChatID: 42},
		FromID:  &tg.PeerUser{UserID: 200},
		Message: ".retry",
	}
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := client.submissions.Load(); got != 1 {
		t.Fatalf("post-rollout typed command submissions=%d, want 1", got)
	}
	processed, err := mgr.IsProcessedContext(ctx, "msg:chat:42:95")
	if err != nil || !processed {
		t.Fatalf("typed claim after rollout processed=%v err=%v", processed, err)
	}
}
