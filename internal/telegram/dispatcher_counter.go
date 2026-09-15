package telegram

import (
	"context"
	"sync"
)

// lifecycleCounter is a context-aware WaitGroup-like counter used by
// Dispatcher shutdown. Unlike waiting on sync.WaitGroup through a helper
// goroutine, WaitContext can return on the caller deadline without leaving a
// detached waiter behind.
//
// A positive Add from zero creates a new generation channel. The gate in
// admitIngress serializes the first ingress Add with Quiesce; child work may
// safely Add while a parent ingress reference is still held.
type lifecycleCounter struct {
	mu   sync.Mutex
	n    int
	zero chan struct{}
}

func (c *lifecycleCounter) Add(delta int) {
	if delta == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if delta > 0 && c.n == 0 {
		c.zero = make(chan struct{})
	}
	c.n += delta
	if c.n < 0 {
		panic("telegram: lifecycle counter became negative")
	}
	if c.n == 0 && c.zero != nil {
		close(c.zero)
		c.zero = nil
	}
}

func (c *lifecycleCounter) Done() { c.Add(-1) }

func (c *lifecycleCounter) WaitContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.n == 0 {
		c.mu.Unlock()
		return nil
	}
	zero := c.zero
	c.mu.Unlock()

	select {
	case <-zero:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
