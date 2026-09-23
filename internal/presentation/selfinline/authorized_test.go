package selfinline

import (
	"context"
	"errors"
	"testing"
)

type rendererFunc func(context.Context, Request) (Result, error)

func (f rendererFunc) Render(ctx context.Context, request Request) (Result, error) {
	return f(ctx, request)
}

func TestAuthorizedChecksCapabilityEveryRender(t *testing.T) {
	denied := errors.New("denied")
	checks := 0
	calls := 0
	allowed := false
	renderer := Authorized(rendererFunc(func(context.Context, Request) (Result, error) {
		calls++
		return Result{QueryID: 7}, nil
	}), func() error {
		checks++
		if !allowed {
			return denied
		}
		return nil
	})

	if _, err := renderer.Render(context.Background(), Request{}); !errors.Is(err, denied) {
		t.Fatalf("first Render() error = %v, want %v", err, denied)
	}
	allowed = true
	if got, err := renderer.Render(context.Background(), Request{}); err != nil || got.QueryID != 7 {
		t.Fatalf("second Render() = %+v, %v", got, err)
	}
	if checks != 2 || calls != 1 {
		t.Fatalf("checks/calls = %d/%d, want 2/1", checks, calls)
	}
}
