package selfinline

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

func TestCurrentTransportResolvesServiceAtCallTime(t *testing.T) {
	var current Transport
	transport := CurrentTransport(func() Transport { return current })
	if transport == nil {
		t.Fatal("CurrentTransport() returned nil")
	}
	if _, err := transport.QueryInlineBot(context.Background(), "assistant", &tg.InputPeerSelf{}, "calc", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("QueryInlineBot(before ready) error=%v, want %v", err, ErrUnavailable)
	}

	first := &fakeTransport{results: &tg.MessagesBotResults{
		QueryID: 1,
		Results: []tg.BotInlineResultClass{&tg.BotInlineResult{ID: "calculator", Type: "article"}},
	}}
	current = first
	if _, err := transport.QueryInlineBot(context.Background(), "assistant", &tg.InputPeerSelf{}, "calc", ""); err != nil {
		t.Fatalf("QueryInlineBot(first) error=%v", err)
	}
	if first.query != "calc" {
		t.Fatalf("first transport query=%q", first.query)
	}

	second := &fakeTransport{}
	current = second
	if err := transport.SendInlineBotResult(context.Background(), &tg.InputPeerSelf{}, 1, "calculator", 2, 0, 0, false, true); err != nil {
		t.Fatalf("SendInlineBotResult(second) error=%v", err)
	}
	if second.sendCalls != 1 || first.sendCalls != 0 {
		t.Fatalf("send calls first/second=%d/%d, want 0/1", first.sendCalls, second.sendCalls)
	}
}
