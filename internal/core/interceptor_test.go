package core

import (
	"testing"
	"time"
)

func TestHandledMessageMarker(t *testing.T) {
	MarkMessageHandled(123, 456)
	if !ConsumeMessageHandled(123, 456) {
		t.Fatal("expected marker to be consumed")
	}
	if ConsumeMessageHandled(123, 456) {
		t.Fatal("marker must be single-use")
	}
}

func TestHandledMessageMarkerExpires(t *testing.T) {
	key := handledMessageKey{chatID: 321, messageID: 654}
	handledMessages.Store(key, handledMessageEntry{expiresAt: time.Now().Add(-time.Second)})
	if ConsumeMessageHandled(key.chatID, key.messageID) {
		t.Fatal("expired marker must not be consumed as handled")
	}
}
