package orchestration

import (
	"context"

	"github.com/inipew/goultroid/internal/presentation"
)

type callbackInvocationKey struct{}

type callbackInvocation struct {
	queryID int64
	target  presentation.Target
}

func withCallbackInvocation(ctx context.Context, invocation callbackInvocation) context.Context {
	return context.WithValue(ctx, callbackInvocationKey{}, invocation)
}

func callbackInvocationFromContext(ctx context.Context) (callbackInvocation, bool) {
	if ctx == nil {
		return callbackInvocation{}, false
	}
	invocation, ok := ctx.Value(callbackInvocationKey{}).(callbackInvocation)
	return invocation, ok && invocation.queryID != 0 && invocation.target != nil
}
