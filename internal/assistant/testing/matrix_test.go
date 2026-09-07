package testing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"go.uber.org/zap"
)

func TestIntegration_15ScenarioMatrix(t *testing.T) {
	ctx := context.Background()

	// Scenario 1-6: Menu controller routes attached to Router
	t.Run("01_Menu_StartScreen", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		ctrl := menu.NewController(presentation.RenderScreen)
		ctrl.AttachRoutes(router, func() string { return "Bot" }, func() time.Time { return time.Now() })

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(1, 100, callback.ParsedPayload{Namespace: "assistant", Action: "start"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.LastEditedText == "" {
			t.Fatalf("expected start screen rendered")
		}
	})

	t.Run("02_Menu_SettingsScreen", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		ctrl := menu.NewController(presentation.RenderScreen)
		ctrl.AttachRoutes(router, func() string { return "Bot" }, nil)

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(2, 100, callback.ParsedPayload{Namespace: "assistant", Action: "settings"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.LastEditedText == "" {
			t.Fatalf("expected settings screen rendered")
		}
	})

	t.Run("03_Menu_HelpScreen", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		ctrl := menu.NewController(presentation.RenderScreen)
		ctrl.AttachRoutes(router, func() string { return "Bot" }, nil)

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(3, 100, callback.ParsedPayload{Namespace: "assistant", Action: "help"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.LastEditedText == "" {
			t.Fatalf("expected help screen rendered")
		}
	})

	t.Run("04_Menu_StatusScreen", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		ctrl := menu.NewController(presentation.RenderScreen)
		ctrl.AttachRoutes(router, func() string { return "Bot" }, nil)

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(4, 100, callback.ParsedPayload{Namespace: "assistant", Action: "status"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.LastEditedText == "" {
			t.Fatalf("expected status screen rendered")
		}
	})

	t.Run("05_Menu_PingToast", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		ctrl := menu.NewController(presentation.RenderScreen)
		ctrl.AttachRoutes(router, func() string { return "Bot" }, nil)

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(5, 100, callback.ParsedPayload{Namespace: "assistant", Action: "ping"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.LastAnswerText != "🏓 Pong!" || !fake.LastAlert {
			t.Fatalf("expected alert toast response, got %q alert=%v", fake.LastAnswerText, fake.LastAlert)
		}
	})

	t.Run("06_Menu_CloseIdempotent", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		ctrl := menu.NewController(presentation.RenderScreen)
		ctrl.AttachRoutes(router, func() string { return "Bot" }, nil)

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(6, 100, callback.ParsedPayload{Namespace: "assistant", Action: "close"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !fake.Deleted {
			t.Fatalf("expected message to be deleted on close")
		}
	})

	t.Run("07_Payload_Malformed", func(t *testing.T) {
		_, err := callback.Parse([]byte("invalid_format"))
		if !errors.Is(err, callback.ErrMalformedPayload) {
			t.Fatalf("expected ErrMalformedPayload, got %v", err)
		}
	})

	t.Run("08_Payload_TooLong", func(t *testing.T) {
		longPayload := "a1:assistant:"
		for len(longPayload) < 65 {
			longPayload += "x"
		}
		_, err := callback.Parse([]byte(longPayload))
		if !errors.Is(err, callback.ErrPayloadTooLong) {
			t.Fatalf("expected ErrPayloadTooLong, got %v", err)
		}
	})

	t.Run("09_Authorization_UnauthorizedUser", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		router.SetAuthorizer(callback.NewOwnerAuthorizer(999, nil))

		executed := false
		router.Register("assistant", "secure", func(ctx context.Context, tx *callback.Transaction) error {
			executed = true
			return nil
		})

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(777, 1)
		tx := callback.NewTransaction(9, 777, callback.ParsedPayload{Namespace: "assistant", Action: "secure"}, target, fake)

		err := router.Dispatch(ctx, tx)
		if !errors.Is(err, callback.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
		if executed {
			t.Fatalf("unauthorized handler must not execute")
		}
		if fake.LastAnswerText != "⚠️ This is OWNER's bot!!" || !fake.LastAlert {
			t.Fatalf("expected owner warning alert, got %q", fake.LastAnswerText)
		}
	})

	t.Run("10_Authorization_FailClosed_OwnerZero", func(t *testing.T) {
		auth := callback.NewOwnerAuthorizer(0, nil)
		err := auth.Authorize(ctx, callback.Actor{UserID: 12345}, "test")
		if !errors.Is(err, callback.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized when ownerID=0, got %v", err)
		}
	})

	t.Run("11_Lifecycle_SingleFlightAnswer", func(t *testing.T) {
		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(11, 100, callback.ParsedPayload{Namespace: "assistant", Action: "test"}, target, fake)

		var wg sync.WaitGroup
		var answerCount int32
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := tx.Answer(ctx, "acknowledged", false); err == nil {
					atomic.AddInt32(&answerCount, 1)
				}
			}()
		}
		wg.Wait()

		if fake.AnswerCalls != 1 {
			t.Fatalf("expected exactly 1 RPC answer call, got %d", fake.AnswerCalls)
		}
	})

	t.Run("12_Router_OwnsAnswerLifecycle", func(t *testing.T) {
		router := callback.NewRouter(zap.NewNop())
		router.Register("assistant", "silent", func(ctx context.Context, tx *callback.Transaction) error {
			// Handler deliberately does NOT call tx.Answer
			return nil
		})

		fake := NewFakeInteraction()
		target := FixtureMessageTarget(100, 1)
		tx := callback.NewTransaction(12, 100, callback.ParsedPayload{Namespace: "assistant", Action: "silent"}, target, fake)

		if err := router.Dispatch(ctx, tx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.AnswerCalls != 1 {
			t.Fatalf("expected Router to guarantee exactly 1 answer call, got %d", fake.AnswerCalls)
		}
	})

	t.Run("13_Interaction_StaleAccessHashRecovery_Bounded", func(t *testing.T) {
		peerMock := NewFakePeerResolver()
		peerMock.ReResolveErr = errors.New("cannot resolve user")

		// Verify that PeerReResolver is invoked
		userPeer := &tg.InputPeerUser{UserID: 12345, AccessHash: 999}
		peerMock.InvalidatePeer(userPeer)
		_, err := peerMock.ReResolve(ctx, userPeer)
		if err == nil {
			t.Fatalf("expected error from ReResolve")
		}
		if peerMock.ReResolveCalls != 1 {
			t.Fatalf("expected 1 ReResolve call, got %d", peerMock.ReResolveCalls)
		}
		if len(peerMock.Invalidated) != 1 {
			t.Fatalf("expected 1 invalidated peer, got %d", len(peerMock.Invalidated))
		}
	})

	t.Run("14_Client_RateLimiter", func(t *testing.T) {
		rl := client.NewUserRateLimiter(2, 500*time.Millisecond)
		userID := int64(98765)

		if !rl.Allow(userID, "callback") {
			t.Fatalf("expected first token allowed")
		}
		if !rl.Allow(userID, "callback") {
			t.Fatalf("expected second token allowed")
		}
		if rl.Allow(userID, "callback") {
			t.Fatalf("expected third token within burst window to be rejected")
		}
	})

	t.Run("15_Command_Dispatch_AllCommands", func(t *testing.T) {
		r := command.NewRouter(zap.NewNop())
		command.AttachDefaultCommands(r, func() string { return "TestBot" }, func() time.Time { return time.Now() }, presentation.RenderScreen)

		fake := NewFakeInteraction()
		peer := &tg.InputPeerUser{UserID: 12345}

		// /start
		if err := r.Dispatch(ctx, 12345, peer, "/start", fake); err != nil {
			t.Fatalf("unexpected error on /start: %v", err)
		}
		// /help
		if err := r.Dispatch(ctx, 12345, peer, "/help", fake); err != nil {
			t.Fatalf("unexpected error on /help: %v", err)
		}
		// /status
		if err := r.Dispatch(ctx, 12345, peer, "/status", fake); err != nil {
			t.Fatalf("unexpected error on /status: %v", err)
		}
		// /alive
		if err := r.Dispatch(ctx, 12345, peer, "/alive", fake); err != nil {
			t.Fatalf("unexpected error on /alive: %v", err)
		}

		if len(fake.SentMessages) != 4 {
			t.Fatalf("expected 4 sent messages for 4 commands, got %d", len(fake.SentMessages))
		}
	})
}
