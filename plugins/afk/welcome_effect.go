package afk

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	afkWelcomeEffectTimeout    = 15 * time.Second
	afkWelcomeAdmissionTimeout = 250 * time.Millisecond
)

type afkWelcomeEffect struct {
	ChatID   int64
	Peer     core.PeerRef
	Duration string
}

// submitWelcomeEffect commits only a small immutable value to the existing
// shared TaskEngine. The persisted AFK-off transition is already complete.
// Never fall back to a synchronous send or an unmanaged goroutine on rejection.
func (p *Plugin) submitWelcomeEffect(ctx context.Context, message *core.MessageEnvelope, duration string) error {
	if message == nil {
		return nil
	}
	p.stateMu.RLock()
	client, scope := p.tasks, p.scope
	p.stateMu.RUnlock()
	if client == nil {
		return fmt.Errorf("afk: shared TaskEngine client unavailable")
	}
	if ctx == nil {
		return fmt.Errorf("afk: effect admission context unavailable")
	}
	generation := uint64(0)
	if scope != nil {
		generation = scope.Generation()
	}
	effect := afkWelcomeEffect{ChatID: message.ChatID, Peer: message.Peer, Duration: duration}
	spec := tasks.WorkSpec{
		ID:               tasks.TaskID(fmt.Sprintf("afk:welcome:%d:%d:%d", p.ownerID, generation, p.effectSeq.Add(1))),
		Scope:            tasks.ScopeIdentity{Owner: "plugin:afk", Generation: generation},
		QuotaOwner:       tasks.OwnerID("plugin:afk"),
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("afk-welcome:owner:%d", p.ownerID),
		ExecutionTimeout: afkWelcomeEffectTimeout,
		Input:            effect,
		Handler: func(taskCtx context.Context) error {
			if err := taskCtx.Err(); err != nil {
				return err
			}
			if p.svcFunc == nil {
				return fmt.Errorf("afk: telegram service unavailable for welcome")
			}
			svc := p.svcFunc()
			if svc == nil {
				return fmt.Errorf("afk: telegram service unavailable for welcome")
			}
			envelope := &core.MessageEnvelope{ChatID: effect.ChatID, Peer: effect.Peer}
			return p.sendWelcome(taskCtx, svc, envelope, effect.Duration, scope)
		},
	}
	admissionCtx, cancel := context.WithTimeout(ctx, afkWelcomeAdmissionTimeout)
	defer cancel()
	if _, err := client.Submit(admissionCtx, spec); err != nil {
		return fmt.Errorf("afk: submit welcome effect: %w", err)
	}
	return nil
}

// Used by standalone tests only. Production receives a generation-scoped
// client from PluginContext.TaskClient during managed initialization.
func (p *Plugin) SetTaskClient(client tasks.Client) {
	p.stateMu.Lock()
	p.tasks = client
	p.stateMu.Unlock()
}

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if pctx == nil || pctx.Scope() == nil {
		return fmt.Errorf("afk: managed plugin scope unavailable")
	}
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("afk: initialize scoped TaskEngine client: %w", err)
	}
	// Publishing the provider precedes hook registration. No Telegram update
	// can enter this plugin until the manager finishes initialization.
	p.stateMu.Lock()
	p.scope = pctx.Scope()
	p.tasks = client
	p.stateMu.Unlock()
	return p.loadState(pctx)
}
