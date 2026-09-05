package core

import (
	"errors"
	"testing"
)

func TestPermissionMiddleware_FailsClosedWithoutProvider(t *testing.T) {
	cmd := Command{Name: "exec", Permission: PermissionOwner}
	handlerCalled := false
	handler := PermissionMiddleware(cmd)(func(*Context) error {
		handlerCalled = true
		return nil
	})

	err := handler(&Context{Sender: &User{ID: 123}, Perms: nil})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied, got %v", err)
	}
	if handlerCalled {
		t.Fatal("privileged handler must not run without a permission provider")
	}
}

func TestPermissionMiddleware_PublicCommandAllowsMissingProvider(t *testing.T) {
	cmd := Command{Name: "ping", Permission: PermissionEveryone}
	handler := PermissionMiddleware(cmd)(func(*Context) error { return nil })

	if err := handler(&Context{Sender: &User{ID: 123}}); err != nil {
		t.Fatalf("public command should not require permission provider: %v", err)
	}
}
