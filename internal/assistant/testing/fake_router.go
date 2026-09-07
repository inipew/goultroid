package testing

import (
	"context"
	"sync"

	"github.com/inipew/goultroid/internal/assistant/callback"
)

// FakeCallbackHandler records invocations of callback handlers.
type FakeCallbackHandler struct {
	mu            sync.RWMutex
	Calls         int
	LastTx        *callback.Transaction
	InjectedError error
}

// Handle processes the transaction and records the call.
func (f *FakeCallbackHandler) Handle(ctx context.Context, tx *callback.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	f.LastTx = tx
	return f.InjectedError
}
