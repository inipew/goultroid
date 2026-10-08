package telegram

import (
	"fmt"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

// messageHookPeerIdentity uses the canonical peer kind and numeric ID. Telegram
// can assign the same number to a user, basic group, and channel. The access
// hash is a credential, not part of the stable identity.
func messageHookPeerIdentity(peer core.PeerRef) string {
	if peer.ID == 0 {
		return "unknown:0"
	}
	switch peer.Kind {
	case core.PeerKindUser:
		return fmt.Sprintf("user:%d", peer.ID)
	case core.PeerKindChat:
		return fmt.Sprintf("chat:%d", peer.ID)
	case core.PeerKindChannel:
		return fmt.Sprintf("channel:%d", peer.ID)
	default:
		return "unknown:0"
	}
}

// Every hook task ID includes the peer class to avoid collision for otherwise
// identical handler/chat/message numbers across Telegram peer namespaces.
func messageHookTaskID(lane string, handlerID uint64, peer core.PeerRef, messageID int) tasks.TaskID {
	return tasks.TaskID(fmt.Sprintf("%s:%d:%s:%d", lane, handlerID, messageHookPeerIdentity(peer), messageID))
}

// Decision tasks retain ordering per peer; decision and event domains differ.
func messageHookDecisionOrderingKey(peer core.PeerRef) string {
	return "msg-decision:" + messageHookPeerIdentity(peer)
}

// Events serialize per plugin and peer. Owner stays stable across generations;
// TaskEngine scope cancellation fences tasks from stale generations.
func messageHookEventOrderingKey(scope tasks.ScopeIdentity, peer core.PeerRef) string {
	owner := scope.Owner
	if owner == "" {
		owner = "unscoped"
	}
	return fmt.Sprintf("msg-event:%s:%s", owner, messageHookPeerIdentity(peer))
}
