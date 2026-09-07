package ui

import (
	"errors"

	"github.com/inipew/goultroid/internal/core"
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

// MapUserErrorMessage maps domain and infrastructure errors into user-friendly UI alert messages.
func MapUserErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, core.ErrRateLimited) {
		return "⏳ Too many requests, slow down."
	}
	if errors.Is(err, callback.ErrUnauthorized) {
		return "⚠️ You are not authorized to perform this action."
	}
	if errors.Is(err, callback.ErrStateExpired) {
		return "⏰ Button expired, run the command again."
	}
	if errors.Is(err, callback.ErrStateNotFound) {
		return "Button already used or state expired."
	}
	if errors.Is(err, callback.ErrInvalidCallbackData) {
		return "Invalid button action or payload."
	}
	if errors.Is(err, callback.ErrHandlerNotFound) {
		return "Feature not available."
	}
	// Sanitize: only expose error text if it's user-safe (no internal details like sqlite)
	if core.IsUserSafeText(err.Error()) {
		return "❌ " + err.Error()
	}
	return "❌ Action failed. Please try again."
}

