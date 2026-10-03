package telegram

import (
	"fmt"

	"github.com/inipew/goultroid/internal/tasks"
)

const (
	messageHookDecisionOrderingDomain = "msg-decision"
	messageHookEventOrderingDomain    = "msg-event"
)

func messageHookDecisionOrderingKey(chatID int64) string {
	return fmt.Sprintf("%s:chat:%d", messageHookDecisionOrderingDomain, chatID)
}

func messageHookEventOrderingKey(scope tasks.ScopeIdentity, chatID int64) string {
	owner := scope.Owner
	if owner == "" {
		owner = "unscoped"
	}
	return fmt.Sprintf("%s:%s:chat:%d", messageHookEventOrderingDomain, owner, chatID)
}
