package client

import (
	"context"
	"errors"
	"testing"
	"time"

	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/gotd/td/tg"
)

type familyCaptureExecutor struct {
	method string
	family string
}

func (e *familyCaptureExecutor) Do(
	_ context.Context,
	method, family string,
	_ assistentrpc.Kind,
	_ time.Duration,
	_ func(context.Context) error,
) error {
	e.method = method
	e.family = family
	return errors.New("stop before raw RPC")
}

func TestManagedAPICallbackAnswerUsesDedicatedLimiterFamily(t *testing.T) {
	executor := &familyCaptureExecutor{}
	api := &managedAPI{executor: executor}

	_, err := api.MessagesSetBotCallbackAnswer(context.Background(), &tg.MessagesSetBotCallbackAnswerRequest{QueryID: 1})
	if err == nil {
		t.Fatal("capture executor unexpectedly returned nil")
	}
	if executor.method != "messages.setBotCallbackAnswer" {
		t.Fatalf("method=%q", executor.method)
	}
	if executor.family != "callback" {
		t.Fatalf("family=%q, want callback", executor.family)
	}
}
