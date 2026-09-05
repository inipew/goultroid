package core

import (
	"fmt"
	"strings"
	"sync"
	"unicode"
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

// Prefix returns the active command prefix.
func (r *Router) Prefix() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.prefix
}

// Register adds a command and its aliases to the router.
// Returns an error if the name or any alias is already registered.
func (r *Router) Register(cmd Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := strings.ToLower(strings.TrimSpace(cmd.Name))
	if name == "" {
		return fmt.Errorf("command name cannot be empty")
	}

	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("command already registered: %s", name)
	}

	for _, alias := range cmd.Aliases {
		aliasLower := strings.ToLower(strings.TrimSpace(alias))
		if aliasLower == "" {
			continue
		}
		if _, exists := r.commands[aliasLower]; exists {
			return fmt.Errorf("command alias already registered: %s", aliasLower)
		}
	}

	// Register canonical name
	r.commands[name] = cmd

	// Register aliases
	for _, alias := range cmd.Aliases {
		aliasLower := strings.ToLower(strings.TrimSpace(alias))
		if aliasLower != "" {
			r.commands[aliasLower] = cmd
		}
	}

	r.all = append(r.all, cmd)
	return nil
}

// Parse checks if a text starts with prefix and parses it into ParsedCommand.
// Returns (parsed, true, nil) if it is a valid command format,
// (nil, false, nil) if not a command,
// or (nil, false, err) if it starts with the command prefix but contains syntax errors.
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

	// Extract raw args (everything after the command word)
	rawArgs := ""
	trimmedRemainder := strings.TrimSpace(afterPrefix[len(tokens[0]):])
	if trimmedRemainder != "" {
		rawArgs = trimmedRemainder
	}

	args := tokens[1:]
	if args == nil {
		args = []string{}
	}

	return &ParsedCommand{
		Name:    cmdName,
		Args:    args,
		RawArgs: rawArgs,
	}, true, nil
}

// Find retrieves a registered Command by name or alias (case-insensitive).
func (r *Router) Find(name string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cmd, exists := r.commands[strings.ToLower(strings.TrimSpace(name))]
	return cmd, exists
}

// All returns a slice of all unique registered commands.
func (r *Router) All() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Command, len(r.all))
	copy(result, r.all)
	return result
}

// tokenize splits a string into whitespace-separated arguments,
// preserving quoted substrings (both single and double quotes) and backslash escapes.
func tokenize(s string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

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
