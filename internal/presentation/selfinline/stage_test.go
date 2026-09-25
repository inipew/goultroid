package selfinline

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type stageTransport struct {
	results   *tg.MessagesBotResults
	queryErr  error
	queryHook func()
	sendErr   error
	sendCalls int
}

func (t *stageTransport) QueryInlineBot(context.Context, string, tg.InputPeerClass, string, string) (*tg.MessagesBotResults, error) {
	if t.queryHook != nil {
		t.queryHook()
	}
	return t.results, t.queryErr
}

func (t *stageTransport) SendInlineBotResult(context.Context, tg.InputPeerClass, int64, string, int64, int, int, bool, bool) error {
	t.sendCalls++
	return t.sendErr
}

func stageResults() *tg.MessagesBotResults {
	return &tg.MessagesBotResults{
		QueryID: 44,
		Results: []tg.BotInlineResultClass{
			&tg.BotInlineResult{ID: "calculator", Type: "article"},
		},
	}
}

func stageRequest() Request {
	return Request{Peer: &tg.InputPeerSelf{}, Query: "calc", ResultID: "calculator"}
}

func TestRenderFailureStagePreflightIsFallbackSafe(t *testing.T) {
	transport := &stageTransport{}
	bridge := NewWithIdentity(transport, func() (string, error) {
		return "", ErrInlineDisabled
	})
	_, err := bridge.Render(context.Background(), stageRequest())
	if !errors.Is(err, ErrInlineDisabled) {
		t.Fatalf("Render() error=%v, want %v", err, ErrInlineDisabled)
	}
	if FailureStage(err) != RenderStagePreflight || !FallbackSafe(err) {
		t.Fatalf("stage=%v fallbackSafe=%v", FailureStage(err), FallbackSafe(err))
	}
	if transport.sendCalls != 0 {
		t.Fatalf("preflight failure unexpectedly sent result, calls=%d", transport.sendCalls)
	}
}

func TestRenderFailureStageQueryIsFallbackSafe(t *testing.T) {
	transport := &stageTransport{queryErr: tgerr.New(400, tg.ErrBotInlineDisabled)}
	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), stageRequest())
	if !errors.Is(err, ErrInlineDisabled) {
		t.Fatalf("Render() error=%v, want %v", err, ErrInlineDisabled)
	}
	if FailureStage(err) != RenderStageQuery || !FallbackSafe(err) {
		t.Fatalf("stage=%v fallbackSafe=%v", FailureStage(err), FallbackSafe(err))
	}
	if transport.sendCalls != 0 {
		t.Fatalf("query failure unexpectedly sent result, calls=%d", transport.sendCalls)
	}
}

func TestRenderFailureStageSelectIsFallbackSafe(t *testing.T) {
	transport := &stageTransport{results: &tg.MessagesBotResults{QueryID: 44}}
	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), stageRequest())
	if !errors.Is(err, ErrNoResults) {
		t.Fatalf("Render() error=%v, want %v", err, ErrNoResults)
	}
	if FailureStage(err) != RenderStageSelect || !FallbackSafe(err) {
		t.Fatalf("stage=%v fallbackSafe=%v", FailureStage(err), FallbackSafe(err))
	}
	if transport.sendCalls != 0 {
		t.Fatalf("select failure unexpectedly sent result, calls=%d", transport.sendCalls)
	}
}

func TestRenderFailureStageSendIsNotFallbackSafe(t *testing.T) {
	transport := &stageTransport{
		results: stageResults(),
		sendErr: tgerr.New(500, "RPC_CALL_FAIL"),
	}
	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), stageRequest())
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("Render() error=%v, want %v", err, ErrSendFailed)
	}
	if FailureStage(err) != RenderStageSend || FallbackSafe(err) {
		t.Fatalf("stage=%v fallbackSafe=%v", FailureStage(err), FallbackSafe(err))
	}
	var failure *RenderFailure
	if !errors.As(err, &failure) || !failure.MayHaveCommitted {
		t.Fatalf("failure=%#v, want MayHaveCommitted", failure)
	}
	if transport.sendCalls != 1 {
		t.Fatalf("send calls=%d, want 1", transport.sendCalls)
	}
}

func TestCurrentTransportLossBeforeSendIsMarkedSendStage(t *testing.T) {
	var current Transport
	transport := CurrentTransport(func() Transport { return current })
	underlying := &stageTransport{results: stageResults()}
	underlying.queryHook = func() { current = nil }
	current = underlying

	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), stageRequest())
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("Render() error=%v, want %v", err, ErrSendFailed)
	}
	if FailureStage(err) != RenderStageSend || FallbackSafe(err) {
		t.Fatalf("stage=%v fallbackSafe=%v", FailureStage(err), FallbackSafe(err))
	}
	if underlying.sendCalls != 0 {
		t.Fatalf("stale transport unexpectedly sent result, calls=%d", underlying.sendCalls)
	}
}

func TestAuthorizedFailureIsPreflightAndFallbackSafe(t *testing.T) {
	denied := errors.New("denied")
	calls := 0
	renderer := Authorized(stageRendererFunc(func(context.Context, Request) (Result, error) {
		calls++
		return Result{}, nil
	}), func() error { return denied })

	_, err := renderer.Render(context.Background(), stageRequest())
	if !errors.Is(err, denied) {
		t.Fatalf("Render() error=%v, want %v", err, denied)
	}
	if FailureStage(err) != RenderStagePreflight || !FallbackSafe(err) {
		t.Fatalf("stage=%v fallbackSafe=%v", FailureStage(err), FallbackSafe(err))
	}
	if calls != 0 {
		t.Fatalf("delegate calls=%d, want 0", calls)
	}
}

func TestUnknownRenderFailureIsNotFallbackSafe(t *testing.T) {
	err := errors.New("not stage aware")
	if FailureStage(err) != RenderStageUnknown {
		t.Fatalf("FailureStage()=%v, want %v", FailureStage(err), RenderStageUnknown)
	}
	if FallbackSafe(err) {
		t.Fatal("unknown error must fail closed for automatic fallback")
	}
}

type stageRendererFunc func(context.Context, Request) (Result, error)

func (f stageRendererFunc) Render(ctx context.Context, request Request) (Result, error) {
	return f(ctx, request)
}
