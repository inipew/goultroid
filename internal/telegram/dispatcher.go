package telegram

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// NativeInteractionDispatcher is the narrow userbot a2 ingress boundary. It
// claims callbacks owned by the canonical a2 protocol.
type NativeInteractionDispatcher interface {
	HandleCallback(context.Context, *core.CallbackQueryEvent) (bool, error)
}

// Dispatcher processes incoming Telegram updates and routes them to userbot commands.
// Composition root is split across dispatcher_*.go files (peer, handlers, accessors, callback, dispatch).
type Dispatcher struct {
	router             *core.Router
	perms              *core.Permissions
	svc                core.TelegramServicer
	logger             *zap.Logger
	cooldown           *core.CooldownTracker
	executor           *core.CommandExecutor
	selfID             int64
	resolver           core.PeerResolver
	rootCtx            context.Context
	eventBus           *core.EventBus
	albumBuffer        *core.AlbumBuffer
	localizer          core.Localizer
	nativeInteractions NativeInteractionDispatcher
	inlineEngine       *inline.Engine
	normalizer         UpdateNormalizer
	idempotencyMgr     *idempotency.Manager
	ingressDedupe      *ingressMessageDedupe
	tasks              tasks.Client
	scopeResolver      func(string) (tasks.ScopeIdentity, bool)

	messageHandlers   []prioritizedHandler
	messageRouteIndex atomic.Pointer[messageHandlerIndex]
	nextHandlerID     uint64
	acceptingUpdates  atomic.Bool
	inFlight          lifecycleCounter
	cmdWG             lifecycleCounter
	runningCommands   atomic.Int64
	totalCommands     atomic.Int64
	mu                sync.RWMutex

	peerSignal     chan struct{}
	peerUsers      map[int64]*tg.User
	peerChannels   map[int64]*tg.Channel
	peerChats      map[int64]*tg.Chat
	peerWG         sync.WaitGroup
	peerStopOnce   sync.Once
	peerDone       chan struct{}
	peerCancel     context.CancelFunc
	peerDropped    atomic.Int64
	peerEnqueued   atomic.Int64
	peerSaveFailed atomic.Int64
	stopping       atomic.Bool
}

// SetPluginScopeResolver binds Telegram work to the currently active plugin generation.
func (d *Dispatcher) SetPluginScopeResolver(resolve func(string) (tasks.ScopeIdentity, bool)) {
	d.mu.Lock()
	d.scopeResolver = resolve
	d.mu.Unlock()
}

func (d *Dispatcher) resolvePluginScope(owner string) (tasks.ScopeIdentity, bool) {
	d.mu.RLock()
	resolve := d.scopeResolver
	d.mu.RUnlock()
	if resolve == nil {
		return tasks.ScopeIdentity{}, false
	}
	return resolve(owner)
}

// DispatcherDeps specifies dependencies for initializing a Dispatcher via dependency injection.
type DispatcherDeps struct {
	Router         *core.Router
	Permissions    *core.Permissions
	Service        core.TelegramServicer
	Logger         *zap.Logger
	EventBus       *core.EventBus
	Localizer      core.Localizer
	InlineEngine   *inline.Engine
	Resolver       core.PeerResolver
	Tasks          tasks.Client
}

// NewDispatcherWithDeps constructs a Dispatcher with all available dependencies.
func NewDispatcherWithDeps(deps DispatcherDeps) (*Dispatcher, error) {
	if deps.Router == nil {
		return nil, fmt.Errorf("dispatcher: Router is required")
	}
	if deps.Permissions == nil {
		return nil, fmt.Errorf("dispatcher: Permissions is required")
	}
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	d := NewDispatcher(deps.Router, deps.Permissions, deps.Service, deps.Logger)
	if deps.Tasks != nil {
		d.SetTasks(deps.Tasks)
	}
	if deps.EventBus != nil {
		d.SetEventBus(deps.EventBus)
	}
	if deps.Localizer != nil {
		d.SetLocalizer(deps.Localizer)
	}
	if deps.InlineEngine != nil {
		d.SetInlineEngine(deps.InlineEngine)
	}
	if deps.Resolver != nil {
		d.SetResolver(deps.Resolver)
	}
	return d, nil
}

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(
	router *core.Router,
	perms *core.Permissions,
	svc core.TelegramServicer,
	logger *zap.Logger,
) *Dispatcher {
	if logger == nil {
		logger = zap.NewNop()
	}
	cooldown := core.NewCooldownTracker()
	executor := core.NewCommandExecutor(logger, cooldown, 30*time.Second)
	d := &Dispatcher{
		router:        router,
		perms:         perms,
		svc:           svc,
		logger:        logger,
		cooldown:      cooldown,
		executor:      executor,
		albumBuffer:   core.NewAlbumBuffer(10 * time.Minute),
		normalizer:    NewNormalizer(),
		ingressDedupe: newIngressMessageDedupe(defaultIngressDedupeTTL, defaultIngressDedupeCapacity),
		peerDone:      make(chan struct{}),
	}
	d.messageRouteIndex.Store(&messageHandlerIndex{})
	d.acceptingUpdates.Store(true)
	return d
}

// SetNormalizer configures a custom update normalizer.
func (d *Dispatcher) SetNormalizer(n UpdateNormalizer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.normalizer = n
}

// SetIdempotency configures durable deduplication for recognized commands and callbacks.
// Ordinary message ingress is deduplicated by the bounded in-memory ingress cache.
func (d *Dispatcher) SetIdempotency(mgr *idempotency.Manager) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.idempotencyMgr = mgr
}
