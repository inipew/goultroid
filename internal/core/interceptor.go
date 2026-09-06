package core

import (
	"sync"
	"time"
)

// InterceptDecision controls whether message processing continues after an interceptor.
type InterceptDecision uint8

const (
	InterceptContinue InterceptDecision = iota
	InterceptHandled
)

type handledMessageKey struct { chatID int64; messageID int }
type handledMessageEntry struct { expiresAt time.Time }

var handledMessages sync.Map

func MarkMessageHandled(chatID int64, messageID int) {
	if messageID == 0 { return }
	handledMessages.Store(handledMessageKey{chatID: chatID, messageID: messageID}, handledMessageEntry{expiresAt: time.Now().Add(30 * time.Second)})
}

func ConsumeMessageHandled(chatID int64, messageID int) bool {
	if messageID == 0 { return false }
	key := handledMessageKey{chatID: chatID, messageID: messageID}
	value, ok := handledMessages.LoadAndDelete(key)
	if !ok { return false }
	entry, ok := value.(handledMessageEntry)
	return ok && time.Now().Before(entry.expiresAt)
}
