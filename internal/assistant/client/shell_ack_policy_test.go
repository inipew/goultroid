package client

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

func TestAssistantShellNavigationAcknowledgesBeforeRender(t *testing.T) {
	manager, _, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)

	assertPolicy := func(actionID string, want rootinteraction.AckPolicy) {
		t.Helper()
		data := callbackForAction(t, port.sent, actionID)
		prepared, err := engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
			Data:    data,
			ActorID: 7,
			QueryID: 8800,
			Target: presentationtelegram.MessageTarget{
				Peer:      peer,
				ChatID:    7,
				MessageID: 77,
			},
		})
		if err != nil {
			t.Fatalf("PrepareCallback(%s) error=%v", actionID, err)
		}
		aware, ok := prepared.(orchestration.AckPreparedCallback)
		if !ok {
			t.Fatalf("PrepareCallback(%s) does not expose AckPolicy", actionID)
		}
		if got := aware.AckPolicy(); got != want {
			t.Fatalf("AckPolicy(%s)=%v, want %v", actionID, got, want)
		}
	}

	assertPolicy(assistantshell.ActionHelp, rootinteraction.AckImmediate)
	assertPolicy(assistantshell.ActionStatus, rootinteraction.AckImmediate)
	assertPolicy(assistantshell.ActionSettings, rootinteraction.AckImmediate)
	assertPolicy(assistantshell.ActionPing, rootinteraction.AckHandlerOwned)
}
