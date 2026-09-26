package myxl

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestP6MyXLUserbotMenuDoesNotRequireAssistant(t *testing.T) {
	p := New(nil, nil)
	commands := p.Commands()
	var command *core.Command
	for i := range commands {
		if commands[i].Name == "myxl" {
			command = &commands[i]
			break
		}
	}
	if command == nil {
		t.Fatal("myxl command is not registered")
	}

	svc := &mockTgService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionInteractive,
		Sender:  &core.User{ID: 1001},
		Chat:    &core.Chat{ID: 1001, Type: "private"},
		Message: &core.Message{ID: 1, IsOutgoing: true},
		Svc:     svc,
		PeerID:  &tg.InputPeerUser{UserID: 1001},
	}
	if err := command.Handler(ctx); err != nil {
		t.Fatalf("native .myxl menu error=%v", err)
	}
	if !strings.Contains(svc.sent, "MyXL Plugin Menu") || !strings.Contains(svc.sent, ".myxl login") {
		t.Fatalf("native .myxl menu=%q", svc.sent)
	}
	if svc.lastMarkup != nil {
		t.Fatalf("native .myxl unexpectedly required Assistant markup: %T", svc.lastMarkup)
	}
}
