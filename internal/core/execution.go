package core

import (
	"context"

	"github.com/gotd/td/tg"
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
)

func (s ExecutionSource) String() string {
	switch s {
	case ExecutionScheduled:
		return "scheduled"
	case ExecutionAssistant:
		return "assistant"
	case ExecutionSystem:
		return "system"
	default:
		return "interactive"
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
