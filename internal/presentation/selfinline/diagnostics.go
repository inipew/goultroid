package selfinline

import (
	"errors"

	"github.com/gotd/td/tg"
)

var (
	ErrAssistantResponseTimeout = errors.New("Assistant did not answer the inline query in time. Try again.")
	ErrAssistantInvalid         = errors.New("Telegram cannot use the configured Assistant bot for inline mode. Check BOT_TOKEN and the Assistant username, then restart Goultroid.")
	ErrPeerInlineRestricted     = errors.New("Telegram cannot insert Assistant inline results in this chat. Check chat access and inline/send permissions, or try another chat.")
	ErrInlineResultExpired      = errors.New("The Assistant inline result expired before Telegram could send it. Run the command again.")
	ErrQueryFailed              = errors.New("Telegram could not query the Assistant inline bot. Try again.")
	ErrSendFailed               = errors.New("Telegram could not insert the Assistant inline result. Try again.")
)

// diagnosticError keeps the original transport/RPC error in the chain for
// logging and execution semantics while exposing only the stable user-facing
// diagnostic through Error().
type diagnosticError struct {
	kind  error
	cause error
}

func (e *diagnosticError) Error() string {
	if e == nil || e.kind == nil {
		return ErrUnavailable.Error()
	}
	return e.kind.Error()
}

func (e *diagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *diagnosticError) Is(target error) bool {
	return e != nil && e.kind != nil && target == e.kind
}

func diagnostic(kind, cause error) error {
	if cause == nil {
		return kind
	}
	return &diagnosticError{kind: kind, cause: cause}
}

func normalizeQueryError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case tg.IsBotInlineDisabled(err):
		return diagnostic(ErrInlineDisabled, err)
	case tg.IsBotResponseTimeout(err):
		return diagnostic(ErrAssistantResponseTimeout, err)
	case tg.IsBotInvalid(err), tg.IsUserBotInvalid(err), tg.IsInlineBotRequired(err):
		return diagnostic(ErrAssistantInvalid, err)
	case isPeerInlineRestricted(err):
		return diagnostic(ErrPeerInlineRestricted, err)
	default:
		return diagnostic(ErrQueryFailed, err)
	}
}

func normalizeSendError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case isPeerInlineRestricted(err):
		return diagnostic(ErrPeerInlineRestricted, err)
	case tg.IsInlineResultExpired(err), tg.IsQueryIDInvalid(err), tg.IsResultIDInvalid(err):
		return diagnostic(ErrInlineResultExpired, err)
	default:
		return diagnostic(ErrSendFailed, err)
	}
}

func isPeerInlineRestricted(err error) bool {
	return tg.IsChatSendInlineForbidden(err) ||
		tg.IsChatWriteForbidden(err) ||
		tg.IsChannelPrivate(err) ||
		tg.IsUserBannedInChannel(err) ||
		tg.IsChatRestricted(err) ||
		tg.IsUserRestricted(err) ||
		tg.IsPeerIDInvalid(err)
}
