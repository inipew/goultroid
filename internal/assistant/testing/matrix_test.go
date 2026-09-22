package testing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	corecallback "github.com/inipew/goultroid/internal/services/callback"
	"go.uber.org/zap"
)

func TestIntegration_PostCutoverMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("CanonicalCallback_Malformed", func(t *testing.T) {
		if _, _, _, err := corecallback.ParseCallbackData([]byte("invalid_format")); !errors.Is(err, corecallback.ErrInvalidCallbackData) {
			t.Fatalf("expected ErrInvalidCallbackData, got %v", err)
		}
	})

	t.Run("CanonicalCallback_RejectsRetiredA1", func(t *testing.T) {
		if _, _, _, err := corecallback.ParseCallbackData([]byte("a1:assistant:start:noop")); !errors.Is(err, corecallback.ErrInvalidCallbackData) {
			t.Fatalf("expected retired a1 rejection, got %v", err)
		}
	})

	t.Run("CanonicalCallback_V1RoundTrip", func(t *testing.T) {
		data := corecallback.EncodeCallbackData("myxl", "refresh", "abc123")
		ns, action, opaqueID, err := corecallback.ParseCallbackData(data)
		if err != nil {
			t.Fatalf("parse v1 callback: %v", err)
		}
		if ns != "myxl" || action != "refresh" || opaqueID != "abc123" {
			t.Fatalf("unexpected callback coordinates: %q %q %q", ns, action, opaqueID)
		}
	})

	t.Run("Interaction_StaleAccessHashRecovery_Bounded", func(t *testing.T) {
		peerMock := NewFakePeerResolver()
		peerMock.ReResolveErr = errors.New("cannot resolve user")

		userPeer := &tg.InputPeerUser{UserID: 12345, AccessHash: 999}
		peerMock.InvalidatePeer(userPeer)
		if _, err := peerMock.ReResolve(ctx, userPeer); err == nil {
			t.Fatal("expected error from ReResolve")
		}
		if peerMock.ReResolveCalls != 1 || len(peerMock.Invalidated) != 1 {
			t.Fatalf("unexpected recovery calls: resolve=%d invalidated=%d", peerMock.ReResolveCalls, len(peerMock.Invalidated))
		}
	})

	t.Run("Client_RateLimiter", func(t *testing.T) {
		rl := client.NewUserRateLimiter(2, 500*time.Millisecond)
		userID := int64(98765)
		firstAllowed := rl.Allow(userID, "callback")
		secondAllowed := rl.Allow(userID, "callback")
		if !firstAllowed || !secondAllowed {
			t.Fatal("expected burst tokens to be allowed")
		}
		if rl.Allow(userID, "callback") {
			t.Fatal("expected third token within burst window to be rejected")
		}
	})

	t.Run("Command_Dispatch_AllCommands", func(t *testing.T) {
		r := command.NewRouter(zap.NewNop())
		r.Register("/start", func(c *command.Context) error {
			_, err := c.Reply("start", nil)
			return err
		})

		coreRouter := core.NewRouter(".")
		_ = coreRouter.RegisterBatch([]core.Command{
			{Name: "help", Surfaces: execution.SurfaceAssistant, Handler: func(c *core.Context) error { return c.Reply("help") }},
			{Name: "alive", Aliases: []string{"status"}, Surfaces: execution.SurfaceAssistant, Handler: func(c *core.Context) error { return c.Reply("alive") }},
		})
		r.SetCoreRouter(coreRouter)

		fake := NewFakeInteraction()
		peer := &tg.InputPeerUser{UserID: 12345}
		for _, cmd := range []string{"/start", "/help", "/status", "/alive"} {
			if err := r.Dispatch(ctx, 12345, peer, cmd, fake); err != nil {
				t.Fatalf("dispatch %s: %v", cmd, err)
			}
		}
		if len(fake.SentMessages) != 4 {
			t.Fatalf("expected 4 sent messages, got %d", len(fake.SentMessages))
		}
	})

	t.Run("Observability_Metrics_Emitted", func(t *testing.T) {
		metrics := core.NewDefaultMetricsTracker()
		cmdRouter := command.NewRouter(zap.NewNop())
		cmdRouter.SetMetricsCollector(metrics)

		coreRouter := core.NewRouter(".")
		_ = coreRouter.Register(core.Command{
			Name: "metricping", Surfaces: execution.SurfaceAssistant,
			Handler: func(c *core.Context) error { return c.Reply("pong") },
		})
		cmdRouter.SetCoreRouter(coreRouter)

		fake := NewFakeInteraction()
		peer := &tg.InputPeerUser{UserID: 12345}
		_ = cmdRouter.Dispatch(ctx, 12345, peer, "/metricping", fake)

		snap := metrics.Snapshot()
		if snap.TotalCommands == 0 {
			t.Fatal("expected TotalCommands > 0")
		}
		if _, ok := snap.Commands["metricping"]; !ok {
			t.Fatal("expected metricping in metrics stats")
		}
	})
}
