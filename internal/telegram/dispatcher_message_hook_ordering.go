package telegram

import (
	"fmt"

	"github.com/inipew/goultroid/internal/tasks"
)

// Decision tasks retain ordering per chat, but never share keys with the
// asynchronous feature/observability tasks dispatched for that chat.
func messageHookDecisionOrderingKey(chatID int64) string {
	return fmt.Sprintf("msg-decision:chat:%d", chatID)
}

// Event tasks are serialized only within the same plugin and chat.
// The plugin owner is deliberately stable across lifecycle generations;
// TaskEngine scope cancellation fences tasks belonging to a stale generation.
func messageHookEventOrderingKey(scope tasks.ScopeIdentity, chatID int64) string {
	owner := scope.Owner
	if owner == "" {
		owner = "unscoped"
	}
	return fmt.Sprintf("msg-event:%s:chat:%d", owner, chatID)
}
