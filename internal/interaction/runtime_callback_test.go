package interaction

import (
	"context"
	"errors"
	"testing"
)

func TestCallbackRevisionRejectsOldButtons(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}, State: []byte("one")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	oldData, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	updated, err := runtime.UpdateState(context.Background(), created.Session.ID, UpdateRequest{ExpectedRevision: 1, State: []byte("two")})
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", updated.Revision)
	}
	if _, err := runtime.ResolveCallback(context.Background(), oldData, Binding{ActorID: 1}); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("ResolveCallback(old) error = %v, want %v", err, ErrStaleToken)
	}
	newData, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData(new) error = %v", err)
	}
	resolved, err := runtime.ResolveCallback(context.Background(), newData, Binding{ActorID: 1})
	if err != nil {
		t.Fatalf("ResolveCallback(new) error = %v", err)
	}
	if resolved.Session.Revision != 2 {
		t.Fatalf("resolved revision = %d, want 2", resolved.Session.Revision)
	}
}

func TestBindTargetDoesNotInvalidateRenderedRevision(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	bound, err := runtime.BindTarget(context.Background(), created.Session.ID, 1, TargetBinding{ChatID: 7, MessageID: 9})
	if err != nil {
		t.Fatalf("BindTarget() error = %v", err)
	}
	if bound.Revision != 1 {
		t.Fatalf("revision after bind = %d, want 1", bound.Revision)
	}
	if _, err := runtime.ResolveCallback(context.Background(), data, Binding{ActorID: 1, ChatID: 7, MessageID: 9}); err != nil {
		t.Fatalf("ResolveCallback() after bind error = %v", err)
	}
	if _, err := runtime.ResolveCallback(context.Background(), data, Binding{ActorID: 1, ChatID: 7, MessageID: 10}); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("wrong message error = %v, want binding mismatch", err)
	}
}

func TestResolveCallbackClaimsFirstConcreteInlineTarget(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 42},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}

	first, err := runtime.ResolveCallback(context.Background(), data, Binding{
		ActorID:         42,
		InlineMessageID: "inline:first",
	})
	if err != nil {
		t.Fatalf("ResolveCallback(first) error = %v", err)
	}
	if first.Session.Binding.InlineMessageID != "inline:first" {
		t.Fatalf("inline target was not claimed: %+v", first.Session.Binding)
	}
	if first.Session.Revision != created.Session.Revision {
		t.Fatalf("target claim changed revision: got %d want %d", first.Session.Revision, created.Session.Revision)
	}

	if _, err := runtime.ResolveCallback(context.Background(), data, Binding{
		ActorID:         42,
		InlineMessageID: "inline:first",
	}); err != nil {
		t.Fatalf("ResolveCallback(same target) error = %v", err)
	}
	if _, err := runtime.ResolveCallback(context.Background(), data, Binding{
		ActorID:         42,
		InlineMessageID: "inline:copy",
	}); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("ResolveCallback(copied target) error = %v, want %v", err, ErrBindingMismatch)
	}
	if _, err := runtime.ResolveCallback(context.Background(), data, Binding{
		ActorID:         99,
		InlineMessageID: "inline:first",
	}); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("ResolveCallback(wrong actor) error = %v, want %v", err, ErrBindingMismatch)
	}
}
