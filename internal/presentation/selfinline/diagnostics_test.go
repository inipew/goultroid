package selfinline_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
	telegramrpc "github.com/inipew/goultroid/internal/telegram"
)

type diagnosticTransport struct {
	results   *tg.MessagesBotResults
	queryErr  error
	sendErr   error
	sendCalls int
}

func (t *diagnosticTransport) QueryInlineBot(context.Context, string, tg.InputPeerClass, string, string) (*tg.MessagesBotResults, error) {
	return t.results, t.queryErr
}

func (t *diagnosticTransport) SendInlineBotResult(context.Context, tg.InputPeerClass, int64, string, int64, int, int, bool, bool) error {
	t.sendCalls++
	return t.sendErr
}

func queryRequest() selfinline.Request {
	return selfinline.Request{Peer: &tg.InputPeerSelf{}, Query: "calc", ResultID: "calculator"}
}

func selectableResults() *tg.MessagesBotResults {
	return &tg.MessagesBotResults{
		QueryID: 44,
		Results: []tg.BotInlineResultClass{
			&tg.BotInlineResult{ID: "calculator", Type: "article"},
		},
	}
}

func TestRenderNormalizesQueryTelegramDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "inline disabled", err: tgerr.New(400, tg.ErrBotInlineDisabled), want: selfinline.ErrInlineDisabled},
		{name: "assistant timeout", err: tgerr.New(400, tg.ErrBotResponseTimeout), want: selfinline.ErrAssistantResponseTimeout},
		{name: "invalid assistant", err: tgerr.New(400, tg.ErrBotInvalid), want: selfinline.ErrAssistantInvalid},
		{name: "user bot invalid", err: tgerr.New(400, tg.ErrUserBotInvalid), want: selfinline.ErrAssistantInvalid},
		{name: "inline bot required", err: tgerr.New(400, tg.ErrInlineBotRequired), want: selfinline.ErrAssistantInvalid},
		{name: "inline forbidden", err: tgerr.New(400, tg.ErrChatSendInlineForbidden), want: selfinline.ErrPeerInlineRestricted},
		{name: "peer invalid after executor refresh", err: tgerr.New(400, tg.ErrPeerIDInvalid), want: selfinline.ErrPeerInlineRestricted},
		{name: "unknown", err: tgerr.New(500, "RPC_CALL_FAIL"), want: selfinline.ErrQueryFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &diagnosticTransport{queryErr: tc.err}
			bridge := selfinline.New(transport, func() string { return "assistant_bot" })
			_, got := bridge.Render(context.Background(), queryRequest())
			if !errors.Is(got, tc.want) {
				t.Fatalf("Render() error=%v, want %v", got, tc.want)
			}
			if got.Error() != tc.want.Error() {
				t.Fatalf("diagnostic=%q, want %q", got.Error(), tc.want.Error())
			}
			if transport.sendCalls != 0 {
				t.Fatalf("query failure unexpectedly reached send: %d", transport.sendCalls)
			}
			var rpcErr *tgerr.Error
			if !errors.As(got, &rpcErr) {
				t.Fatalf("original Telegram error is not preserved: %T", got)
			}
		})
	}
}

func TestRenderNormalizesSendTelegramDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "inline forbidden", err: tgerr.New(400, tg.ErrChatSendInlineForbidden), want: selfinline.ErrPeerInlineRestricted},
		{name: "write forbidden", err: tgerr.New(400, tg.ErrChatWriteForbidden), want: selfinline.ErrPeerInlineRestricted},
		{name: "channel private", err: tgerr.New(400, tg.ErrChannelPrivate), want: selfinline.ErrPeerInlineRestricted},
		{name: "banned in channel", err: tgerr.New(400, tg.ErrUserBannedInChannel), want: selfinline.ErrPeerInlineRestricted},
		{name: "chat restricted", err: tgerr.New(400, tg.ErrChatRestricted), want: selfinline.ErrPeerInlineRestricted},
		{name: "user restricted", err: tgerr.New(400, tg.ErrUserRestricted), want: selfinline.ErrPeerInlineRestricted},
		{name: "expired", err: tgerr.New(400, tg.ErrInlineResultExpired), want: selfinline.ErrInlineResultExpired},
		{name: "query invalid", err: tgerr.New(400, tg.ErrQueryIDInvalid), want: selfinline.ErrInlineResultExpired},
		{name: "result invalid", err: tgerr.New(400, tg.ErrResultIDInvalid), want: selfinline.ErrInlineResultExpired},
		{name: "unknown", err: tgerr.New(500, "RPC_CALL_FAIL"), want: selfinline.ErrSendFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &diagnosticTransport{results: selectableResults(), sendErr: tc.err}
			bridge := selfinline.New(transport, func() string { return "assistant_bot" })
			_, got := bridge.Render(context.Background(), queryRequest())
			if !errors.Is(got, tc.want) {
				t.Fatalf("Render() error=%v, want %v", got, tc.want)
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
	transport := &diagnosticTransport{results: selectableResults(), sendErr: cause}
	bridge := selfinline.New(transport, func() string { return "assistant_bot" })
	_, err := bridge.Render(context.Background(), queryRequest())
	if !errors.Is(err, selfinline.ErrSendFailed) {
		t.Fatalf("Render() error=%v, want %v", err, selfinline.ErrSendFailed)
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
