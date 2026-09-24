package orchestration

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
)

type Context struct {
	ctx         context.Context
	engine      *Engine
	session     interaction.Session
	target      presentation.Target
	queryID     int64
	preparation any
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

// Preparation returns opaque state produced by a prepared-action admission hook.
// Ordinary a2 actions return nil.
func (c *Context) Preparation() any {
	if c == nil {
		return nil
	}
	return c.preparation
}

// SetTarget attaches the transport target corresponding to the session's
// already-bound presentation message. It is primarily used when a free-form
// input arrives as a separate Telegram message.
func (c *Context) SetTarget(target presentation.Target) error {
	if c == nil || c.engine == nil {
		return ErrInvalidEngine
	}
	_, binding, err := sessionTarget(target, c.session.Binding.ActorID)
	if err != nil {
		return err
	}
	if !c.session.Binding.Matches(binding) {
		return ErrInvalidTarget
	}
	c.target = target
	return nil
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

// Touch extends the live session deadline without changing state or revision.
// It is intended for owner-bound navigation surfaces that should remain usable
// while the authorized actor is actively interacting with the original message.
func (c *Context) Touch(ttl time.Duration) error {
	if c == nil || c.engine == nil {
		return ErrInvalidEngine
	}
	updated, err := c.engine.sessions.Touch(c.Context(), c.session.ID, ttl)
	if err != nil {
		return err
	}
	c.session = updated
	return nil
}

// AwaitInput atomically advances opaque state, reserves a bounded actor+chat
// input claim, and renders the corresponding prompt revision. If presentation
// fails, the claim is released so unseen input prompts never remain active.
func (c *Context) AwaitInput(state []byte, ttl time.Duration, view presentation.View) error {
	if c == nil || c.engine == nil || c.target == nil {
		return ErrInvalidEngine
	}
	updated, err := c.engine.sessions.ArmInput(c.Context(), c.session.ID, interaction.InputRequest{
		ExpectedRevision: c.session.Revision,
		State:            state,
		TTL:              ttl,
	})
	if err != nil {
		return err
	}
	c.session = updated
	if err := c.Edit(view); err != nil {
		c.engine.sessions.ReleaseInput(c.session.ID)
		return err
	}
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
