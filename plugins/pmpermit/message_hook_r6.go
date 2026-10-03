package pmpermit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	pmpermitservice "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/tasks"
)

const pmpermitEffectTimeout = 15 * time.Second

var _ plugin.MessageEventRegistrationsPlugin = (*Plugin)(nil)
var _ plugin.PluginContextInitializer = (*Plugin)(nil)

type pmpermitIncomingEffect struct {
	Peer     core.PeerRef
	Decision pmpermitservice.IncomingPMDecision
}

type pmpermitAutoApproveEffect struct {
	Peer   core.PeerRef
	Effect pmpermitservice.AutoApproveEffect
}

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if pctx == nil {
		return fmt.Errorf("pmpermit: plugin context is nil")
	}
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("pmpermit: initialize task client: %w", err)
	}
	p.taskMu.Lock()
	p.tasks = client
	p.taskMu.Unlock()
	return p.Init()
}

func (p *Plugin) getTaskClient() tasks.Client {
	p.taskMu.RLock()
	defer p.taskMu.RUnlock()
	return p.tasks
}

func (p *Plugin) outgoingFastGate(facts core.MessageHookFacts) bool {
	return facts.Origin != core.ExecutionAutomation && !facts.IsCommand
}

func (p *Plugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return []core.MessageHookRegistration{
		{
			Priority: p.MessageHookPriority(),
			Routing: core.MessageHookRouting{
				Lane: core.MessageHookDecision,
				Interests: []core.MessageHookInterest{{
					Directions: core.MessageDirectionIncoming,
					Peers:      core.MessagePeerPrivate,
				}},
			},
			StateGate: p.MessageHookInterested,
			Execution: core.MessageHookExecutionPolicy{
				FailurePolicy: core.MessageHookFailClosed,
				Ordering:      core.MessageHookOrderingChat,
			},
			Handler: p.handleIncomingPMDecision,
		},
		{
			Priority: p.MessageHookPriority(),
			Routing: core.MessageHookRouting{
				Lane: core.MessageHookDecision,
				Interests: []core.MessageHookInterest{{
					Directions: core.MessageDirectionOutgoing,
					Peers:      core.MessagePeerPrivate,
					Commands:   core.MessagePlain,
				}},
			},
			FastGate:  p.outgoingFastGate,
			StateGate: p.MessageHookInterested,
			Execution: core.MessageHookExecutionPolicy{
				FailurePolicy: core.MessageHookFailOpen,
				Ordering:      core.MessageHookOrderingChat,
			},
			Handler: p.handleOutgoingPMDecision,
		},
	}
}

func (p *Plugin) handleIncomingPMDecision(ctx context.Context, message *core.MessageEnvelope) error {
	if p.svc == nil || message == nil || !message.IsPrivate() {
		return nil
	}
	senderID := message.Sender.ID
	if senderID == 0 {
		senderID = message.ChatID
	}
	if senderID == 0 {
		return nil
	}

	decisionState, err := p.svc.DecideIncomingPM(ctx, senderID, pmpermitservice.PMActor{
		UserID:   senderID,
		IsBot:    message.Sender.IsBot,
		Verified: message.SenderVerified,
		IsSelf:   message.SenderSelf,
	})
	if err != nil {
		return err
	}
	if !decisionState.Handled {
		return nil
	}

	if message.IsCommand {
		core.MarkMessageHandled(senderID, message.ID)
	}
	if decision := core.GetMessageDecision(ctx); decision != nil {
		decision.SetHandled(true)
		decision.SetSuppressAutomation(true)
		decision.SetSuppressAFK(true)
		decision.SetSuppressFilters(true)
		decision.SetSuppressCommands(true)
	}

	if decisionState.Effect != pmpermitservice.IncomingPMEffectNone {
		if err := p.submitIncomingEffect(ctx, pmpermitIncomingEffect{
			Peer:     message.Peer,
			Decision: decisionState,
		}); err != nil {
			return err
		}
	}
	return core.ErrInterceptHandled
}

