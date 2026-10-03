package telegram

import (
	"fmt"

	"github.com/inipew/goultroid/internal/core"
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

func messageHookOrderingOwner(scope tasks.ScopeIdentity) string {
	owner := scope.Owner
	if owner == "" {
		return "unscoped"
	}
	return owner
}

func messageHookOrderingKey(registered prioritizedHandler, chatID int64) string {
	policy := messageHookExecutionPolicy(registered)
	switch policy.Ordering {
	case core.MessageHookOrderingPlugin:
		owner := messageHookOrderingOwner(registered.scope)
		if registered.routing.Lane == core.MessageHookEvent {
			return fmt.Sprintf("%s:%s", messageHookEventOrderingDomain, owner)
		}
		return fmt.Sprintf("%s:%s", messageHookDecisionOrderingDomain, owner)
	case core.MessageHookOrderingPluginChat:
		if registered.routing.Lane == core.MessageHookDecision {
			owner := messageHookOrderingOwner(registered.scope)
			return fmt.Sprintf("%s:%s:chat:%d", messageHookDecisionOrderingDomain, owner, chatID)
		}
		return messageHookEventOrderingKey(registered.scope, chatID)
	case core.MessageHookOrderingChat:
		if registered.routing.Lane == core.MessageHookEvent {
			return fmt.Sprintf("%s:chat:%d", messageHookEventOrderingDomain, chatID)
		}
		return messageHookDecisionOrderingKey(chatID)
	default:
		return ""
	}
}
