package ui

import (
	"github.com/inipew/goultroid/internal/services/callback"
)

// AnswerToast responds to an interactive callback query with a banner or pop-up notification.
func AnswerToast(ctx *callback.CallbackContext, text string, alert bool) error {
	if ctx == nil || ctx.IsAnswered() {
		return nil
	}
	return ctx.Answer(text, alert)
}

// AnswerSuccessToast displays a brief success toast to the user.
func AnswerSuccessToast(ctx *callback.CallbackContext, text string) error {
	if text == "" {
		text = "✅ Success"
	}
	return AnswerToast(ctx, text, false)
}

// AnswerErrorToast displays an error alert modal or toast to the user.
func AnswerErrorToast(ctx *callback.CallbackContext, text string) error {
	if text == "" {
		text = "❌ Action failed"
	}
	return AnswerToast(ctx, text, true)
}
