package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestP8GDispatchRequiresAuthoritativeMessageContext(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	err := r.DispatchMessageContext(
		context.Background(),
		7,
		&tg.InputPeerUser{UserID: 7},
		"/start",
		command.MessageContext{},
		&fakeInteraction{},
	)
	if !errors.Is(err, core.ErrInvalidArguments) {
		t.Fatalf("DispatchMessageContext() error=%v, want %v", err, core.ErrInvalidArguments)
	}
}
