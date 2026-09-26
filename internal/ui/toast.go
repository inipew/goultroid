package ui

import (
	"errors"

	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
)

// UserErrorPresentation is the transport-neutral user-facing rendering of an
// operational error. Internal diagnostics stay in logs; presentation surfaces
// receive only bounded, intentional text plus whether Telegram should show it
// as an alert.
type UserErrorPresentation struct {
	Text  string
	Alert bool
}

// PresentUserError maps domain, interaction, and callback errors into one
// sanitized presentation contract shared by Assistant input and button flows.
func PresentUserError(err error) UserErrorPresentation {
	if err == nil {
		return UserErrorPresentation{}
	}

	switch {
	case errors.Is(err, core.ErrRateLimited):
		return UserErrorPresentation{Text: "⏳ Too many requests. Please try again later."}
	case errors.Is(err, rootinteraction.ErrBindingMismatch):
		return UserErrorPresentation{
			Text:  "This button can only be used by the user who opened it on the original message.",
			Alert: true,
		}
	case errors.Is(err, rootinteraction.ErrInputExpired):
		return UserErrorPresentation{Text: "⌛ Input session expired. Reopen the interaction and try again."}
	case errors.Is(err, rootinteraction.ErrExpired),
		errors.Is(err, rootinteraction.ErrNotFound),
		errors.Is(err, rootinteraction.ErrStaleToken),
		errors.Is(err, rootinteraction.ErrScopeStale):
		return UserErrorPresentation{Text: "⌛ Interaction expired. Please reopen it."}
	case errors.Is(err, rootinteraction.ErrRevisionConflict):
		return UserErrorPresentation{Text: "⚠️ Interaction changed while this action was pending. Please retry."}
	case errors.Is(err, rootinteraction.ErrInputBusy):
		return UserErrorPresentation{Text: "⚠️ Another input session is already active in this chat."}
	case errors.Is(err, rootinteraction.ErrCapacity):
		return UserErrorPresentation{Text: "⚠️ Too many active interactions. Close an older interaction and try again."}
	}

	message := core.UserMessage(err)
	if message != "" && message != "❌ An internal error occurred." {
		return UserErrorPresentation{Text: message}
	}
	return UserErrorPresentation{Text: "❌ Action failed. Please try again."}
}

// MapUserErrorMessage preserves the historical helper while routing all callers
// through the canonical presentation mapper.
func MapUserErrorMessage(err error) string {
	return PresentUserError(err).Text
}
