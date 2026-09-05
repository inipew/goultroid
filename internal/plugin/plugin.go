package plugin

import "github.com/inipew/goultroid/internal/core"

// Plugin is the standard interface that all userbot plugins must implement.
type Plugin interface {
	// Name returns the unique identifier for the plugin.
	Name() string
	// Commands returns the list of commands provided by this plugin.
	Commands() []core.Command
	// Init is called when the plugin is registered.
	Init() error
}

// Shutdowner is an optional interface that plugins can implement
// if they need to release resources or close connections on exit.
type Shutdowner interface {
	Shutdown() error
}
