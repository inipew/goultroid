package telegram

import (
	"context"

	"github.com/inipew/goultroid/internal/core"
	presentationselfinline "github.com/inipew/goultroid/internal/presentation/selfinline"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
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

// CommandConcurrencySnapshot exposes dispatcher command pressure. Capacity is
// owned by TaskEngine and is intentionally not duplicated in Dispatcher.
type CommandConcurrencySnapshot struct {
	Capacity int
	Active   int
	Total    int64
}

func (d *Dispatcher) CommandConcurrency() CommandConcurrencySnapshot {
	return CommandConcurrencySnapshot{
		Active: int(d.runningCommands.Load()),
		Total:  d.totalCommands.Load(),
	}
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

// SetService is the compatibility injection path for older tests and
// composition. Production client startup binds capability slots directly.
func (d *Dispatcher) SetService(svc DispatcherService) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.compatService = svc
	d.services = dispatcherCapabilitiesFrom(svc)
}

func (d *Dispatcher) setRuntimeService(svc *Service) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.compatService = nil
	d.services = dispatcherCapabilitiesFrom(svc)
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

func (d *Dispatcher) getService() DispatcherService {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.compatService
}

// Service returns only the compatibility aggregate configured through
// NewDispatcher/SetService. Production runtime wiring uses narrow accessors.
func (d *Dispatcher) Service() DispatcherService {
	return d.getService()
}

func (d *Dispatcher) getCommandCapabilities() core.TelegramCapabilities {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.command
}

// CommandCapabilities returns the capability-sized command transport snapshot
// used by production command contexts.
func (d *Dispatcher) CommandCapabilities() core.TelegramCapabilities {
	return d.getCommandCapabilities()
}

// CommandService is retained only for compatibility callers injected through
// SetService/NewDispatcher. Production runtime composition uses capability
// accessors and does not depend on this aggregate.
func (d *Dispatcher) CommandService() core.CommandTelegramServicer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.compatService == nil {
		return nil
	}
	return d.compatService
}

func (d *Dispatcher) MessageService() core.MessageServicer {
	return d.getCommandCapabilities().Messages
}

func (d *Dispatcher) AdminService() core.AdminServicer {
	return d.getCommandCapabilities().Admin
}

func (d *Dispatcher) MediaService() core.MediaServicer {
	return d.getCommandCapabilities().Media
}

func (d *Dispatcher) PeerService() core.PeerServicer {
	return d.getCommandCapabilities().Peers
}

func (d *Dispatcher) ProfileService() core.ProfileServicer {
	return d.getCommandCapabilities().Profile
}

func (d *Dispatcher) ContextualMessageService() core.ContextualMessageServicer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.contextualMessages
}

func (d *Dispatcher) ContextualMediaService() core.ContextualMediaServicer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.contextualMedia
}

func (d *Dispatcher) PresentationService() presentationtelegram.BridgeService {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.presentation
}

func (d *Dispatcher) SelfInlineTransport() presentationselfinline.Transport {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.selfInline
}

func (d *Dispatcher) getCallbackAnswerer() callbackQueryAnswerer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.callback
}

func (d *Dispatcher) getInlineAnswerer() inline.TelegramAnswerer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.inline
}

func (d *Dispatcher) getOriginTracker() botOriginTracker {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.services.origin
}

func (d *Dispatcher) OriginTracker() OriginTracker {
	return d.getOriginTracker()
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

func (d *Dispatcher) getNormalizer() UpdateNormalizer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.normalizer
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

// SetLocalizerResolver binds a locale-specific translator to each command
// invocation without mutating the shared process localizer.
func (d *Dispatcher) SetLocalizerResolver(resolve func(context.Context, int64, int64) core.Localizer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.localizerResolver = resolve
}

func (d *Dispatcher) getLocalizerResolver() func(context.Context, int64, int64) core.Localizer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.localizerResolver
}

// SetNativeInteractions installs the native/userbot a2 callback ingress.
func (d *Dispatcher) SetNativeInteractions(interactions NativeInteractionDispatcher) {
	d.mu.Lock()
	d.nativeInteractions = interactions
	d.mu.Unlock()
}

func (d *Dispatcher) getNativeInteractions() NativeInteractionDispatcher {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.nativeInteractions
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
