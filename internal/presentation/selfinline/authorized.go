package selfinline

import "context"

// Authorizer is evaluated immediately before every self-inline render. It lets
// application wiring preserve plugin capability ownership without exposing the
// capability gate or raw Telegram transport to feature code.
type Authorizer func() error

type authorizedRenderer struct {
	delegate  Renderer
	authorize Authorizer
}

// Authorized wraps one renderer with a per-call authorization check. The
// wrapper is stateless and intentionally does not memoize authorization across
// plugin disable/reload or manifest changes.
func Authorized(delegate Renderer, authorize Authorizer) Renderer {
	if delegate == nil || authorize == nil {
		return nil
	}
	return &authorizedRenderer{delegate: delegate, authorize: authorize}
}

func (r *authorizedRenderer) Render(ctx context.Context, request Request) (Result, error) {
	if r == nil || r.delegate == nil || r.authorize == nil {
		return Result{}, renderFailure(RenderStagePreflight, false, ErrUnavailable)
	}
	if err := r.authorize(); err != nil {
		return Result{}, renderFailure(RenderStagePreflight, false, err)
	}
	return r.delegate.Render(ctx, request)
}

var _ Renderer = (*authorizedRenderer)(nil)
