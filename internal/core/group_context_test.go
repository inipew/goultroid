package core

import "testing"

func TestNormalizeChatKind(t *testing.T) {
	tests := map[string]ChatKind{
		"private":      ChatKindPrivate,
		"GROUP":        ChatKindGroup,
		" supergroup ": ChatKindSupergroup,
		"channel":      ChatKindChannel,
		"":             ChatKindUnknown,
		"mystery":      ChatKindUnknown,
	}
	for input, want := range tests {
		if got := NormalizeChatKind(input); got != want {
			t.Fatalf("NormalizeChatKind(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestContextGroupExecutionSeparatesGlobalAndContextualIdentity(t *testing.T) {
	ctx := &Context{
		Source:    ExecutionAssistant,
		Command:   "groupinfo",
		Chat:      &Chat{ID: 99, Type: "supergroup"},
		Sender:    &User{ID: 42},
		Message:   &Message{ID: 10, TopicID: 7},
		Perms:     NewPermissions(42, nil),
		Principal: &Principal{UserID: 42, IsOwner: true, IsSudo: true, Level: PermissionOwner},
	}

	got, ok := ctx.GroupExecution()
	if !ok {
		t.Fatal("expected group execution context")
	}
	if got.ChatID != 99 || got.Kind != ChatKindSupergroup || got.TopicID != 7 {
		t.Fatalf("unexpected chat context: %+v", got)
	}
	if got.Source != ExecutionAssistant || got.Feature != "groupinfo" {
		t.Fatalf("unexpected execution identity: %+v", got)
	}
	if got.Actor.UserID != 42 || !got.Actor.IsOwner || !got.Actor.IsSudo {
		t.Fatalf("unexpected global actor identity: %+v", got.Actor)
	}
	if got.Actor.Role != GroupActorRoleUnknown || got.Actor.Verified {
		t.Fatalf("P7-A must not manufacture Telegram role authority: %+v", got.Actor)
	}
}

func TestManagerGroupExcludesBroadcastChannel(t *testing.T) {
	if !(&Chat{Type: "group"}).IsManagerGroup() {
		t.Fatal("basic group should be a manager group")
	}
	if !(&Chat{Type: "supergroup"}).IsManagerGroup() {
		t.Fatal("supergroup should be a manager group")
	}
	if (&Chat{Type: "channel"}).IsManagerGroup() {
		t.Fatal("broadcast channel must fail closed for manager group commands")
	}
	if (&Chat{Type: "private"}).IsManagerGroup() {
		t.Fatal("private chat must not be a manager group")
	}
}
