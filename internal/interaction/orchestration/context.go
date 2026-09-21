package orchestration

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
)

type Context struct {
	ctx     context.Context
	engine  *Engine
	session interaction.Session
	target  presentation.Target
	queryID int64
}

func newContext(ctx context.Context, engine *Engine, session interaction.Session, target presentation.Target, queryID int64) *Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Context{ctx: ctx, engine: engine, session: session, target: target, queryID: queryID}
}

func (c *Context) Context() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func (c *Context) Session() interaction.Session {
	if c == nil {
		return interaction.Session{}
	}
	snapshot := c.session
	snapshot.State = append([]byte(nil), c.session.State...)
	return snapshot
}

func (c *Context) State() []byte {
	if c == nil {
		return nil
	}
	return append([]byte(nil), c.session.State...)
}

func (c *Context) Target() presentation.Target {
	if c == nil {
		return nil
	}
	return c.target
}

func (c *Context) UpdateState(state []byte, ttl time.Duration) error {
	if c == nil || c.engine == nil {
		return ErrInvalidEngine
	}
	updated, err := c.engine.sessions.UpdateState(c.Context(), c.session.ID, interaction.UpdateRequest{
		ExpectedRevision: c.session.Revision,
		State:            state,
		TTL:              ttl,
	})
	if err != nil {
		return err
	}
	c.session = updated
	return nil
}

func (c *Context) Edit(view presentation.View) error {
	if c == nil || c.engine == nil || c.target == nil {
		return ErrInvalidEngine
	}
	compiled, err := c.engine.compiler.Compile(c.Context(), c.session.ID, view)
	if err != nil {
		return err
	}
	return c.engine.port.Edit(c.Context(), c.target, compiled)
}

// Transition advances opaque session state and renders the corresponding new
// revision. If transport editing fails, the older visible buttons are stale by
// design and cannot execute against the new state.
func (c *Context) Transition(state []byte, ttl time.Duration, view presentation.View) error {
	if err := c.UpdateState(state, ttl); err != nil {
		return err
	}
	return c.Edit(view)
}

func (c *Context) Answer(text string, alert bool) error {
	if c == nil || c.engine == nil {
		return ErrInvalidEngine
	}
	if c.queryID == 0 {
		return ErrNoCallback
	}
	return c.engine.port.Answer(c.Context(), presentation.Answer{QueryID: c.queryID, Text: text, Alert: alert})
}

func (c *Context) Cancel() bool {
	if c == nil || c.engine == nil || c.session.ID == "" {
		return false
	}
	return c.engine.sessions.Cancel(c.session.ID)
}
