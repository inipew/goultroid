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

// RenderStage identifies the furthest self-inline phase reached before Render
// returned an error. The zero value is reserved for errors that did not come
// from the stage-aware renderer contract.
type RenderStage uint8

const (
	RenderStageUnknown RenderStage = iota
	RenderStagePreflight
	RenderStageQuery
	RenderStageSelect
	RenderStageSend
)

// RenderFailure attaches delivery-safety metadata to a Render error while
// preserving the original diagnostic and transport/RPC error chain.
type RenderFailure struct {
	Stage            RenderStage
	MayHaveCommitted bool
	Err              error
}

func (e *RenderFailure) Error() string {
	if e == nil || e.Err == nil {
		return ErrUnavailable.Error()
	}
	return e.Err.Error()
}

func (e *RenderFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// FailureStage returns the self-inline stage attached to err. Unknown means the
// error does not carry the stage-aware renderer contract and must not be
// treated as safe for automatic fallback.
func FailureStage(err error) RenderStage {
	var failure *RenderFailure
	if !errors.As(err, &failure) || failure == nil {
		return RenderStageUnknown
	}
	return failure.Stage
}

// FallbackSafe reports whether native presentation may be emitted without
// risking a duplicate self-inline delivery. Unknown and send-stage failures are
// intentionally fail-closed.
func FallbackSafe(err error) bool {
	var failure *RenderFailure
	if !errors.As(err, &failure) || failure == nil || failure.MayHaveCommitted {
		return false
	}
	switch failure.Stage {
	case RenderStagePreflight, RenderStageQuery, RenderStageSelect:
		return true
	default:
		return false
	}
}

func renderFailure(stage RenderStage, mayHaveCommitted bool, err error) error {
	if err == nil {
		return nil
	}
	return &RenderFailure{
		Stage:            stage,
		MayHaveCommitted: mayHaveCommitted,
		Err:              err,
	}
}

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
