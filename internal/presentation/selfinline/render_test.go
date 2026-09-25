package selfinline

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
)

type fakeTransport struct {
	queryBot      string
	queryPeer     tg.InputPeerClass
	query         string
	offset        string
	results       *tg.MessagesBotResults
	queryErr      error
	sendCalls     int
	sentPeer      tg.InputPeerClass
	sentQueryID   int64
	sentResultID  string
	sentRandomID  int64
	sentReplyToID int
	sentTopicID   int
	sentSilent    bool
	sentHideVia   bool
	sendErr       error
}

func (f *fakeTransport) QueryInlineBot(_ context.Context, bot string, peer tg.InputPeerClass, query, offset string) (*tg.MessagesBotResults, error) {
	f.queryBot = bot
	f.queryPeer = peer
	f.query = query
	f.offset = offset
	return f.results, f.queryErr
}

func (f *fakeTransport) SendInlineBotResult(_ context.Context, peer tg.InputPeerClass, queryID int64, resultID string, randomID int64, replyToID, topicID int, silent, hideVia bool) error {
	f.sendCalls++
	f.sentPeer = peer
	f.sentQueryID = queryID
	f.sentResultID = resultID
	f.sentRandomID = randomID
	f.sentReplyToID = replyToID
	f.sentTopicID = topicID
	f.sentSilent = silent
	f.sentHideVia = hideVia
	return f.sendErr
}

func TestRenderQueriesCurrentAssistantAndSendsSelectedResult(t *testing.T) {
	peer := &tg.InputPeerChat{ChatID: 77}
	transport := &fakeTransport{results: &tg.MessagesBotResults{
		QueryID: 900,
		Results: []tg.BotInlineResultClass{
			&tg.BotInlineResult{ID: "first", Type: "article"},
			&tg.BotInlineResult{ID: "second", Type: "article"},
		},
	}}
	bridge := New(transport, func() string { return "@assistant_bot" })

	got, err := bridge.Render(context.Background(), Request{
		Peer: peer, Query: " calc ", ResultIndex: 1, ReplyToID: 12, TopicID: 10,
		Silent: true, HideVia: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.queryBot != "assistant_bot" || transport.query != "calc" || transport.queryPeer != peer {
		t.Fatalf("query mismatch bot=%q query=%q peer=%T", transport.queryBot, transport.query, transport.queryPeer)
	}
	if transport.sendCalls != 1 || transport.sentQueryID != 900 || transport.sentResultID != "second" {
		t.Fatalf("send mismatch calls=%d query=%d result=%q", transport.sendCalls, transport.sentQueryID, transport.sentResultID)
	}
	if transport.sentRandomID == 0 || got.RandomID != transport.sentRandomID {
		t.Fatalf("random id=%d result=%+v", transport.sentRandomID, got)
	}
	if transport.sentReplyToID != 12 || transport.sentTopicID != 10 || !transport.sentSilent || !transport.sentHideVia {
		t.Fatalf("send options mismatch reply=%d topic=%d silent=%v hide=%v", transport.sentReplyToID, transport.sentTopicID, transport.sentSilent, transport.sentHideVia)
	}
}

func TestRenderCanSelectStableResultID(t *testing.T) {
	transport := &fakeTransport{results: &tg.MessagesBotResults{
		QueryID: 901,
		Results: []tg.BotInlineResultClass{
			&tg.BotInlineResult{ID: "a", Type: "article"},
			&tg.BotInlineResult{ID: "stable", Type: "article"},
		},
	}}
	bridge := New(transport, func() string { return "assistant" })
	_, err := bridge.Render(context.Background(), Request{
		Peer: &tg.InputPeerSelf{}, Query: "demo", ResultID: "stable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.sentResultID != "stable" {
		t.Fatalf("selected result=%q", transport.sentResultID)
	}
}

func TestRenderFailsClosedBeforeTelegramSend(t *testing.T) {
	tests := []struct {
		name    string
		bridge  *RenderBridge
		request Request
		want    error
	}{
		{name: "missing bridge", bridge: nil, request: Request{}, want: ErrUnavailable},
		{name: "missing assistant", bridge: New(&fakeTransport{}, func() string { return "" }), request: Request{Peer: &tg.InputPeerSelf{}, Query: "x"}, want: ErrUnavailable},
		{name: "missing peer", bridge: New(&fakeTransport{}, func() string { return "bot" }), request: Request{Query: "x"}, want: core.ErrInvalidArgs},
		{name: "empty query", bridge: New(&fakeTransport{}, func() string { return "bot" }), request: Request{Peer: &tg.InputPeerSelf{}}, want: core.ErrInvalidArgs},
		{name: "bad index", bridge: New(&fakeTransport{}, func() string { return "bot" }), request: Request{Peer: &tg.InputPeerSelf{}, Query: "x", ResultIndex: 50}, want: core.ErrInvalidArgs},
		{name: "no results", bridge: New(&fakeTransport{results: &tg.MessagesBotResults{QueryID: 4}}, func() string { return "bot" }), request: Request{Peer: &tg.InputPeerSelf{}, Query: "x"}, want: ErrNoResults},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.bridge.Render(context.Background(), tc.request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want %v", err, tc.want)
			}
		})
	}
}

func TestRenderUsesTopicRootAsReplyWhenNoExplicitReply(t *testing.T) {
	transport := &fakeTransport{results: &tg.MessagesBotResults{
		QueryID: 902,
		Results: []tg.BotInlineResultClass{&tg.BotInlineResult{ID: "one", Type: "article"}},
	}}
	bridge := New(transport, func() string { return "assistant" })
	_, err := bridge.Render(context.Background(), Request{
		Peer: &tg.InputPeerChannel{ChannelID: 10, AccessHash: 20}, Query: "topic", TopicID: 88,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.sentReplyToID != 88 || transport.sentTopicID != 88 {
		t.Fatalf("topic fallback reply/topic=%d/%d", transport.sentReplyToID, transport.sentTopicID)
	}
}

func TestRenderCapabilityPreflightStopsBeforeTelegramQuery(t *testing.T) {
	transport := &fakeTransport{}
	bridge := NewWithIdentity(transport, func() (string, error) {
		return "", ErrInlineDisabled
	})
	_, err := bridge.Render(context.Background(), Request{
		Peer: &tg.InputPeerSelf{}, Query: "calc",
	})
	if !errors.Is(err, ErrInlineDisabled) {
		t.Fatalf("Render() error=%v, want %v", err, ErrInlineDisabled)
	}
	if transport.queryBot != "" || transport.sendCalls != 0 {
		t.Fatalf("disabled capability reached Telegram: queryBot=%q sendCalls=%d", transport.queryBot, transport.sendCalls)
	}
}

func TestRenderMapsTelegramBotInlineDisabledToCapabilityError(t *testing.T) {
	transport := &fakeTransport{queryErr: tgerr.New(400, tg.ErrBotInlineDisabled)}
	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), Request{
		Peer: &tg.InputPeerSelf{}, Query: "calc",
	})
	if !errors.Is(err, ErrInlineDisabled) {
		t.Fatalf("Render(BOT_INLINE_DISABLED) error=%v, want %v", err, ErrInlineDisabled)
	}
	if transport.sendCalls != 0 {
		t.Fatalf("BOT_INLINE_DISABLED unexpectedly sent result, calls=%d", transport.sendCalls)
	}
}
