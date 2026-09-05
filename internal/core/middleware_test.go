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
	cmdDefault := Command{Name: "ping"}
	mDefault := TimeoutMiddleware(cmdDefault, 50*time.Millisecond)

	slowHandler := func(ctx *Context) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Ctx.Done():
			return ctx.Ctx.Err()
		}
	}

	wrapped := mDefault(slowHandler)
	err := wrapped(&Context{Ctx: context.Background()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded error, got %v", err)
	}

	// Command with custom timeout overrides default timeout
	cmdCustom := Command{Name: "exec", Timeout: 300 * time.Millisecond}
	mCustom := TimeoutMiddleware(cmdCustom, 50*time.Millisecond)

	mediumHandler := func(ctx *Context) error {
		select {
		case <-time.After(100 * time.Millisecond):
			return nil
		case <-ctx.Ctx.Done():
			return ctx.Ctx.Err()
		}
	}

	wrappedCustom := mCustom(mediumHandler)
	err = wrappedCustom(&Context{Ctx: context.Background()})
	if err != nil {
		t.Errorf("expected custom timeout to allow 100ms execution, got: %v", err)
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

func TestFilterMiddleware(t *testing.T) {
	dummy := func(ctx *Context) error { return nil }

	// GroupOnly test
	groupCmd := Command{Name: "ban", GroupOnly: true}
	mGroup := FilterMiddleware(groupCmd)(dummy)

	if err := mGroup(&Context{Chat: &Chat{Type: "private"}}); !errors.Is(err, ErrGroupOnly) {
		t.Errorf("expected ErrGroupOnly, got %v", err)
	}
	if err := mGroup(&Context{Chat: &Chat{Type: "group"}}); err != nil {
		t.Errorf("expected group to pass GroupOnly filter, got %v", err)
	}
	if err := mGroup(&Context{Chat: &Chat{Type: "supergroup"}}); err != nil {
		t.Errorf("expected supergroup to pass GroupOnly filter, got %v", err)
	}

	// Outgoing commands bypass GroupOnly
	if err := mGroup(&Context{Chat: &Chat{Type: "private"}, Message: &Message{IsOutgoing: true}}); err != nil {
		t.Errorf("expected outgoing message to bypass GroupOnly, got %v", err)
	}

	// PrivateOnly test
	privateCmd := Command{Name: "secret", PrivateOnly: true}
	mPrivate := FilterMiddleware(privateCmd)(dummy)

	if err := mPrivate(&Context{Chat: &Chat{Type: "group"}}); !errors.Is(err, ErrPrivateOnly) {
		t.Errorf("expected ErrPrivateOnly, got %v", err)
	}
	if err := mPrivate(&Context{Chat: &Chat{Type: "private"}}); err != nil {
		t.Errorf("expected private chat to pass PrivateOnly filter, got %v", err)
	}

	// Outgoing commands bypass PrivateOnly
	if err := mPrivate(&Context{Chat: &Chat{Type: "group"}, Message: &Message{IsOutgoing: true}}); err != nil {
		t.Errorf("expected outgoing message to bypass PrivateOnly, got %v", err)
	}

	// ReplyOnly test
	replyCmd := Command{Name: "info", ReplyOnly: true}
	mReply := FilterMiddleware(replyCmd)(dummy)

	if err := mReply(&Context{Message: &Message{ReplyToID: 0}}); !errors.Is(err, ErrReplyRequired) {
		t.Errorf("expected ErrReplyRequired when ReplyToID is 0, got %v", err)
	}
	if err := mReply(&Context{Message: nil}); !errors.Is(err, ErrReplyRequired) {
		t.Errorf("expected ErrReplyRequired when Message is nil, got %v", err)
	}
	if err := mReply(&Context{Message: &Message{ReplyToID: 42}}); err != nil {
		t.Errorf("expected reply message to pass ReplyOnly filter, got %v", err)
	}
	// ReplyOnly is strictly enforced even for outgoing messages
	if err := mReply(&Context{Message: &Message{ReplyToID: 0, IsOutgoing: true}}); !errors.Is(err, ErrReplyRequired) {
		t.Errorf("expected ErrReplyRequired for outgoing message when ReplyToID is 0, got %v", err)
	}
}

func TestCooldownMiddleware(t *testing.T) {
	dummy := func(ctx *Context) error { return nil }
	tracker := NewCooldownTracker()

	cmd := Command{Name: "ping", Cooldown: 100 * time.Millisecond}
	m := CooldownMiddleware(cmd, tracker)(dummy)

	ctxNormal := &Context{
		Sender: &User{ID: 222},
		Perms:  NewPermissions(111, nil),
	}

	// First execution -> pass
	if err := m(ctxNormal); err != nil {
		t.Fatalf("expected first execution to pass, got %v", err)
	}

	// Immediate second execution -> ErrCooldownActive
	if err := m(ctxNormal); !errors.Is(err, ErrCooldownActive) {
		t.Fatalf("expected ErrCooldownActive, got %v", err)
	}

	// Owner execution -> always bypasses cooldown
	ctxOwner := &Context{
		Sender: &User{ID: 111},
		Perms:  NewPermissions(111, nil),
	}
	if err := m(ctxOwner); err != nil {
		t.Fatalf("owner should bypass cooldown, got %v", err)
	}
	if err := m(ctxOwner); err != nil {
		t.Fatalf("owner should bypass cooldown repeatedly, got %v", err)
	}
}

