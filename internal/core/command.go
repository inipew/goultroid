package core

import "time"

// Permission represents the access tier required to execute a command.
type Permission int

const (
	// PermissionEveryone allows any user to run the command.
	PermissionEveryone Permission = iota
	// PermissionSudo allows Sudo users and the Owner to run the command.
	PermissionSudo
	// PermissionOwner allows only the Owner to run the command.
	PermissionOwner
)

// String returns the human-readable name of the permission tier.
func (p Permission) String() string {
	switch p {
	case PermissionOwner:
		return "Owner"
	case PermissionSudo:
		return "Sudo"
	default:
		return "Everyone"
	}
}

// CommandHandler is the function signature for command execution.
type CommandHandler func(*Context) error

// Command defines a userbot command and its metadata.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Category    string
	Permission  Permission
	GroupOnly   bool
	PrivateOnly bool
	ReplyOnly   bool
	Cooldown    time.Duration
	Handler     CommandHandler
}
