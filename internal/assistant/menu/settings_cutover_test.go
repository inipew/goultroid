package menu_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"github.com/inipew/goultroid/internal/settings"
	"go.uber.org/zap"
)

func TestLegacySettingsMutationCallbacksCutOverWithoutPersistence(t *testing.T) {
	router := callback.NewRouter(zap.NewNop())
	ctrl := menu.NewController(presentation.RenderScreen)
	svc := settings.NewService(nil, settings.NewRegistry(), nil)
	ctrl.AttachSettingsRoutes(router, svc)

	fake := &fakeInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 50, 100, 1)
	ctrl.RegisterInstance(menu.MenuInstance{
		ID:        "menu:100:50",
		OwnerID:   100,
		ChatID:    100,
		MessageID: 50,
		Screen:    menu.ScreenIDSettings,
	})

	for i, action := range []string{"set", "reset"} {
		fake.lastAnswer = ""
		fake.lastEditedText = ""
		tx := callback.NewTransaction(int64(100+i), 100, callback.ParsedPayload{
			Namespace: "settings",
			Action:    action,
			State:     "core:prefix:next",
		}, target, fake)
		if err := router.Dispatch(context.Background(), tx); err != nil {
			t.Fatalf("%s compatibility dispatch error = %v", action, err)
		}
		if !strings.Contains(fake.lastAnswer, "moved") {
			t.Fatalf("%s answer = %q, want cutover notice", action, fake.lastAnswer)
		}
		if !strings.Contains(fake.lastEditedText, "Settings moved to a2") {
			t.Fatalf("%s edited text = %q", action, fake.lastEditedText)
		}
	}
}

func TestGenericLegacyTextHandlersRemainAvailableAfterSettingsCutover(t *testing.T) {
	ctrl := menu.NewController(presentation.RenderScreen)
	called := false
	ctrl.RegisterTextHandler(textHandlerFunc(func(_ context.Context, userID, chatID int64, text string, _ interaction.MessageInteraction) (bool, error) {
		called = true
		if userID != 100 || chatID != 100 || text != "hello" {
			t.Fatalf("unexpected generic text input: user=%d chat=%d text=%q", userID, chatID, text)
		}
		return true, nil
	}))
	handled, err := ctrl.HandleTextMessage(context.Background(), 100, 100, "hello", &fakeInteraction{})
	if err != nil || !handled || !called {
		t.Fatalf("generic handler handled=%v called=%v err=%v", handled, called, err)
	}
}

type textHandlerFunc func(context.Context, int64, int64, string, interaction.MessageInteraction) (bool, error)

func (f textHandlerFunc) HandleTextMessage(ctx context.Context, userID, chatID int64, text string, inter interaction.MessageInteraction) (bool, error) {
	return f(ctx, userID, chatID, text, inter)
}
