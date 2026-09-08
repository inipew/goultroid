package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type LifecycleState uint32

const (
	LifecycleNew LifecycleState = iota
	LifecycleStarting
	LifecycleRunning
	LifecycleQuiescing
	LifecycleStopping
	LifecycleStopped
	LifecycleFailed
)

var (
	ErrLifecycleInvalidTransition = errors.New("invalid application lifecycle transition")
	ErrLifecycleQuiescing         = errors.New("application is quiescing")
	ErrLifecycleStopped           = errors.New("application is stopped")
)

func (s LifecycleState) String() string {
	switch s {
	case LifecycleNew:
		return "new"
	case LifecycleStarting:
		return "starting"
	case LifecycleRunning:
		return "running"
	case LifecycleQuiescing:
		return "quiescing"
	case LifecycleStopping:
		return "stopping"
	case LifecycleStopped:
		return "stopped"
	case LifecycleFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type lifecycle struct {
	state        atomic.Uint32
	mu           sync.Mutex
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

func newLifecycle() *lifecycle {
	l := &lifecycle{shutdownDone: make(chan struct{})}
	l.state.Store(uint32(LifecycleNew))
	return l
}

func (l *lifecycle) State() LifecycleState { return LifecycleState(l.state.Load()) }

func (l *lifecycle) beginStart() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.State() != LifecycleNew {
		return ErrLifecycleInvalidTransition
	}
	l.state.Store(uint32(LifecycleStarting))
	return nil
}

func (l *lifecycle) markRunning() { l.state.Store(uint32(LifecycleRunning)) }
func (l *lifecycle) markFailed()  { l.state.Store(uint32(LifecycleFailed)) }

func (l *lifecycle) quiesce() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.State() {
	case LifecycleNew, LifecycleStarting, LifecycleRunning:
		l.state.Store(uint32(LifecycleQuiescing))
		return true
	case LifecycleQuiescing, LifecycleStopping, LifecycleStopped, LifecycleFailed:
		return false
	default:
		return false
	}
}

func (l *lifecycle) canAccept() bool {
	s := l.State()
	return s == LifecycleStarting || s == LifecycleRunning
}

func (l *lifecycle) shutdown(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.shutdownOnce.Do(func() {
		go func() {
			l.quiesce()
			l.state.Store(uint32(LifecycleStopping))
			l.shutdownErr = fn(ctx)
			if l.shutdownErr != nil {
				l.state.Store(uint32(LifecycleFailed))
			} else {
				l.state.Store(uint32(LifecycleStopped))
			}
			close(l.shutdownDone)
		}()
	})
	select {
	case <-l.shutdownDone:
		return l.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
