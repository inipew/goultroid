package execution

import (
	"context"
	"testing"
)

func newPolicyContext() *ExecutionContext {
	return NewExecutionContext(
		context.Background(),
		SourceUserbot,
		NewActor(42, 100, false, false),
		100,
		7,
		".test",
		nil,
	)
}

func TestPolicyAllowsMatchingContext(t *testing.T) {
	ctx := newPolicyContext()
	ctx.SetChatKind(ChatGroup)

	policy := Policy{
		Surfaces: SurfaceUserbot,
		GroupOnly: true,
		ReplyRequired: true,
	}
	if err := policy.Validate(ctx); err != nil {
		t.Fatalf("expected policy to pass: %v", err)
	}
}

func TestPolicyRejectsWrongSurface(t *testing.T) {
	ctx := newPolicyContext()
	if err := (Policy{Surfaces: SurfaceInline}).Validate(ctx); err == nil {
		t.Fatal("expected wrong surface to be rejected")
	}
}

func TestPolicyRejectsUnauthorizedActor(t *testing.T) {
	ctx := newPolicyContext()
	if err := (Policy{RequireSudo: true}).Validate(ctx); err == nil {
		t.Fatal("expected non-sudo actor to be rejected")
	}
}

func TestPolicyRejectsPrivateOnlyForGroup(t *testing.T) {
	ctx := newPolicyContext()
	ctx.SetChatKind(ChatGroup)
	if err := (Policy{PrivateOnly: true}).Validate(ctx); err == nil {
		t.Fatal("expected private-only policy to reject group chat")
	}
}

func TestPolicyRejectsGroupOnlyForUnknownChat(t *testing.T) {
	ctx := newPolicyContext()
	if err := (Policy{GroupOnly: true}).Validate(ctx); err == nil {
		t.Fatal("expected group-only policy to fail closed for unknown chat kind")
	}
}

func TestPolicyRejectsConflictingChatModes(t *testing.T) {
	ctx := newPolicyContext()
	ctx.SetChatKind(ChatGroup)
	if err := (Policy{GroupOnly: true, PrivateOnly: true}).Validate(ctx); err == nil {
		t.Fatal("expected conflicting policy to be rejected")
	}
}
