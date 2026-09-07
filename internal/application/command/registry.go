package command

import (
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

// UnifiedRegistry manages command registration and surface filtering across GoUltroid.
type UnifiedRegistry struct {
	mu       sync.RWMutex
	commands map[string]core.Command
	all      []core.Command
}

// NewUnifiedRegistry creates an empty UnifiedRegistry.
func NewUnifiedRegistry() *UnifiedRegistry {
	return &UnifiedRegistry{
		commands: make(map[string]core.Command),
		all:      make([]core.Command, 0),
	}
}

// Register adds a command atomically after validation.
func (r *UnifiedRegistry) Register(cmd core.Command) error {
	return r.RegisterBatch([]core.Command{cmd})
}

// RegisterBatch registers a slice of commands after conflict validation.
func (r *UnifiedRegistry) RegisterBatch(cmds []core.Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pending := make(map[string]struct{})
	for _, cmd := range cmds {
		name := strings.ToLower(strings.TrimSpace(cmd.Name))
		if name == "" {
			return fmt.Errorf("command name cannot be empty")
		}
		if _, exists := r.commands[name]; exists {
			return fmt.Errorf("command already registered: %s", name)
		}
		if _, exists := pending[name]; exists {
			return fmt.Errorf("command duplicated in batch: %s", name)
		}
		pending[name] = struct{}{}
		for _, alias := range cmd.Aliases {
			aliasLower := strings.ToLower(strings.TrimSpace(alias))
			if aliasLower == "" {
				continue
			}
			if _, exists := r.commands[aliasLower]; exists {
				return fmt.Errorf("command alias already registered: %s", aliasLower)
			}
			if _, exists := pending[aliasLower]; exists {
				return fmt.Errorf("command alias duplicated in batch: %s", aliasLower)
			}
			pending[aliasLower] = struct{}{}
		}
	}

	for _, cmd := range cmds {
		name := strings.ToLower(strings.TrimSpace(cmd.Name))
		r.commands[name] = cmd
		for _, alias := range cmd.Aliases {
			aliasLower := strings.ToLower(strings.TrimSpace(alias))
			if aliasLower != "" {
				r.commands[aliasLower] = cmd
			}
		}
		r.all = append(r.all, cmd)
	}
	return nil
}

// FindForSurface retrieves a command by name only if supported on the given source surface.
func (r *UnifiedRegistry) FindForSurface(name string, source execution.Source) (core.Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cmd, exists := r.commands[strings.ToLower(strings.TrimSpace(name))]
	if !exists {
		return core.Command{}, false
	}
	if !cmd.IsAvailableOn(source) {
		return core.Command{}, false
	}
	return cmd, true
}

// CommandsForSurface returns all registered commands supported on the specified surface.
func (r *UnifiedRegistry) CommandsForSurface(source execution.Source) []core.Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []core.Command
	for _, cmd := range r.all {
		if cmd.IsAvailableOn(source) {
			result = append(result, cmd)
		}
	}
	return result
}

// All returns all registered commands.
func (r *UnifiedRegistry) All() []core.Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]core.Command, len(r.all))
	copy(result, r.all)
	return result
}
