package client

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/tasks"
)

const relayExecutionTimeout = 15 * time.Second

var relayScope = tasks.ScopeIdentity{Owner: "service:pmrelay", Generation: 1}

type relayMessageIngress interface {
	tryOwnerReply(context.Context, pmrelay.IngressMessage) (bool, error)
	tryVisitor(context.Context, pmrelay.IngressMessage) (bool, error)
}

// RelayIngress is the Assistant transport admission bridge for PM Relay.
// Prepare is intentionally read-only; all mutable relay state is revalidated
// inside the TaskEngine handler through pmrelay.Ingress.RevalidatePrepared.
type RelayIngress struct {
	relay             pmrelay.Ingress
	tasks             tasks.Client
	visitorTransport  pmrelay.VisitorTransport
	ownerTransport    pmrelay.OwnerTransport
	guidanceTransport forceSubGuidanceTransport
	forceSub          forceSubMembershipGate
	seq               atomic.Uint64
}

func NewRelayIngress(relay pmrelay.Ingress, taskClient tasks.Client, visitorTransport ...pmrelay.VisitorTransport) *RelayIngress {
	if relay == nil {
		return nil
	}
	var transport pmrelay.VisitorTransport
	var ownerTransport pmrelay.OwnerTransport
	var guidanceTransport forceSubGuidanceTransport
	if len(visitorTransport) > 0 {
		transport = visitorTransport[0]
		ownerTransport, _ = transport.(pmrelay.OwnerTransport)
		guidanceTransport, _ = transport.(forceSubGuidanceTransport)
	}
	return &RelayIngress{
		relay:             relay,
		tasks:             taskClient,
		visitorTransport:  transport,
		ownerTransport:    ownerTransport,
		guidanceTransport: guidanceTransport,
	}
}

func (r *RelayIngress) setForceSubGate(gate forceSubMembershipGate) {
	if r != nil {
		r.forceSub = gate
	}
}

type telegramRelayVisitorTransport struct {
	resolver    peer.Resolver
	interaction *interaction.ClientInteraction
}

func newTelegramRelayVisitorTransport(resolver peer.Resolver, inter *interaction.ClientInteraction) pmrelay.VisitorTransport {
	if resolver == nil || inter == nil {
		return nil
	}
	return &telegramRelayVisitorTransport{resolver: resolver, interaction: inter}
}

func (t *telegramRelayVisitorTransport) resolveUser(ctx context.Context, userID int64) (tg.InputPeerClass, error) {
	if t == nil || t.resolver == nil || userID <= 0 {
		return nil, pmrelay.ErrUnavailable
	}
	return t.resolver.Resolve(ctx, &tg.PeerUser{UserID: userID}, userID, tg.Entities{})
}

func (t *telegramRelayVisitorTransport) ForwardVisitor(ctx context.Context, request pmrelay.VisitorForward) (int, error) {
	if t == nil || t.interaction == nil || request.SourceChatID <= 0 ||
		request.SourceMessageID <= 0 || request.TargetChatID <= 0 || request.RandomID == 0 {
		return 0, pmrelay.ErrUnavailable
	}

	fromPeer, err := t.resolveUser(ctx, request.SourceChatID)
	if err != nil {
		return 0, fmt.Errorf("resolve relay source visitor: %w", err)
	}
	toPeer, err := t.resolveUser(ctx, request.TargetChatID)
	if err != nil {
		return 0, fmt.Errorf("resolve relay owner target: %w", err)
	}

	for attempt := 0; attempt <= interaction.MaxPeerRecoveryAttempts; attempt++ {
		msg, forwardErr := t.interaction.ForwardMessageWithRandomID(
			ctx,
			fromPeer,
			toPeer,
			request.SourceMessageID,
			request.RandomID,
		)
		if forwardErr == nil {
			return msg.ID, nil
		}
		if !errors.Is(forwardErr, interaction.ErrAccessHashStale) || attempt >= interaction.MaxPeerRecoveryAttempts {
			return 0, forwardErr
		}

		t.resolver.InvalidatePeer(fromPeer)
		t.resolver.InvalidatePeer(toPeer)
		fromPeer, err = t.resolveUser(ctx, request.SourceChatID)
		if err != nil {
			return 0, fmt.Errorf("re-resolve relay source visitor: %w", err)
		}
		toPeer, err = t.resolveUser(ctx, request.TargetChatID)
		if err != nil {
			return 0, fmt.Errorf("re-resolve relay owner target: %w", err)
		}
	}
	return 0, interaction.ErrAccessHashStale
}

