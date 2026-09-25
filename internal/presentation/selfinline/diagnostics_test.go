package selfinline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/execution"
	telegramrpc "github.com/inipew/goultroid/internal/telegram"
)

func TestNormalizeQueryTelegramDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "inline disabled", err: tgerr.New(400, tg.ErrBotInlineDisabled), want: ErrInlineDisabled},
		{name: "assistant timeout", err: tgerr.New(400, tg.ErrBotResponseTimeout), want: ErrAssistantResponseTimeout},
		{name: "invalid assistant", err: tgerr.New(400, tg.ErrBotInvalid), want: ErrAssistantInvalid},
		{name: "user bot invalid", err: tgerr.New(400, tg.ErrUserBotInvalid), want: ErrAssistantInvalid},
		{name: "inline bot required", err: tgerr.New(400, tg.ErrInlineBotRequired), want: ErrAssistantInvalid},
		{name: "inline forbidden", err: tgerr.New(400, tg.ErrChatSendInlineForbidden), want: ErrPeerInlineRestricted},
		{name: "peer invalid after executor refresh", err: tgerr.New(400, tg.ErrPeerIDInvalid), want: ErrPeerInlineRestricted},
		{name: "unknown", err: tgerr.New(500, "RPC_CALL_FAIL"), want: ErrQueryFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeQueryError(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("normalizeQueryError()=%v, want %v", got, tc.want)
			}
			if got.Error() != tc.want.Error() {
				t.Fatalf("diagnostic=%q, want %q", got.Error(), tc.want.Error())
			}
			var rpcErr *tgerr.Error
			if !errors.As(got, &rpcErr) {
				t.Fatalf("original Telegram error is not preserved: %T", got)
			}
		})
	}
}

func TestNormalizeSendTelegramDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "inline forbidden", err: tgerr.New(400, tg.ErrChatSendInlineForbidden), want: ErrPeerInlineRestricted},
		{name: "write forbidden", err: tgerr.New(400, tg.ErrChatWriteForbidden), want: ErrPeerInlineRestricted},
		{name: "channel private", err: tgerr.New(400, tg.ErrChannelPrivate), want: ErrPeerInlineRestricted},
		{name: "banned in channel", err: tgerr.New(400, tg.ErrUserBannedInChannel), want: ErrPeerInlineRestricted},
		{name: "chat restricted", err: tgerr.New(400, tg.ErrChatRestricted), want: ErrPeerInlineRestricted},
		{name: "user restricted", err: tgerr.New(400, tg.ErrUserRestricted), want: ErrPeerInlineRestricted},
		{name: "expired", err: tgerr.New(400, tg.ErrInlineResultExpired), want: ErrInlineResultExpired},
		{name: "query invalid", err: tgerr.New(400, tg.ErrQueryIDInvalid), want: ErrInlineResultExpired},
		{name: "result invalid", err: tgerr.New(400, tg.ErrResultIDInvalid), want: ErrInlineResultExpired},
		{name: "unknown", err: tgerr.New(500, "RPC_CALL_FAIL"), want: ErrSendFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeSendError(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("normalizeSendError()=%v, want %v", got, tc.want)
			}
			if got.Error() != tc.want.Error() {
				t.Fatalf("diagnostic=%q, want %q", got.Error(), tc.want.Error())
			}
		})
	}
}

func TestRenderHidesRawRPCFailureButPreservesExecutionSemantics(t *testing.T) {
	cause := &telegramrpc.RPCFailure{
		Method:    "messages.sendInlineBotResult",
		Class:     telegramrpc.RPCTransient,
		Attempts:  3,
		Ambiguous: true,
		Err:       tgerr.New(500, "RPC_CALL_FAIL"),
	}
	transport := &fakeTransport{
		results: &tg.MessagesBotResults{
			QueryID: 44,
			Results: []tg.BotInlineResultClass{
				&tg.BotInlineResult{ID: "calculator", Type: "article"},
			},
		},
		sendErr: cause,
	}
	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), Request{
		Peer: &tg.InputPeerSelf{}, Query: "calc", ResultID: "calculator",
	})
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("Render() error=%v, want %v", err, ErrSendFailed)
	}
	if strings.Contains(err.Error(), "rpc ") || strings.Contains(err.Error(), "RPC_CALL_FAIL") {
		t.Fatalf("raw RPC failure leaked into diagnostic: %q", err.Error())
	}
	var failure *telegramrpc.RPCFailure
	if !errors.As(err, &failure) || failure != cause {
		t.Fatalf("RPCFailure cause was not preserved: %#v", failure)
	}
	semantics, ok := execution.ExplicitSemantics(err)
	if !ok || semantics.Disposition != execution.DispositionPermanent || semantics.Code != "rpc_ambiguous" {
		t.Fatalf("execution semantics changed by diagnostic wrapper: %+v ok=%v", semantics, ok)
	}
}

func TestRenderNormalizesExpiredInlineResult(t *testing.T) {
	transport := &fakeTransport{
		results: &tg.MessagesBotResults{
			QueryID: 45,
			Results: []tg.BotInlineResultClass{
				&tg.BotInlineResult{ID: "downloader", Type: "article"},
			},
		},
		sendErr: tgerr.New(400, tg.ErrInlineResultExpired),
	}
	bridge := New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), Request{
		Peer: &tg.InputPeerSelf{}, Query: "dl https://example.com/file", ResultID: "downloader",
	})
	if !errors.Is(err, ErrInlineResultExpired) {
		t.Fatalf("Render() error=%v, want %v", err, ErrInlineResultExpired)
	}
	if err.Error() != ErrInlineResultExpired.Error() {
		t.Fatalf("expired result diagnostic=%q", err.Error())
	}
}
