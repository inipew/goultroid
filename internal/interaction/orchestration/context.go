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

// Terminate renders a terminal view and releases the interaction session even
// when the transport edit fails. This keeps already-visible callback tokens
// fail-closed after an explicit close/cancel action.
func (c *Context) Terminate(view presentation.View) error {
	if c == nil || c.engine == nil {
		return ErrInvalidEngine
	}
	editErr := c.Edit(view)
	c.Cancel()
	return editErr
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

// DetachedTransition advances one captured session revision and edits the
// already-bound target after the originating callback handler has returned.
// The runtime remains the sole state authority; a stale/cancelled session fails
// closed through the normal revision checks.
type DetachedTransition func(context.Context, []byte, time.Duration, presentation.View) error

func (c *Context) PrepareTransition() (DetachedTransition, error) {
	if c == nil || c.engine == nil || c.target == nil || c.session.ID == "" {
		return nil, ErrInvalidEngine
	}
	sessions := c.engine.sessions
	compiler := c.engine.compiler
	port := c.engine.port
	target := c.target
	sessionID := c.session.ID
	expectedRevision := c.session.Revision
	return func(ctx context.Context, state []byte, ttl time.Duration, view presentation.View) error {
		if ctx == nil {
			ctx = context.Background()
		}
		_, err := sessions.UpdateState(ctx, sessionID, interaction.UpdateRequest{
			ExpectedRevision: expectedRevision,
			State:            state,
			TTL:              ttl,
		})
		if err != nil {
			return err
		}
		compiled, err := compiler.Compile(ctx, sessionID, view)
		if err != nil {
			return err
		}
		return port.Edit(ctx, target, compiled)
	}, nil
}

// PrepareCancel returns a minimal detached session cancellation handle for
// asynchronous terminal completion. It retains no presentation view/state.
func (c *Context) PrepareCancel() (func() bool, error) {
	if c == nil || c.engine == nil || c.session.ID == "" {
		return nil, ErrInvalidEngine
	}
	sessions := c.engine.sessions
	sessionID := c.session.ID
	return func() bool {
		return sessions.Cancel(sessionID)
	}, nil
}

// MediaDelivery is a detached target-bound delivery handle.
type MediaDelivery func(context.Context, presentation.Media) error

// PrepareMediaDelivery snapshots the transport target without retaining session state.
func (c *Context) PrepareMediaDelivery() (MediaDelivery, error) {
	if c == nil || c.engine == nil || c.target == nil {
		return nil, ErrInvalidEngine
	}
	deliverer, ok := c.engine.port.(presentation.MediaDeliverer)
	if !ok {
		return nil, ErrInvalidTarget
	}
	target := c.target
	return func(ctx context.Context, media presentation.Media) error {
		if ctx == nil {
			ctx = context.Background()
		}
		return deliverer.DeliverMedia(ctx, target, media)
	}, nil
}

// PrepareTextEdit snapshots a target-bound, callback-free editor for transient
// asynchronous status updates such as downloader progress. Dynamic text is
// compiled on each update, but no callback rows or mutable session state are retained.
func (c *Context) PrepareTextEdit() (func(context.Context, string) error, error) {
	if c == nil || c.engine == nil || c.target == nil {
		return nil, ErrInvalidEngine
	}
	target := c.target
	port := c.engine.port
	compiler := c.engine.compiler
	sessionID := c.session.ID
	return func(ctx context.Context, text string) error {
		if ctx == nil {
			ctx = context.Background()
		}
		compiled, err := compiler.Compile(ctx, sessionID, presentation.View{Text: text})
		if err != nil {
			return err
		}
		return port.Edit(ctx, target, compiled)
	}, nil
}

// PrepareStaticEdit compiles a callback-free terminal view for asynchronous updates.
func (c *Context) PrepareStaticEdit(view presentation.View) (func(context.Context) error, error) {
	if c == nil || c.engine == nil || c.target == nil {
		return nil, ErrInvalidEngine
	}
	if len(view.Rows) != 0 {
		return nil, ErrInvalidTarget
	}
	compiled, err := c.engine.compiler.Compile(c.Context(), c.session.ID, view)
	if err != nil {
		return nil, err
	}
	target := c.target
	port := c.engine.port
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		return port.Edit(ctx, target, compiled)
	}, nil
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