func (t *telegramRelayVisitorTransport) SendOwnerReply(ctx context.Context, request pmrelay.OwnerSend) (int, error) {
	if t == nil || t.interaction == nil || request.SourceChatID <= 0 ||
		request.SourceMessageID <= 0 || request.TargetChatID <= 0 || request.RandomID == 0 {
		return 0, pmrelay.ErrUnavailable
	}

	sourcePeer, err := t.resolveUser(ctx, request.SourceChatID)
	if err != nil {
		return 0, fmt.Errorf("resolve relay owner source: %w", err)
	}
	targetPeer, err := t.resolveUser(ctx, request.TargetChatID)
	if err != nil {
		return 0, fmt.Errorf("resolve relay visitor target: %w", err)
	}

	for attempt := 0; attempt <= interaction.MaxPeerRecoveryAttempts; attempt++ {
		msg, sendErr := t.interaction.CopyMessageWithRandomID(
			ctx,
			interaction.NewMessageTarget(sourcePeer, request.SourceMessageID, request.SourceChatID, 0),
			targetPeer,
			request.RandomID,
		)
		if sendErr == nil {
			return msg.ID, nil
		}
		if errors.Is(sendErr, core.ErrUnsupported) {
			return 0, pmrelay.ErrUnsupportedDelivery
		}
		if !errors.Is(sendErr, interaction.ErrAccessHashStale) || attempt >= interaction.MaxPeerRecoveryAttempts {
			return 0, sendErr
		}

		t.resolver.InvalidatePeer(sourcePeer)
		t.resolver.InvalidatePeer(targetPeer)
		sourcePeer, err = t.resolveUser(ctx, request.SourceChatID)
		if err != nil {
			return 0, fmt.Errorf("re-resolve relay owner source: %w", err)
		}
		targetPeer, err = t.resolveUser(ctx, request.TargetChatID)
		if err != nil {
			return 0, fmt.Errorf("re-resolve relay visitor target: %w", err)
		}
	}
	return 0, interaction.ErrAccessHashStale
}

func (r *RelayIngress) tryOwnerReply(ctx context.Context, message pmrelay.IngressMessage) (bool, error) {
	if r == nil || r.relay == nil {
		return false, nil
	}
	prepared, handled, err := r.relay.PrepareOwnerReply(ctx, message)
	if err != nil || !handled {
		return handled, err
	}
	return true, r.submit(ctx, prepared)
}

func (r *RelayIngress) tryVisitor(ctx context.Context, message pmrelay.IngressMessage) (bool, error) {
	if r == nil || r.relay == nil {
		return false, nil
	}
	prepared, handled, err := r.relay.PrepareVisitor(ctx, message)
	if err != nil || !handled {
		return handled, err
	}
	if r.forceSub != nil {
		decision, gateErr := r.forceSub.Check(ctx, prepared.VisitorUserID())
		if gateErr != nil || !decision.Allowed {
			var guidanceErr error
			if decision.JoinRequired || decision.VerificationBlocked {
				guidanceErr = r.submitForceSubGuidance(ctx, prepared.VisitorUserID(), decision)
			}
			switch {
			case gateErr != nil && guidanceErr != nil:
				return true, errors.Join(gateErr, guidanceErr)
			case gateErr != nil:
				return true, gateErr
			default:
				return true, guidanceErr
			}
		}
	}
	return true, r.submit(ctx, prepared)
}

func (r *RelayIngress) submitForceSubGuidance(
	ctx context.Context,
	visitorID int64,
	decision forceSubDecision,
) error {
	if r == nil || r.tasks == nil || r.guidanceTransport == nil || visitorID <= 0 {
		return pmrelay.ErrUnavailable
	}
	if limiter, ok := r.forceSub.(forceSubGuidanceLimiter); ok &&
		!limiter.GuidanceDue(visitorID, decision.Config.Revision) {
		return nil
	}
	sequence := r.seq.Add(1)
	_, err := r.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               tasks.TaskID(fmt.Sprintf("asst:relay:forcesub-guidance:%d:%d", visitorID, sequence)),
		Scope:            relayScope,
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("pmrelay:guidance:%d", visitorID)),
		Pool:             tasks.PoolID("interactive"),
		Class:            tasks.PriorityInteractive,
		OrderingKey:      fmt.Sprintf("pmrelay:thread:%d", visitorID),
		ExecutionTimeout: 10 * time.Second,
		Handler: func(taskCtx context.Context) error {
			current, currentErr := r.forceSub.Check(taskCtx, visitorID)
			if current.Allowed {
				return nil
			}
			if !current.JoinRequired && !current.VerificationBlocked {
				return currentErr
			}
			if limiter, ok := r.forceSub.(forceSubGuidanceLimiter); ok &&
				!limiter.ClaimGuidance(visitorID, current.Config.Revision) {
				return nil
			}
			return r.guidanceTransport.SendForceSubGuidance(
				taskCtx,
				visitorID,
				current.Config,
				current.VerificationBlocked,
			)
		},
	})
	if err != nil {
		return fmt.Errorf("assistant force-sub guidance admission failed: %w", err)
	}
	return nil
}

