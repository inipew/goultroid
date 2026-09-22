package app

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/assistant/savedresponsecallback"
	"github.com/inipew/goultroid/internal/presentation"
)

// OpenSavedResponseCallback renders one a2 button bound to an exact persistent
// SurfaceCallback binding. The returned message/session is owned by the
// Assistant interaction runtime; no SavedResponse identity is embedded in
// Telegram callback_data.
func (a *App) OpenSavedResponseCallback(
	ctx context.Context,
	alias string,
	actorID int64,
	target presentation.Target,
	text string,
	buttonText string,
	ttl time.Duration,
) error {
	if a == nil || a.savedResponseCallbacks == nil {
		return fmt.Errorf("saved-response callback runtime is unavailable")
	}
	_, err := a.savedResponseCallbacks.Begin(ctx, savedresponsecallback.BeginRequest{
		Alias:      alias,
		ActorID:    actorID,
		Target:     target,
		Text:       text,
		ButtonText: buttonText,
		TTL:        ttl,
	})
	return err
}
