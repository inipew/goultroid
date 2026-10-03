package filters

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
)

var _ plugin.MessageEventRegistrationsPlugin = (*Plugin)(nil)

func (p *Plugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return []core.MessageHookRegistration{{
		Priority: p.MessageHookPriority(),
		Routing: core.MessageHookRouting{
			Lane: core.MessageHookDecision,
			Interests: []core.MessageHookInterest{{
				Directions:  core.MessageDirectionIncoming,
				Peers:       core.MessagePeerStable,
				Commands:    core.MessagePlain,
				RequireText: true,
			}},
		},
		StateGate: p.MessageHookInterested,
		Execution: core.MessageHookExecutionPolicy{
			FailurePolicy: core.MessageHookFailOpen,
			Ordering:      core.MessageHookOrderingChat,
		},
		Handler: p.HandleMessageEvent,
	}}
}