func sendForceSubGuidance(
	ctx context.Context,
	gate forceSubMembershipGate,
	transport forceSubGuidanceTransport,
	visitorID int64,
	decision forceSubDecision,
) {
	if transport == nil || (!decision.JoinRequired && !decision.VerificationBlocked) {
		return
	}
	if limiter, ok := gate.(forceSubGuidanceLimiter); ok &&
		!limiter.ClaimGuidance(visitorID, decision.Config.Revision) {
		return
	}
	_ = transport.SendForceSubGuidance(
		ctx,
		visitorID,
		decision.Config,
		decision.VerificationBlocked,
	)
}

type forceSubGuardedVisitorTransport struct {
	base     pmrelay.VisitorTransport
	gate     forceSubMembershipGate
	guidance forceSubGuidanceTransport
	visitor  int64
}

func (t forceSubGuardedVisitorTransport) ForwardVisitor(
	ctx context.Context,
	request pmrelay.VisitorForward,
) (int, error) {
	if t.base == nil || t.gate == nil {
		return 0, pmrelay.ErrUnavailable
	}
	decision, err := t.gate.Check(ctx, t.visitor)
	if err != nil || !decision.Allowed {
		sendForceSubGuidance(ctx, t.gate, t.guidance, t.visitor, decision)
		if err != nil {
			return 0, err
		}
		return 0, pmrelay.ErrForceSubRequired
	}
	return t.base.ForwardVisitor(ctx, request)
}

func (r *RelayIngress) submit(ctx context.Context, prepared pmrelay.PreparedIngress) error {
	if r == nil || r.relay == nil || r.tasks == nil {
		return pmrelay.ErrUnavailable
	}
	visitorID := prepared.VisitorUserID()
	if visitorID <= 0 || prepared.SourceChatID() <= 0 || prepared.SourceMessageID() <= 0 {
		return pmrelay.ErrPreparedStale
	}

	quotaOwner := tasks.OwnerID(fmt.Sprintf("pmrelay:visitor:%d", visitorID))
	if prepared.Direction() == pmrelay.DeliveryOwnerToVisitor {
		quotaOwner = tasks.OwnerID(fmt.Sprintf("pmrelay:owner-reply:%d", visitorID))
	}

	sequence := r.seq.Add(1)
	_, err := r.tasks.Submit(ctx, tasks.WorkSpec{
		ID: tasks.TaskID(fmt.Sprintf(
			"asst:relay:%s:%d:%d:%d",
			prepared.Direction(),
			prepared.SourceChatID(),
			prepared.SourceMessageID(),
			sequence,
		)),
		Scope:            relayScope,
		QuotaOwner:       quotaOwner,
		Pool:             tasks.PoolID("interactive"),
		Class:            tasks.PriorityInteractive,
		OrderingKey:      fmt.Sprintf("pmrelay:thread:%d", visitorID),
		ExecutionTimeout: relayExecutionTimeout,
		Handler: func(taskCtx context.Context) error {
			if prepared.Direction() == pmrelay.DeliveryVisitorToOwner && r.visitorTransport != nil {
				executor, ok := r.relay.(pmrelay.VisitorExecutor)
				if !ok {
					return pmrelay.ErrUnavailable
				}
				transport := r.visitorTransport
				if r.forceSub != nil {
					decision, gateErr := r.forceSub.Check(taskCtx, visitorID)
					if gateErr != nil || !decision.Allowed {
						sendForceSubGuidance(
							taskCtx,
							r.forceSub,
							r.guidanceTransport,
							visitorID,
							decision,
						)
						if gateErr != nil {
							return gateErr
						}
						return pmrelay.ErrForceSubRequired
					}
					transport = forceSubGuardedVisitorTransport{
						base:     r.visitorTransport,
						gate:     r.forceSub,
						guidance: r.guidanceTransport,
						visitor:  visitorID,
					}
				}
				return executor.ExecuteVisitor(taskCtx, prepared, transport)
			}
			if prepared.Direction() == pmrelay.DeliveryOwnerToVisitor && r.ownerTransport != nil {
				executor, ok := r.relay.(pmrelay.OwnerExecutor)
				if !ok {
					return pmrelay.ErrUnavailable
				}
				return executor.ExecuteOwner(taskCtx, prepared, r.ownerTransport)
			}
			if err := r.relay.RevalidatePrepared(taskCtx, prepared); err != nil {
				return err
			}
			if prepared.Direction() == pmrelay.DeliveryOwnerToVisitor && r.visitorTransport != nil {
				// A partial transport without the P6-D owner port must keep
				// mapped replies claimed/fail-closed.
				return pmrelay.ErrUnsupportedDelivery
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("assistant relay admission failed: %w", err)
	}
	return nil
}

var _ relayMessageIngress = (*RelayIngress)(nil)
