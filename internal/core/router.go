package core

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/inipew/goultroid/internal/execution"
)

// ParsedCommand contains the extracted command name and arguments.
type ParsedCommand struct {
	Name    string
	Args    []string
	RawArgs string
}

// Router dispatches incoming message text to registered commands.
type Router struct {
	prefix   string
	commands map[string]Command
	all      []Command
	mu       sync.RWMutex
}

// NewRouter creates a new command Router with the given prefix.
func NewRouter(prefix string) *Router {
	if prefix == "" {
		prefix = "."
	}
	return &Router{
		prefix:   prefix,
		commands: make(map[string]Command),
		all:      make([]Command, 0),
	}
}

func (r *Router) Prefix() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.prefix
}

// Register adds one command atomically.
func (r *Router) Register(cmd Command) error {
	return r.RegisterBatch([]Command{cmd})
}

// RegisterBatch validates the complete batch before mutating the router. A
// conflict therefore leaves the router exactly as it was before the call.
func (r *Router) RegisterBatch(cmds []Command) error {
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

// UnregisterBatch removes commands and aliases previously registered as a
// batch. Entries are removed only when they still point to the same command,
// so a later replacement cannot be accidentally deleted.
func (r *Router) UnregisterBatch(cmds []Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cmd := range cmds {
		name := strings.ToLower(strings.TrimSpace(cmd.Name))
		if current, ok := r.commands[name]; ok && current.Name == cmd.Name {
			delete(r.commands, name)
		}
		for _, alias := range cmd.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias == "" {
				continue
			}
			if current, ok := r.commands[alias]; ok && current.Name == cmd.Name {
				delete(r.commands, alias)
			}
		}
	}
	if len(cmds) == 0 {
		return
	}
	remaining := r.all[:0]
	for _, registered := range r.all {
		remove := false
		for _, cmd := range cmds {
			if registered.Name == cmd.Name {
				remove = true
				break
			}
		}
		if !remove {
			remaining = append(remaining, registered)
		}
	}
	r.all = remaining
}

// Parse checks if a text starts with prefix and parses it into ParsedCommand.
func (r *Router) Parse(text string) (*ParsedCommand, bool, error) {
	r.mu.RLock()
	prefix := r.prefix
	r.mu.RUnlock()
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, prefix) {
		return nil, false, nil
	}
	afterPrefix := strings.TrimSpace(text[len(prefix):])
	if afterPrefix == "" {
		return nil, false, nil
	}
	tokens, err := tokenize(afterPrefix)
	if err != nil {
		return nil, false, err
	}
	if len(tokens) == 0 {
		return nil, false, nil
	}
	cmdName := strings.ToLower(tokens[0])
	rawArgs := ""
	trimmedRemainder := strings.TrimSpace(afterPrefix[len(tokens[0]):])
	if trimmedRemainder != "" {
		rawArgs = trimmedRemainder
	}
	args := tokens[1:]
	if args == nil {
		args = []string{}
	}
	return &ParsedCommand{Name: cmdName, Args: args, RawArgs: rawArgs}, true, nil
}

func (r *Router) Find(name string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cmd, exists := r.commands[strings.ToLower(strings.TrimSpace(name))]
	return cmd, exists
}

func (r *Router) All() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Command, len(r.all))
	copy(result, r.all)
	return result
}

// FindForSurface retrieves a command by name only if supported on the given source surface.
func (r *Router) FindForSurface(name string, source execution.Source) (Command, bool) {
	cmd, exists := r.Find(name)
	if !exists {
		return Command{}, false
	}
	if !cmd.IsAvailableOn(source) {
		return Command{}, false
	}
	return cmd, true
}

// CommandsForSurface returns all registered commands supported on the specified surface.
func (r *Router) CommandsForSurface(source execution.Source) []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Command
	for _, cmd := range r.all {
		if cmd.IsAvailableOn(source) {
			result = append(result, cmd)
		}
	}
	return result
}

// tokenize splits a string into whitespace-separated arguments, preserving
// quoted substrings and backslash escapes.
func tokenize(s string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inSingle, inDouble, escaped := false, false, false
	for _, r := range s {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if unicode.IsSpace(r) && !inSingle && !inDouble {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		return nil, ErrTrailingEscape
	}
	if inSingle || inDouble {
		return nil, ErrUnclosedQuote
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}
