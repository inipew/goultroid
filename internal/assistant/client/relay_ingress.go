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
	relay            pmrelay.Ingress
	tasks            tasks.Client
	visitorTransport pmrelay.VisitorTransport
	seq              atomic.Uint64
}

func NewRelayIngress(relay pmrelay.Ingress, taskClient tasks.Client, visitorTransport ...pmrelay.VisitorTransport) *RelayIngress {
	if relay == nil {
		return nil
	}
	var transport pmrelay.VisitorTransport
	if len(visitorTransport) > 0 {
		transport = visitorTransport[0]
	}
	return &RelayIngress{relay: relay, tasks: taskClient, visitorTransport: transport}
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
	return true, r.submit(ctx, prepared)
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
				return executor.ExecuteVisitor(taskCtx, prepared, r.visitorTransport)
			}
			if err := r.relay.RevalidatePrepared(taskCtx, prepared); err != nil {
				return err
			}
			if prepared.Direction() == pmrelay.DeliveryOwnerToVisitor && r.visitorTransport != nil {
				// P6-C activates the visitor delivery plane only. Keep mapped
				// owner replies claimed/fail-closed so they cannot fall into an
				// unrelated AwaitInput while P6-D transport is not installed.
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
