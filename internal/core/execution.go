package core

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/execution"
)

// ExecutionSource identifies who or what triggered a command execution.
// It is deliberately explicit so scheduled, assistant, and system executions
// do not have to impersonate an incoming Telegram message.
type ExecutionSource uint8

const (
	ExecutionInteractive ExecutionSource = iota
	ExecutionScheduled
	ExecutionAssistant
	ExecutionSystem
	ExecutionAddon
	ExecutionAutomation
)

func (s ExecutionSource) String() string {
	switch s {
	case ExecutionScheduled:
		return "scheduled"
	case ExecutionAssistant:
		return "assistant"
	case ExecutionSystem:
		return "system"
	case ExecutionAddon:
		return "addon"
	case ExecutionAutomation:
		return "automation"
	default:
		return "interactive"
	}
}

// Surface maps this runtime ExecutionSource trigger to its canonical execution surface.
func (s ExecutionSource) Surface() execution.Source {
	switch s {
	case ExecutionAssistant:
		return execution.SourceAssistant
	default:
		return execution.SourceUserbot
	}
}

// CommandExecution is the canonical execution envelope shared by every
// command entry point. TriggerMessage is optional because non-interactive
// sources do not have a real Telegram message that can be edited/replied to.
type CommandExecution struct {
	Ctx            context.Context
	Source         ExecutionSource
	Command        string
	Args           []string
	RawArgs        string
	Principal      *Principal
	Perms          *Permissions
	Chat           *Chat
	Target         *Chat
	TriggerMessage *Message
	Sender         *User
	PeerID         tg.InputPeerClass
	CorrelationID  string
}

// WithContext returns a copy using the supplied context.
func (e CommandExecution) WithContext(ctx context.Context) CommandExecution {
	e.Ctx = ctx
	return e
}
