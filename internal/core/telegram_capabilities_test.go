package core

import (
	"reflect"
	"testing"
)

func TestCommandTelegramServicerExcludesInteractionTransport(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf((*CommandTelegramServicer)(nil)).Elem()
	for _, forbidden := range []string{
		"AnswerCallbackQuery",
		"AnswerInlineQuery",
		"AnswerInlineQueryOptions",
		"EditInlineBotMessage",
		"EditInlineBotMessageMarkup",
		"IsBotSent",
	} {
		if _, ok := typ.MethodByName(forbidden); ok {
			t.Fatalf("CommandTelegramServicer unexpectedly contains interaction method %s", forbidden)
		}
	}
}

func TestContextualCapabilitiesRemainIndependent(t *testing.T) {
	t.Parallel()

	messageType := reflect.TypeOf((*ContextualMessageServicer)(nil)).Elem()
	mediaType := reflect.TypeOf((*ContextualMediaServicer)(nil)).Elem()

	if messageType.NumMethod() != 1 {
		t.Fatalf("ContextualMessageServicer method count = %d, want 1", messageType.NumMethod())
	}
	if mediaType.NumMethod() != 1 {
		t.Fatalf("ContextualMediaServicer method count = %d, want 1", mediaType.NumMethod())
	}
	if _, ok := messageType.MethodByName("SendMediaContext"); ok {
		t.Fatal("ContextualMessageServicer must not require media delivery")
	}
	if _, ok := mediaType.MethodByName("SendMessageContext"); ok {
		t.Fatal("ContextualMediaServicer must not require message delivery")
	}
}
