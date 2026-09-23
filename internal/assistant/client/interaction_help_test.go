package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
)

type publicStartInteraction struct {
	sent string
}

func (*publicStartInteraction) Answer(context.Context, int64, string, bool) error { return nil }
func (*publicStartInteraction) Edit(context.Context, assistantinteraction.MessageTarget, string, tg.ReplyMarkupClass) error {
	return nil
}
func (*publicStartInteraction) EditMarkup(context.Context, assistantinteraction.MessageTarget, tg.ReplyMarkupClass) error {
	return nil
}
func (*publicStartInteraction) Delete(context.Context, assistantinteraction.MessageTarget) error {
	return nil
}
func (*publicStartInteraction) GetMessage(context.Context, assistantinteraction.MessageTarget) (*tg.Message, error) {
	return nil, nil
}
func (i *publicStartInteraction) SendMessage(_ context.Context, _ tg.InputPeerClass, text string, _ tg.ReplyMarkupClass) (*tg.Message, error) {
	i.sent = text
	return &tg.Message{ID: 1}, nil
}
func (*publicStartInteraction) SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error) {
	return nil, nil
}

func TestAssistantShellDetailedHelpParityUsesOneRevisionFencedSession(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	router := core.NewRouter(".")
	if err := router.RegisterBatch([]core.Command{
		{
			Name: "beta", Category: "Media", Description: "Second media command", Usage: "beta <url>",
			Aliases: []string{"b"}, Permission: core.PermissionSudo, Cooldown: time.Second, Timeout: 5 * time.Second,
			Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil },
		},
		{
			Name: "alpha", Category: "Media", Description: "First media command",
			Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil },
		},
		{
			Name: "alive", Category: "System", Description: "Health probe",
			Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil },
		},
		{
			Name: "hidden", Category: "Hidden", Description: "Userbot only",
			Surfaces: execution.SurfaceUserbot, Handler: func(*core.Context) error { return nil },
		},
	}); err != nil {
		t.Fatalf("RegisterBatch() error = %v", err)
	}
	client.SetCoreRouter(router)

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)
	help := callbackForAction(t, port.sent, assistantshell.ActionHelp)
	if err := dispatchShell(t, engine, help, 600, peer); err != nil {
		t.Fatalf("Dispatch(help) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "GoUltroid Help Menu") || !strings.Contains(port.edited.Text, "Media") {
		t.Fatalf("help root = %q", port.edited.Text)
	}
	if strings.Contains(port.edited.Text, "Hidden") {
		t.Fatalf("help root leaked userbot-only command: %q", port.edited.Text)
	}
	openModule := callbackForAction(t, port.edited, assistantshell.ActionHelpOpen)
	if err := dispatchShell(t, engine, openModule, 601, peer); err != nil {
		t.Fatalf("Dispatch(open module) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "/alpha") || !strings.Contains(port.edited.Text, "First media command") {
		t.Fatalf("module view = %q", port.edited.Text)
	}
	if err := dispatchShell(t, engine, openModule, 602, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old module token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}

	nextCommand := callbackForAction(t, port.edited, assistantshell.ActionHelpCmdNext)
	if err := dispatchShell(t, engine, nextCommand, 603, peer); err != nil {
		t.Fatalf("Dispatch(next command) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "/beta") {
		t.Fatalf("next command did not select beta: %q", port.edited.Text)
	}

	openCommand := callbackForAction(t, port.edited, assistantshell.ActionHelpCmdOpen)
	if err := dispatchShell(t, engine, openCommand, 604, peer); err != nil {
		t.Fatalf("Dispatch(command detail) error = %v", err)
	}
	for _, want := range []string{"/beta", "beta &lt;url&gt;", "/b", "Media", "Sudo", "1s", "5s"} {
		if !strings.Contains(port.edited.Text, want) {
			t.Fatalf("command detail missing %q: %q", want, port.edited.Text)
		}
	}

	back := callbackForAction(t, port.edited, assistantshell.ActionHelpBack)
	if err := dispatchShell(t, engine, back, 605, peer); err != nil {
		t.Fatalf("Dispatch(back to module) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "/beta") {
		t.Fatalf("module selection was not preserved: %q", port.edited.Text)
	}

	modules := callbackForAction(t, port.edited, assistantshell.ActionHelp)
	if err := dispatchShell(t, engine, modules, 606, peer); err != nil {
		t.Fatalf("Dispatch(back to modules) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Media") {
		t.Fatalf("module selection was not preserved at root: %q", port.edited.Text)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("help navigation sessions = %d, want 1", got)
	}
}
