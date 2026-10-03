package blacklist

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/tasks"
)

const blacklistDeleteEffectTimeout = 15 * time.Second

var _ plugin.MessageEventRegistrationsPlugin = (*Plugin)(nil)
var _ plugin.PluginContextInitializer = (*Plugin)(nil)

type blacklistDeleteEffect struct {
	ChatID    int64
	MessageID int
	Peer      core.PeerRef
}

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if pctx == nil {
		return fmt.Errorf("blacklist: plugin context is nil")
	}
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("blacklist: initialize task client: %w", err)
	}
	p.taskMu.Lock()
	p.tasks = client
	p.taskMu.Unlock()
	return p.InitContext(pctx)
}

func (p *Plugin) getTaskClient() tasks.Client {
	p.taskMu.RLock()
	defer p.taskMu.RUnlock()
	return p.tasks
}

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
			FailurePolicy: core.MessageHookFailClosed,
			Ordering:      core.MessageHookOrderingChat,
		},
		Handler: p.handleBlacklistDecision,
	}}
}

func (p *Plugin) handleBlacklistDecision(ctx context.Context, message *core.MessageEnvelope) error {
	if message == nil || message.ChatID == 0 {
		return nil
	}
	matched, err := p.matchMessage(ctx, message)
	if err != nil {
		return err
	}
	if !matched {
		return nil
	}

	if err := p.submitDeleteEffect(ctx, blacklistDeleteEffect{
		ChatID:    message.ChatID,
		MessageID: message.ID,
		Peer:      message.Peer,
	}); err != nil {
		return err
	}

	if decision := core.GetMessageDecision(ctx); decision != nil {
		decision.SetHandled(true)
		decision.SetSuppressAutomation(true)
		decision.SetSuppressAFK(true)
		decision.SetSuppressFilters(true)
		decision.SetSuppressCommands(true)
	}
	return core.ErrInterceptHandled
}

func (p *Plugin) submitDeleteEffect(ctx context.Context, effect blacklistDeleteEffect) error {
	client := p.getTaskClient()
	if client == nil {
		return fmt.Errorf("blacklist: TaskEngine client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	spec := tasks.WorkSpec{
		ID: tasks.TaskID(fmt.Sprintf(
			"blacklist:delete:%d:%d:%d",
			effect.ChatID,
			effect.MessageID,
			p.effectSeq.Add(1),
		)),
		QuotaOwner:       tasks.OwnerID("plugin:blacklist"),
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("blacklist-effect:chat:%d", effect.ChatID),
		ExecutionTimeout: blacklistDeleteEffectTimeout,
		Input: fmt.Sprintf(
			"%d:%d:%d:%d:%d",
			effect.ChatID,
			effect.MessageID,
			effect.Peer.Kind,
			effect.Peer.ID,
			effect.Peer.AccessHash,
		),
		Handler: func(taskCtx context.Context) error {
			return p.applyDeleteEffect(taskCtx, effect)
		},
	}
	if _, err := client.Submit(ctx, spec); err != nil {
		return fmt.Errorf("blacklist: submit delete effect: %w", err)
	}
	return nil
}

func (p *Plugin) applyDeleteEffect(ctx context.Context, effect blacklistDeleteEffect) error {
	if p.svcFunc == nil {
		return fmt.Errorf("%w: blacklist delete transport unavailable", core.ErrUnavailable)
	}
	svc := p.svcFunc()
	if svc == nil {
		return fmt.Errorf("%w: blacklist delete transport unavailable", core.ErrUnavailable)
	}
	peer, err := effect.Peer.InputPeer()
	if err != nil {
		return fmt.Errorf(
			"blacklist: cannot resolve peer for chat %d; message %d was not deleted: %w",
			effect.ChatID,
			effect.MessageID,
			err,
		)
	}
	if err := svc.DeleteMessage(ctx, peer, []int{effect.MessageID}); err != nil {
		return fmt.Errorf("blacklist: failed to delete message %d: %w", effect.MessageID, err)
	}
	return nil
}
