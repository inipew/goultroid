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

func TestAssistantShellDetailedHelpParityUsesDirectTypedSlots(t *testing.T) {
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
	if !strings.Contains(port.edited.Text, "GoUltroid Help Menu") || strings.Contains(port.edited.Text, "Hidden") {
		t.Fatalf("help root = %q", port.edited.Text)
	}

	moduleSlot := callbackForAction(t, port.edited, assistantshell.HelpModuleSlotActionIDs()[0])
	if err := dispatchShell(t, engine, moduleSlot, 601, peer); err != nil {
		t.Fatalf("Dispatch(module slot) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Media") {
		t.Fatalf("module view = %q", port.edited.Text)
	}
	if err := dispatchShell(t, engine, moduleSlot, 602, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("old module token error = %v, want %v", err, rootinteraction.ErrStaleToken)
	}

	commandSlot := callbackForAction(t, port.edited, assistantshell.HelpCommandSlotActionIDs()[1])
	if err := dispatchShell(t, engine, commandSlot, 603, peer); err != nil {
		t.Fatalf("Dispatch(command slot) error = %v", err)
	}
	for _, want := range []string{"/beta", "beta &lt;url&gt;", "/b", "Media", "Sudo", "1s", "5s"} {
		if !strings.Contains(port.edited.Text, want) {
			t.Fatalf("command detail missing %q: %q", want, port.edited.Text)
		}
	}

	back := callbackForAction(t, port.edited, assistantshell.ActionHelpBack)
	if err := dispatchShell(t, engine, back, 604, peer); err != nil {
		t.Fatalf("Dispatch(back to module) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "Media") {
		t.Fatalf("module view was not restored: %q", port.edited.Text)
	}

	modules := callbackForAction(t, port.edited, assistantshell.ActionHelp)
	if err := dispatchShell(t, engine, modules, 605, peer); err != nil {
		t.Fatalf("Dispatch(back to modules) error = %v", err)
	}
	if !strings.Contains(port.edited.Text, "GoUltroid Help Menu") {
		t.Fatalf("help root was not restored: %q", port.edited.Text)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 1 {
		t.Fatalf("help navigation sessions = %d, want 1", got)
	}
}

func TestAssistantShellHelpSlotRejectsCatalogRemap(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	first := core.NewRouter(".")
	if err := first.RegisterBatch([]core.Command{
		{Name: "alpha", Category: "Media", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
		{Name: "alive", Category: "System", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
	}); err != nil {
		t.Fatalf("RegisterBatch(first) error = %v", err)
	}
	client.SetCoreRouter(first)

	peer := &tg.InputPeerUser{UserID: 7}
	beginShell(t, engine, port, peer)
	help := callbackForAction(t, port.sent, assistantshell.ActionHelp)
	if err := dispatchShell(t, engine, help, 700, peer); err != nil {
		t.Fatalf("Dispatch(help) error = %v", err)
	}
	oldSlot := callbackForAction(t, port.edited, assistantshell.HelpModuleSlotActionIDs()[0])

	second := core.NewRouter(".")
	if err := second.RegisterBatch([]core.Command{
		{Name: "aardvark", Category: "Aardvark", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
		{Name: "alpha", Category: "Media", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
		{Name: "alive", Category: "System", Surfaces: execution.SurfaceAssistant, Handler: func(*core.Context) error { return nil }},
	}); err != nil {
		t.Fatalf("RegisterBatch(second) error = %v", err)
	}
	client.SetCoreRouter(second)

	if err := dispatchShell(t, engine, oldSlot, 701, peer); !errors.Is(err, ErrShellHelpSelectionStale) {
		t.Fatalf("catalog-remapped slot error = %v, want %v", err, ErrShellHelpSelectionStale)
	}
	if !strings.Contains(port.edited.Text, "GoUltroid Help Menu") {
		t.Fatalf("stale slot unexpectedly transitioned view: %q", port.edited.Text)
	}
}
