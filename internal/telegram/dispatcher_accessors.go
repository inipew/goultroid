package telegram

import (
	"context"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
)

// Executor returns the underlying CommandExecutor used by this dispatcher.
func (d *Dispatcher) Executor() *core.CommandExecutor {
	return d.executor
}

// SetExecutor sets the CommandExecutor used by this dispatcher.
func (d *Dispatcher) SetExecutor(executor *core.CommandExecutor) {
	if executor != nil {
		d.executor = executor
	}
}

// CommandStats returns the number of currently running commands and total dispatched commands.
func (d *Dispatcher) CommandStats() (running, total int64) {
	return d.runningCommands.Load(), d.totalCommands.Load()
}

// RunningCommands returns the number of currently executing commands.
func (d *Dispatcher) RunningCommands() int64 {
	return d.runningCommands.Load()
}

// SetRootContext sets the application root context used for command lifetime coordination.
func (d *Dispatcher) SetRootContext(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rootCtx = ctx
}

func (d *Dispatcher) getRootContext() context.Context {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.rootCtx
}

// SetService updates the TelegramServicer instance (e.g. once client is connected).
func (d *Dispatcher) SetService(svc core.TelegramServicer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.svc = svc
}

// SetResolver updates the PeerResolver instance.
func (d *Dispatcher) SetResolver(resolver core.PeerResolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolver = resolver
}

func (d *Dispatcher) getResolver() core.PeerResolver {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.resolver
}

// Resolver returns the configured PeerResolver instance.
func (d *Dispatcher) Resolver() core.PeerResolver {
	return d.getResolver()
}

// SetSelfID sets the current logged-in user ID.
func (d *Dispatcher) SetSelfID(id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.selfID = id
}

func (d *Dispatcher) getSelfID() int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.selfID
}

func (d *Dispatcher) getService() core.TelegramServicer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.svc
}

// Service returns the configured TelegramServicer.
func (d *Dispatcher) Service() core.TelegramServicer {
	return d.getService()
}

// EventBus returns the domain event bus used by this dispatcher.
func (d *Dispatcher) EventBus() *core.EventBus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.eventBus
}

// SetEventBus sets the domain event bus. Must be called before RegisterHooks.
func (d *Dispatcher) SetEventBus(bus *core.EventBus) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eventBus = bus
}

func (d *Dispatcher) getEventBus() *core.EventBus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.eventBus
}

// AlbumBuffer returns the album aggregator used by this dispatcher.
func (d *Dispatcher) AlbumBuffer() *core.AlbumBuffer {
	return d.albumBuffer
}

// SetLocalizer configures the internationalization provider.
func (d *Dispatcher) SetLocalizer(l core.Localizer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.localizer = l
}

func (d *Dispatcher) getLocalizer() core.Localizer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.localizer
}

// SetCallbackRouter configures the router for button callback queries.
func (d *Dispatcher) SetCallbackRouter(r *callback.Router) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callbackRouter = r
}

func (d *Dispatcher) getCallbackRouter() *callback.Router {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.callbackRouter
}

// SetInlineEngine configures the inline query evaluation engine.
func (d *Dispatcher) SetInlineEngine(e *inline.Engine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inlineEngine = e
}

func (d *Dispatcher) getInlineEngine() *inline.Engine {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.inlineEngine
}
