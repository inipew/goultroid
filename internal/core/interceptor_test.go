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

func TestMessageDecision(t *testing.T) {
	d := NewMessageDecision(ExecutionScheduled)
	if d.Origin() != ExecutionScheduled {
		t.Fatalf("expected ExecutionScheduled, got %v", d.Origin())
	}
	if d.IsHandled() || d.IsSuppressedAutomation() || d.IsSuppressedAFK() || d.IsSuppressedFilters() || d.IsSuppressedCommands() {
		t.Fatal("initial flags must be false")
	}

	d.SetHandled(true)
	d.SetSuppressAutomation(true)
	d.SetSuppressAFK(true)
	d.SetSuppressFilters(true)
	d.SetSuppressCommands(true)
	d.SetOrigin(ExecutionInteractive)

	if !d.IsHandled() || !d.IsSuppressedAutomation() || !d.IsSuppressedAFK() || !d.IsSuppressedFilters() || !d.IsSuppressedCommands() {
		t.Fatal("flags must be true after setting")
	}
	if d.Origin() != ExecutionInteractive {
		t.Fatalf("expected ExecutionInteractive, got %v", d.Origin())
	}

	ctx := WithMessageDecision(nil, d)
	retrieved := GetMessageDecision(ctx)
	if retrieved != d {
		t.Fatalf("retrieved decision does not match original")
	}
	if GetMessageDecision(nil) != nil {
		t.Fatal("expected nil on nil context")
	}
}
