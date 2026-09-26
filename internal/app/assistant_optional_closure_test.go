package app

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	assistantclient "github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/plugins/calculator"
)

func TestP6CalculatorFallsBackAfterAssistantStops(t *testing.T) {
	provider := &p0SelfInlineServiceProvider{}
	identity := &p0AssistantIdentity{username: "assistant_bot"}
	renderer := newSelfInlineRenderer(provider, identity)
	if renderer == nil {
		t.Fatal("newSelfInlineRenderer() returned nil")
	}

	feature := calculator.New()
	feature.SetSelfInlineRenderer(renderer)
	live := &p0SelfInlineService{}
	provider.service = live

	commands := feature.Commands()
	if len(commands) != 1 || commands[0].Name != "calc" {
		t.Fatalf("calculator commands=%+v", commands)
	}

	rich := &core.Context{
		Ctx:     context.Background(),
		RawArgs: "1 + 2",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 10, IsOutgoing: true},
		Svc:     live,
	}
	if err := commands[0].Handler(rich); err != nil {
		t.Fatalf(".calc while Assistant ready error=%v", err)
	}
	if live.queryCalls != 1 || live.sendCalls != 1 {
		t.Fatalf("ready Assistant query/send=%d/%d, want 1/1", live.queryCalls, live.sendCalls)
	}

	// Keep the old username intentionally. Readiness, rather than stale pointer
	// clearing, must fence self-inline after Assistant shutdown/restart begins.
	identity.err = assistantclient.ErrNotReady
	live.edited = ""
	fallback := &core.Context{
		Ctx:     context.Background(),
		RawArgs: "1 + 2 * 3",
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &core.Message{ID: 11, IsOutgoing: true},
		Svc:     live,
	}
	if err := commands[0].Handler(fallback); err != nil {
		t.Fatalf(".calc after Assistant stopped error=%v", err)
	}
	if live.queryCalls != 1 || live.sendCalls != 1 {
		t.Fatalf("stopped Assistant reached self-inline transport: query/send=%d/%d, want 1/1", live.queryCalls, live.sendCalls)
	}
	if !strings.Contains(live.edited, "<code>1+2*3</code>") || !strings.Contains(live.edited, "<b>7</b>") {
		t.Fatalf("native calculator fallback after Assistant stop=%q", live.edited)
	}
}
