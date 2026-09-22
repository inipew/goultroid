package app

import (
	"context"
	"fmt"
	"time"
)

// IssueSavedResponseDeepLink creates an opaque /start payload for one exact
// SavedResponse SurfaceDeepLink binding. actorID=0 creates a public token.
func (a *App) IssueSavedResponseDeepLink(
	ctx context.Context,
	alias string,
	actorID int64,
	ttl time.Duration,
	singleUse bool,
) (string, error) {
	if a == nil || a.deepLinks == nil || a.savedResponseDeepLinks == nil {
		return "", fmt.Errorf("saved-response deep-link runtime is unavailable")
	}
	token, err := a.savedResponseDeepLinks.Issue(
		ctx,
		a.deepLinks,
		alias,
		actorID,
		ttl,
		singleUse,
	)
	if err != nil {
		return "", err
	}
	return token.ID, nil
}
