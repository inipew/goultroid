package selfinline

import (
	"context"

	"github.com/gotd/td/tg"
)

// TransportProvider resolves the currently active self-inline transport.
// It is evaluated for every physical query/send so application composition can
// install the renderer before Telegram connects without retaining a stale
// transport across reconnects.
type TransportProvider func() Transport

type currentTransport struct {
	provider TransportProvider
}

// CurrentTransport returns a stateless Transport that delegates every operation
// to the transport returned by provider at call time.
func CurrentTransport(provider TransportProvider) Transport {
	if provider == nil {
		return nil
	}
	return currentTransport{provider: provider}
}

func (t currentTransport) resolve() (Transport, error) {
	if t.provider == nil {
		return nil, ErrUnavailable
	}
	transport := t.provider()
	if transport == nil {
		return nil, ErrUnavailable
	}
	return transport, nil
}

func (t currentTransport) QueryInlineBot(
	ctx context.Context,
	botUsername string,
	peer tg.InputPeerClass,
	query string,
	offset string,
) (*tg.MessagesBotResults, error) {
	transport, err := t.resolve()
	if err != nil {
		return nil, err
	}
	return transport.QueryInlineBot(ctx, botUsername, peer, query, offset)
}

func (t currentTransport) SendInlineBotResult(
	ctx context.Context,
	peer tg.InputPeerClass,
	queryID int64,
	resultID string,
	randomID int64,
	replyToID int,
	topicID int,
	silent bool,
	hideVia bool,
) error {
	transport, err := t.resolve()
	if err != nil {
		return err
	}
	return transport.SendInlineBotResult(ctx, peer, queryID, resultID, randomID, replyToID, topicID, silent, hideVia)
}

var _ Transport = currentTransport{}
