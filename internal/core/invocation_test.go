package core

import (
	"context"
	"errors"
	"testing"
)

func TestCommandInvocation_DefaultsAreSurfaceAware(t *testing.T) {
	perms := NewPermissions(100, []int64{200})

	public := Command{Name: "ping", Permission: PermissionEveryone}
	if got := public.EffectiveInvocation(ExecutionInteractive); got != InvocationSelfOnly {
		t.Fatalf("userbot default=%v, want SelfOnly", got)
	}
	if got := public.EffectiveInvocation(ExecutionAssistant); got != InvocationAnyone {
		t.Fatalf("assistant default=%v, want Anyone", got)
	}
	if public.CanInvoke(ExecutionInteractive, 300, false, perms) {
		t.Fatal("regular user unexpectedly invoked default userbot command")
	}
	if !public.CanInvoke(ExecutionInteractive, 100, false, perms) {
		t.Fatal("owner could not invoke default userbot command")
	}
	if !public.CanInvoke(ExecutionAssistant, 300, false, perms) {
		t.Fatal("regular user could not invoke default assistant command")
	}
}

func TestCommandInvocation_LegacySudoDefaultPreserved(t *testing.T) {
	perms := NewPermissions(100, []int64{200})
	cmd := Command{Name: "ban", Permission: PermissionSudo}

	if got := cmd.EffectiveInvocation(ExecutionInteractive); got != InvocationSelfOrSudo {
		t.Fatalf("sudo command userbot default=%v, want SelfOrSudo", got)
	}
	if !cmd.CanInvoke(ExecutionInteractive, 200, false, perms) {
		t.Fatal("sudo user could not invoke legacy sudo command")
	}
	if cmd.CanInvoke(ExecutionInteractive, 300, false, perms) {
		t.Fatal("regular user invoked legacy sudo command")
	}
}

func TestCommandInvocation_IsIndependentFromPermission(t *testing.T) {
	perms := NewPermissions(100, nil)

	publicInvocationOwnerPermission := Command{
		Name:       "dangerous",
		Permission: PermissionOwner,
		Invocation: InvocationPolicy{
			Userbot: InvocationAnyone,
		},
	}
	if !publicInvocationOwnerPermission.CanInvoke(ExecutionInteractive, 300, false, perms) {
		t.Fatal("InvocationAnyone should pass invocation gate")
	}

	ctx := &Context{Ctx: context.Background(), Sender: &User{ID: 300}, Perms: perms}
	err := PermissionMiddleware(publicInvocationOwnerPermission)(func(*Context) error { return nil })(ctx)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("permission gate should still deny regular user, got %v", err)
	}

	privateInvocationEveryonePermission := Command{
		Name:       "private-ping",
		Permission: PermissionEveryone,
		Invocation: InvocationPolicy{
			Userbot: InvocationSelfOnly,
		},
	}
	if privateInvocationEveryonePermission.CanInvoke(ExecutionInteractive, 300, false, perms) {
		t.Fatal("PermissionEveryone must not bypass SelfOnly invocation")
	}
}

func TestCommandInvocation_InternalSourcesBypassHumanGate(t *testing.T) {
	cmd := Command{
		Name: "scheduled",
		Invocation: InvocationPolicy{
			Userbot:   InvocationSelfOnly,
			Assistant: InvocationSelfOnly,
		},
	}
	for _, source := range []ExecutionSource{ExecutionScheduled, ExecutionSystem, ExecutionAddon, ExecutionAutomation} {
		if !cmd.CanInvoke(source, 0, false, nil) {
			t.Fatalf("internal source %s unexpectedly blocked", source)
		}
	}
}
