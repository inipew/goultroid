package core

import (
	"time"

	"github.com/inipew/goultroid/internal/execution"
)

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
	Surfaces    execution.SurfaceMask
	GroupOnly   bool
	PrivateOnly bool
	ReplyOnly   bool
	Cooldown    time.Duration
	Timeout     time.Duration
	Handler     CommandHandler
}

// IsAvailableOn reports whether this command is enabled on the specified execution source.
// If Surfaces is 0 (unspecified), it defaults to SurfaceUserbot for backward compatibility.
func (c Command) IsAvailableOn(s execution.Source) bool {
	mask := c.Surfaces
	if mask == 0 {
		mask = execution.SurfaceUserbot
	}
	return mask.Supports(s)
}
