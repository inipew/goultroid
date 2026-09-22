package client

import (
	"context"
	"fmt"
	"time"

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
// inside the TaskEngine handler through pmrelay.Ingress.ExecutePrepared.
type RelayIngress struct {
	relay pmrelay.Ingress
	tasks tasks.Client
}

func NewRelayIngress(relay pmrelay.Ingress, taskClient tasks.Client) *RelayIngress {
	if relay == nil {
		return nil
	}
	return &RelayIngress{relay: relay, tasks: taskClient}
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

	_, err := r.tasks.Submit(ctx, tasks.WorkSpec{
		ID: tasks.TaskID(fmt.Sprintf(
			"asst:relay:%s:%d:%d",
			prepared.Direction(),
			prepared.SourceChatID(),
			prepared.SourceMessageID(),
		)),
		Scope:            relayScope,
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("pmrelay:visitor:%d", visitorID)),
		Pool:             tasks.PoolID("interactive"),
		Class:            tasks.PriorityInteractive,
		OrderingKey:      fmt.Sprintf("pmrelay:thread:%d", visitorID),
		ExecutionTimeout: relayExecutionTimeout,
		Handler: func(taskCtx context.Context) error {
			return r.relay.ExecutePrepared(taskCtx, prepared)
		},
	})
	if err != nil {
		return fmt.Errorf("assistant relay admission failed: %w", err)
	}
	return nil
}

var _ relayMessageIngress = (*RelayIngress)(nil)
