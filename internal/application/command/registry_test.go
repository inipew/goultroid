package command_test

import (
	"testing"

	"github.com/inipew/goultroid/internal/application/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

func TestUnifiedRegistry(t *testing.T) {
	reg := command.NewUnifiedRegistry()

	cmdPing := core.Command{
		Name:        "ping",
		Aliases:     []string{"p"},
		Description: "Check ping",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		Handler:     func(c *core.Context) error { return nil },
	}

	cmdUserbotOnly := core.Command{
		Name:        "eval",
		Description: "Run eval",
		Surfaces:    execution.SurfaceUserbot,
		Handler:     func(c *core.Context) error { return nil },
	}

	if err := reg.RegisterBatch([]core.Command{cmdPing, cmdUserbotOnly}); err != nil {
		t.Fatalf("unexpected register batch error: %v", err)
	}

	// 1. Check Userbot surface
	if _, ok := reg.FindForSurface("ping", execution.SourceUserbot); !ok {
		t.Fatalf("expected ping available on userbot")
	}
	if _, ok := reg.FindForSurface("p", execution.SourceUserbot); !ok {
		t.Fatalf("expected alias p available on userbot")
	}
	if _, ok := reg.FindForSurface("eval", execution.SourceUserbot); !ok {
		t.Fatalf("expected eval available on userbot")
	}

	// 2. Check Assistant surface
	if _, ok := reg.FindForSurface("ping", execution.SourceAssistant); !ok {
		t.Fatalf("expected ping available on assistant")
	}
	if _, ok := reg.FindForSurface("eval", execution.SourceAssistant); ok {
		t.Fatalf("expected eval NOT available on assistant")
	}

	// 3. Check CommandsForSurface
	userbotCmds := reg.CommandsForSurface(execution.SourceUserbot)
	if len(userbotCmds) != 2 {
		t.Fatalf("expected 2 commands for userbot, got %d", len(userbotCmds))
	}

	assistantCmds := reg.CommandsForSurface(execution.SourceAssistant)
	if len(assistantCmds) != 1 || assistantCmds[0].Name != "ping" {
		t.Fatalf("expected only ping command for assistant, got %+v", assistantCmds)
	}
}
