package menu_test

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"go.uber.org/zap"
)

type fakeInteraction struct {
	lastAnswer     string
	lastAlert      bool
	lastEditedText string
	deleted        bool
}

func (f *fakeInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	f.lastAnswer = text
	f.lastAlert = alert
	return nil
}

func (f *fakeInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	f.lastEditedText = text
	return nil
}

func (f *fakeInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	return nil
}

func (f *fakeInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	f.deleted = true
	return nil
}

func (f *fakeInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}

func (f *fakeInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

func TestMenuScreens(t *testing.T) {
	start := menu.BuildStartScreen("TestBot", 15*time.Minute)
	if start.ID != menu.ScreenIDStart || len(start.Rows) != 3 {
		t.Fatalf("unexpected start screen: %+v", start)
	}

	settings := menu.BuildSettingsScreen("TestBot")
	if settings.ID != menu.ScreenIDSettings || len(settings.Rows) != 2 {
		t.Fatalf("unexpected settings screen: %+v", settings)
	}

	help := menu.BuildHelpScreen("TestBot")
	if help.ID != menu.ScreenIDHelp || len(help.Rows) != 2 {
		t.Fatalf("unexpected help screen: %+v", help)
	}

	status := menu.BuildStatusScreen("TestBot", 15*time.Minute, "v2")
	if status.ID != menu.ScreenIDStatus || len(status.Rows) != 1 {
		t.Fatalf("unexpected status screen: %+v", status)
	}
}

func TestMemoryInstanceStore(t *testing.T) {
	store := menu.NewMemoryInstanceStore(50 * time.Millisecond)

	inst := menu.MenuInstance{
		ID:        "inst1",
		OwnerID:   123,
		ChatID:    456,
		MessageID: 789,
		Screen:    menu.ScreenIDStart,
	}
	store.Register(inst)

	got, ok := store.Get(456, 789)
	if !ok || got == nil || got.Screen != menu.ScreenIDStart {
		t.Fatalf("expected to get registered instance, got ok=%v, inst=%+v", ok, got)
	}

	store.UpdateScreen(456, 789, menu.ScreenIDSettings)
	got, _ = store.Get(456, 789)
	if got.Screen != menu.ScreenIDSettings {
		t.Fatalf("expected updated screen to be settings, got %v", got.Screen)
	}

	// Expiration check
	time.Sleep(60 * time.Millisecond)
	_, ok = store.Get(456, 789)
	if ok {
		t.Fatalf("expected expired instance to be pruned")
	}

	// Invalidation
	store.Register(inst)
	store.Invalidate(456, 789)
	if _, ok := store.Get(456, 789); ok {
		t.Fatalf("expected invalidated instance to not be found")
	}
}

func TestController_AttachRoutes(t *testing.T) {
	router := callback.NewRouter(zap.NewNop())
	ctrl := menu.NewController(presentation.RenderScreen)

	startTime := time.Now().Add(-1 * time.Hour)
	ctrl.AttachRoutes(router, func() string { return "MyTestBot" }, func() time.Time { return startTime })

	fake := &fakeInteraction{}
	ctx := context.Background()
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 50, 100, 1)

	// 1. Dispatch "start"
	txStart := callback.NewTransaction(1, 100, callback.ParsedPayload{Namespace: "assistant", Action: "start"}, target, fake)
	if err := router.Dispatch(ctx, txStart); err != nil {
		t.Fatalf("unexpected error on start: %v", err)
	}
	if fake.lastEditedText == "" {
		t.Fatalf("expected edited text on start")
	}

	// 2. Dispatch "ping"
	fake.lastAnswer = ""
	fake.lastAlert = false
	txPing := callback.NewTransaction(2, 100, callback.ParsedPayload{Namespace: "assistant", Action: "ping"}, target, fake)
	if err := router.Dispatch(ctx, txPing); err != nil {
		t.Fatalf("unexpected error on ping: %v", err)
	}
	if fake.lastAnswer != "🏓 Pong!" || !fake.lastAlert {
		t.Fatalf("expected ping alert answer, got text=%q alert=%v", fake.lastAnswer, fake.lastAlert)
	}

	// 3. Dispatch "close"
	fake.deleted = false
	txClose := callback.NewTransaction(3, 100, callback.ParsedPayload{Namespace: "assistant", Action: "close"}, target, fake)
	if err := router.Dispatch(ctx, txClose); err != nil {
		t.Fatalf("unexpected error on close: %v", err)
	}
	if !fake.deleted {
		t.Fatalf("expected message to be deleted on close")
	}
}
