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
