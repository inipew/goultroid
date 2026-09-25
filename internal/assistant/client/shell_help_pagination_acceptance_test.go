package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
)

func TestAssistantDirectHelpPaginationTransitionsAndStalesOldButtons(t *testing.T) {
	manager, client, port, engine := newShellEngine(t)
	defer manager.Shutdown()

	coreRouter := core.NewRouter(".")
	commands := []core.Command{{
		Name:        "help",
		Aliases:     []string{"h", "commands"},
		Description: "Show help",
		Category:    "Module00",
		Permission:  core.PermissionEveryone,
		Surfaces:    execution.SurfaceAssistant,
		Handler:     func(*core.Context) error { return nil },
	}}
	for module := 0; module < 10; module++ {
		commandCount := 1
		if module == 0 {
			commandCount = 10
		}
		for index := 0; index < commandCount; index++ {
			commands = append(commands, core.Command{
				Name:        fmt.Sprintf("m%02dc%02d", module, index),
				Description: "pagination acceptance command",
				Category:    fmt.Sprintf("Module%02d", module),
				Surfaces:    execution.SurfaceAssistant,
				Handler:     func(*core.Context) error { return nil },
			})
		}
	}
	if err := coreRouter.RegisterBatch(commands); err != nil {
		t.Fatal(err)
	}
	client.SetCoreRouter(coreRouter)

	peer := &tg.InputPeerUser{UserID: 7}
	if err := client.cmdRouter.DispatchMessageContext(
		context.Background(),
		7,
		peer,
		"/help",
		directAssistantMessageContext(),
		nil,
	); err != nil {
		t.Fatalf("DispatchMessageContext(/help) error=%v", err)
	}
	if !strings.Contains(port.sent.Text, "1 / 2") {
		t.Fatalf("Help root page text=%q, want page 1 / 2", port.sent.Text)
	}

	rootNext := callbackForAction(t, port.sent, assistantshell.ActionHelpNext)
	if err := dispatchShell(t, engine, rootNext, 730, peer); err != nil {
		t.Fatalf("Dispatch(help next) error=%v", err)
	}
	if !strings.Contains(port.edited.Text, "2 / 2") {
		t.Fatalf("Help root next page text=%q, want page 2 / 2", port.edited.Text)
	}
	if err := dispatchShell(t, engine, rootNext, 731, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("reused Help root next error=%v, want %v", err, rootinteraction.ErrStaleToken)
	}

	rootPrev := callbackForAction(t, port.edited, assistantshell.ActionHelpPrev)
	if err := dispatchShell(t, engine, rootPrev, 732, peer); err != nil {
		t.Fatalf("Dispatch(help prev) error=%v", err)
	}
	if !strings.Contains(port.edited.Text, "1 / 2") {
		t.Fatalf("Help root previous page text=%q, want page 1 / 2", port.edited.Text)
	}

	module := callbackForAction(t, port.edited, assistantshell.HelpModuleSlotActionIDs()[0])
	if err := dispatchShell(t, engine, module, 733, peer); err != nil {
		t.Fatalf("Dispatch(help module slot) error=%v", err)
	}
	if !strings.Contains(port.edited.Text, "Module00") || !strings.Contains(port.edited.Text, "1 / 2") {
		t.Fatalf("Help module page text=%q, want Module00 page 1 / 2", port.edited.Text)
	}

	commandNext := callbackForAction(t, port.edited, assistantshell.ActionHelpCmdNext)
	if err := dispatchShell(t, engine, commandNext, 734, peer); err != nil {
		t.Fatalf("Dispatch(help command next) error=%v", err)
	}
	if !strings.Contains(port.edited.Text, "2 / 2") {
		t.Fatalf("Help command next page text=%q, want page 2 / 2", port.edited.Text)
	}
	if err := dispatchShell(t, engine, commandNext, 735, peer); !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("reused Help command next error=%v, want %v", err, rootinteraction.ErrStaleToken)
	}

	commandPrev := callbackForAction(t, port.edited, assistantshell.ActionHelpCmdPrev)
	if err := dispatchShell(t, engine, commandPrev, 736, peer); err != nil {
		t.Fatalf("Dispatch(help command prev) error=%v", err)
	}
	if !strings.Contains(port.edited.Text, "1 / 2") {
		t.Fatalf("Help command previous page text=%q, want page 1 / 2", port.edited.Text)
	}
}
