package plugin

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// Plugin is the standard interface that all userbot plugins must implement.
type Plugin interface {
	// Name returns the unique identifier for the plugin.
	Name() string
	// Commands returns the list of commands provided by this plugin.
	Commands() []core.Command
	// Init is called when the plugin is registered.
	Init() error
}

// Shutdowner is the legacy shutdown interface. It is retained for plugins that
// do not need cancellation-aware teardown. Shutdown is invoked synchronously so
// the application never closes shared resources while a legacy plugin is still
// using them.
type Shutdowner interface {
	Shutdown() error
}

// ContextShutdowner is the preferred lifecycle interface for plugins that own
// goroutines, network clients, timers, or other cancellable resources.
type ContextShutdowner interface {
	ShutdownContext(context.Context) error
}

// Metadata describes authorship, version, and details of a plugin.
type Metadata struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
}

// DescribedPlugin is an optional interface that plugins can implement
// to provide rich metadata.
type DescribedPlugin interface {
	Plugin
	Metadata() Metadata
}

// ContextInitializer is an optional interface for plugins requiring cancellation-aware initialization.
type ContextInitializer interface {
	InitContext(context.Context) error
}

// ScopeInitializer is the preferred initialization hook for plugins that own
// background work or other long-lived resources. The supplied scope provides
// cancellation, cleanup registration, and resource tracking.
type ScopeInitializer interface {
	InitScope(context.Context, *Scope) error
}

// PluginContextInitializer is the preferred initialization hook for plugins that utilize
// capability-gated platform runtime services (HTTP, Files, Process, Secrets, Tasks).
type PluginContextInitializer interface {
	InitPlugin(PluginContext) error
}

// MessageEventPlugin is the preferred message hook API. It receives the
// canonical core envelope and never needs raw MTProto update/entity containers.
type MessageEventPlugin interface {
	Plugin
	MessageHookPriority() int
	HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error
}

// MessageEventRoutingPlugin declares structural routing for a canonical hook.
type MessageEventRoutingPlugin interface {
	MessageEventPlugin
	MessageHookRouting() core.MessageHookRouting
}

// MessageEventStatePlugin adds a fast dynamic feature-state gate to a canonical hook.
type MessageEventStatePlugin interface {
	MessageEventRoutingPlugin
	MessageHookInterested(chatID int64) bool
}

// MessageHookPlugin is the privileged legacy/raw Telegram compatibility API.
// Production registration requires the telegram.raw capability.
type MessageHookPlugin interface {
	Plugin
	MessageHookPriority() int
	HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error
}

// MessageHookRoutingPlugin lets a hook declare its execution lane and
// structural message interests. The dispatcher indexes these interests at
// registration time so irrelevant updates never invoke the plugin.
type MessageHookRoutingPlugin interface {
	MessageHookPlugin
	MessageHookRouting() core.MessageHookRouting
}

// MessageHookStatePlugin optionally supplies a lock-free dynamic interest gate
// for chat-scoped feature state. Returning false lets the dispatcher skip the
// hook before TaskEngine admission. Implementations must fail open whenever
// persistent state is not known to be complete.
type MessageHookStatePlugin interface {
	MessageHookRoutingPlugin
	MessageHookInterested(chatID int64) bool
}