func (p *Plugin) handleOutgoingPMDecision(ctx context.Context, message *core.MessageEnvelope) error {
	if p.svc == nil || message == nil || !message.Outgoing || !message.IsPrivate() {
		return nil
	}
	if message.IsCommand || message.CommandName != "" {
		return nil
	}
	if p.svc.IsPMPermitMessage(message.Text) || p.svc.IsBotSent(message.ID) {
		return nil
	}
	targetID := message.ChatID
	if targetID == 0 || targetID == p.svc.OwnerID() || p.svc.IsSudoID(targetID) {
		return nil
	}
	if p.svc.IsWarnID(targetID, message.ID) {
		return nil
	}

	effectState, changed, err := p.svc.PrepareAutoApproveOutgoing(ctx, targetID)
	if err != nil || !changed {
		return err
	}
	return p.submitAutoApproveEffect(ctx, pmpermitAutoApproveEffect{
		Peer:   message.Peer,
		Effect: effectState,
	})
}

func (p *Plugin) submitIncomingEffect(ctx context.Context, effect pmpermitIncomingEffect) error {
	return p.submitEffect(
		ctx,
		fmt.Sprintf("incoming:%d", effect.Decision.UserID),
		effect.Decision.UserID,
		fmt.Sprintf(
			"%d:%d:%d:%d:%d:%d",
			effect.Decision.UserID,
			effect.Decision.Effect,
			effect.Decision.WarnCount,
			effect.Peer.Kind,
			effect.Peer.ID,
			effect.Peer.AccessHash,
		),
		func(taskCtx context.Context) error {
			peer := p.resolveEffectPeer(taskCtx, effect.Peer, effect.Decision.UserID)
			if peer == nil {
				return fmt.Errorf("%w: pmpermit cannot resolve effect peer %d", core.ErrUnavailable, effect.Decision.UserID)
			}
			return p.svc.ApplyIncomingPMEffect(taskCtx, peer, effect.Decision)
		},
	)
}

func (p *Plugin) submitAutoApproveEffect(ctx context.Context, effect pmpermitAutoApproveEffect) error {
	return p.submitEffect(
		ctx,
		fmt.Sprintf("auto-approve:%d", effect.Effect.UserID),
		effect.Effect.UserID,
		fmt.Sprintf(
			"%d:%d:%d:%d:%d",
			effect.Effect.UserID,
			len(effect.Effect.WarnIDs),
			effect.Peer.Kind,
			effect.Peer.ID,
			effect.Peer.AccessHash,
		),
		func(taskCtx context.Context) error {
			peer := p.resolveEffectPeer(taskCtx, effect.Peer, effect.Effect.UserID)
			if peer == nil {
				return fmt.Errorf("%w: pmpermit cannot resolve auto-approve peer %d", core.ErrUnavailable, effect.Effect.UserID)
			}
			return p.svc.ApplyAutoApproveOutgoingEffect(taskCtx, peer, effect.Effect)
		},
	)
}

func (p *Plugin) submitEffect(
	ctx context.Context,
	kind string,
	userID int64,
	input string,
	handler func(context.Context) error,
) error {
	client := p.getTaskClient()
	if client == nil {
		return fmt.Errorf("pmpermit: TaskEngine client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	spec := tasks.WorkSpec{
		ID: tasks.TaskID(fmt.Sprintf(
			"pmpermit:%s:%d",
			kind,
			p.effectSeq.Add(1),
		)),
		QuotaOwner:       tasks.OwnerID("plugin:pmpermit"),
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("pmpermit-effect:user:%d", userID),
		ExecutionTimeout: pmpermitEffectTimeout,
		Input:            input,
		Handler:          handler,
	}
	if _, err := client.Submit(ctx, spec); err != nil {
		return fmt.Errorf("pmpermit: submit %s effect: %w", kind, err)
	}
	return nil
}

func (p *Plugin) resolveEffectPeer(
	ctx context.Context,
	ref core.PeerRef,
	userID int64,
) *tg.InputPeerUser {
	if peer, err := ref.InputPeer(); err == nil {
		if userPeer, ok := peer.(*tg.InputPeerUser); ok && userPeer.AccessHash != 0 {
			return userPeer
		}
	}
	if p.resolver == nil || userID == 0 {
		return nil
	}
	peer, resolvedID, err := p.resolver.ResolveUser(ctx, strconv.FormatInt(userID, 10))
	if err != nil || resolvedID != userID {
		return nil
	}
	userPeer, ok := peer.(*tg.InputPeerUser)
	if !ok || userPeer == nil || userPeer.AccessHash == 0 {
		return nil
	}
	return userPeer
}
