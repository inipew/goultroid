package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/execution"
)

func TestUserFacingErrorKeepsSafeMessageSeparateFromCause(t *testing.T) {
	cause := errors.New("sqlite /home/user/private.db token=secret")
	err := WithUserMessage(cause, "The operation could not be completed.")

	if !errors.Is(err, cause) {
		t.Fatal("typed user-facing error lost internal cause")
	}
	if got := UserMessage(err); got != "The operation could not be completed." {
		t.Fatalf("UserMessage()=%q", got)
	}
	if strings.Contains(UserMessage(err), "sqlite") || strings.Contains(UserMessage(err), "secret") {
		t.Fatalf("safe user message leaked cause: %q", UserMessage(err))
	}
	if UserErrorWasPresented(err) {
		t.Fatal("WithUserMessage unexpectedly marked error as presented")
	}
	if got := ExecutionSemantics(err).Disposition; got != execution.DispositionInternal {
		t.Fatalf("unpresented semantics=%q, want internal", got)
	}
}

func TestContextFailPresentsSafeMessageOnceAndPreservesCause(t *testing.T) {
	service := &mockTelegramServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 205, IsOutgoing: true},
		Svc:     service,
		PeerID:  &tg.InputPeerSelf{},
	}
	cause := errors.New("provider token=secret database=/home/user/private.db")

	err := ctx.Fail(cause, "Unable to load the requested data. Please try again.")
	if !errors.Is(err, cause) {
		t.Fatal("Fail() lost internal cause")
	}
	if !UserErrorWasPresented(err) {
		t.Fatal("Fail() did not mark successful presentation")
	}
	if got := ExecutionSemantics(err).Disposition; got != execution.DispositionHandled {
		t.Fatalf("presented semantics=%q, want handled", got)
	}
	if !strings.Contains(service.editedText, "Unable to load the requested data") {
		t.Fatalf("safe message not presented: %q", service.editedText)
	}
	if strings.Contains(service.editedText, "token=secret") || strings.Contains(service.editedText, "/home/") {
		t.Fatalf("presentation leaked internal cause: %q", service.editedText)
	}
}


func TestContextFailAmbiguousEditPresentationFailsClosed(t *testing.T) {
	transportErr := errors.New("connection reset after error edit")
	service := &mockTelegramServicer{errToEdit: transportErr}
	ctx := &Context{
		Ctx:     context.Background(),
		Message: &Message{ID: 205, IsOutgoing: true},
		Svc:     service,
		PeerID:  &tg.InputPeerSelf{},
	}

	err := ctx.Fail(ErrUnavailable, "The service is temporarily unavailable.")
	if err == nil {
		t.Fatal("Fail() unexpectedly succeeded")
	}
	if !errors.Is(err, transportErr) || !MessageEditMayHaveCommitted(err) {
		t.Fatalf("error=%v, want ambiguous edit cause", err)
	}
	if !UserErrorWasPresented(err) {
		t.Fatal("ambiguous presentation must fail closed against duplicate feedback")
	}
	semantics := ExecutionSemantics(err)
	if semantics.Disposition != execution.DispositionHandled || semantics.Code != "user_error_presentation_unconfirmed" {
		t.Fatalf("semantics=%+v, want handled unconfirmed presentation", semantics)
	}
	if service.sendCalls != 0 {
		t.Fatalf("send calls=%d, ambiguous edit must not trigger a reply fallback", service.sendCalls)
	}
}
