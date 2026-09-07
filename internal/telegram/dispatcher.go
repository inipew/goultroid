package telegram

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"go.uber.org/zap"
)

// Dispatcher processes incoming Telegram updates and routes them to userbot commands.
// Composition root is split across dispatcher_*.go files (peer, handlers, accessors, callback, dispatch).
type Dispatcher struct {
	router         *core.Router
	perms          *core.Permissions
	svc            core.TelegramServicer
	logger         *zap.Logger
	cooldown       *core.CooldownTracker
	executor       *core.CommandExecutor
	selfID         int64
	resolver       core.PeerResolver
	rootCtx        context.Context
	eventBus       *core.EventBus
	albumBuffer    *core.AlbumBuffer
	localizer      core.Localizer
	callbackRouter *callback.Router
	inlineEngine   *inline.Engine

	messageHandlers  []prioritizedHandler
	nextHandlerID    uint64
	acceptingUpdates atomic.Bool
	inFlight         sync.WaitGroup
	cmdWG            sync.WaitGroup
	cmdSem           chan struct{}
	runningCommands  atomic.Int64
	totalCommands    atomic.Int64
	mu               sync.RWMutex

	peerQueue      chan peerUpdateJob
	peerWG         sync.WaitGroup
	peerStopOnce   sync.Once
	peerDropped    atomic.Int64
	peerEnqueued   atomic.Int64
	peerSaveFailed atomic.Int64
	stopping       atomic.Bool
}

// DispatcherDeps specifies dependencies for initializing a Dispatcher via dependency injection.
type DispatcherDeps struct {
	Router         *core.Router
	Permissions    *core.Permissions
	Service        core.TelegramServicer
	Logger         *zap.Logger
	EventBus       *core.EventBus
	Localizer      core.Localizer
	CallbackRouter *callback.Router
	InlineEngine   *inline.Engine
	Resolver       core.PeerResolver
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
	if deps.EventBus != nil {
		d.SetEventBus(deps.EventBus)
	}
	if deps.Localizer != nil {
		d.SetLocalizer(deps.Localizer)
	}
	if deps.CallbackRouter != nil {
		d.SetCallbackRouter(deps.CallbackRouter)
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
		router:      router,
		perms:       perms,
		svc:         svc,
		logger:      logger,
		cooldown:    cooldown,
		executor:    executor,
		albumBuffer: core.NewAlbumBuffer(10 * time.Minute),
		cmdSem:      make(chan struct{}, 32),
	}
	d.acceptingUpdates.Store(true)
	return d
}
