package telegram

import (
	"context"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func messageHookFacts(ctx context.Context, message *core.MessageEnvelope) core.MessageHookFacts {
	facts := core.MessageHookFacts{Origin: core.ExecutionInteractive}
	if message != nil {
		facts.ChatID = message.ChatID
		facts.Outgoing = message.Outgoing
		facts.IsCommand = message.IsCommand
		facts.CommandName = message.CommandName
	}
	if decision := core.GetMessageDecision(ctx); decision != nil {
		facts.Origin = decision.Origin()
	}
	return facts
}

func (d *Dispatcher) messageHookFastInterested(
	registered prioritizedHandler,
	facts core.MessageHookFacts,
) (interested bool) {
	if registered.fastGate != nil {
		interested = true
		func() {
			defer func() {
				if r := recover(); r != nil {
					d.logger.Warn("message hook fast gate panicked; failing open",
						zap.Uint64("handler_id", registered.id),
						zap.Any("panic", r),
					)
					interested = true
				}
			}()
			interested = registered.fastGate(facts)
		}()
		if !interested {
			return false
		}
	}
	return d.messageHookStateInterested(registered, facts.ChatID)
}
