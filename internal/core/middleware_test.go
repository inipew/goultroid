package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

func TestChain_ExecutionOrder(t *testing.T) {
	var order []string

	m1 := func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			order = append(order, "m1_pre")
			err := next(ctx)
			order = append(order, "m1_post")
			return err
		}
	}

	m2 := func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			order = append(order, "m2_pre")
			err := next(ctx)
			order = append(order, "m2_post")
			return err
		}
	}

	handler := func(ctx *Context) error {
		order = append(order, "handler")
		return nil
	}

	chain := NewChain(m1, m2)
	finalHandler := chain.Then(handler)

	if err := finalHandler(&Context{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedOrder := []string{"m1_pre", "m2_pre", "handler", "m2_post", "m1_post"}
	if !reflect.DeepEqual(order, expectedOrder) {
		t.Errorf("expected order %v, got %v", expectedOrder, order)
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	logger := zaptest.NewLogger(t)
	m := RecoveryMiddleware(logger)

	panickingHandler := func(ctx *Context) error {
		panic("something went terribly wrong")
	}

	wrapped := m(panickingHandler)
	err := wrapped(&Context{Command: "crash"})
	if err == nil {
		t.Fatal("expected error after recovery, got nil")
	}
}

func TestLoggingMiddleware(t *testing.T) {
	logger := zaptest.NewLogger(t)
	m := LoggingMiddleware(logger)

	ctx := &Context{
		Command: "ping",
		Sender:  &User{ID: 123},
		Chat:    &Chat{ID: -456},
		Args:    []string{"foo"},
	}

	// Successful run
	hSuccess := m(func(c *Context) error { return nil })
	if err := hSuccess(ctx); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Error run
	expectedErr := errors.New("custom failure")
	hFail := m(func(c *Context) error { return expectedErr })
	if err := hFail(ctx); !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestTimeoutMiddleware(t *testing.T) {
	m := TimeoutMiddleware(50 * time.Millisecond)

	slowHandler := func(ctx *Context) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Ctx.Done():
			return ctx.Ctx.Err()
		}
	}

	wrapped := m(slowHandler)
	err := wrapped(&Context{Ctx: context.Background()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded error, got %v", err)
	}
}

func TestPermissionMiddleware(t *testing.T) {
	perms := NewPermissions(1001, []int64{2001})

	ownerCmd := Command{Name: "restart", Permission: PermissionOwner}
	m := PermissionMiddleware(ownerCmd)

	dummyHandler := func(ctx *Context) error { return nil }
	wrapped := m(dummyHandler)

	// Normal user (ID 3001) -> Denied
	ctxNormal := &Context{
		Sender: &User{ID: 3001},
		Perms:  perms,
	}
	if err := wrapped(ctxNormal); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied for normal user, got %v", err)
	}

	// Sudo user (ID 2001) -> Denied for PermissionOwner
	ctxSudo := &Context{
		Sender: &User{ID: 2001},
		Perms:  perms,
	}
	if err := wrapped(ctxSudo); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied for sudo user, got %v", err)
	}

	// Owner (ID 1001) -> Allowed
	ctxOwner := &Context{
		Sender: &User{ID: 1001},
		Perms:  perms,
	}
	if err := wrapped(ctxOwner); err != nil {
		t.Errorf("expected owner to be allowed, got error: %v", err)
	}
}
