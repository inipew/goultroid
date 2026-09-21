package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	corecallback "github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

const (
	assistantCallbackDedupTTL = time.Hour
	assistantCallbackDedupMax = 4096
)

type callbackSeenEntry struct {
	id int64
	at time.Time
}

// callbackQueryDeduper keeps Telegram callback delivery deduplication at the
// transport ingress without reintroducing a second callback router.
type callbackQueryDeduper struct {
	mu    sync.Mutex
	seen  map[int64]time.Time
	order []callbackSeenEntry
	head  int
}

func newCallbackQueryDeduper() *callbackQueryDeduper {
	return &callbackQueryDeduper{seen: make(map[int64]time.Time)}
}

func (d *callbackQueryDeduper) Admit(id int64, now time.Time) bool {
	if d == nil || id == 0 {
		return true
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.pruneExpired(now)
	if at, ok := d.seen[id]; ok && now.Sub(at) < assistantCallbackDedupTTL {
		return false
	}

	for len(d.seen) >= assistantCallbackDedupMax {
		if !d.evictOldest() {
			break
		}
	}

	d.seen[id] = now
	d.order = append(d.order, callbackSeenEntry{id: id, at: now})
	d.compact()
	return true
}

func (d *callbackQueryDeduper) pruneExpired(now time.Time) {
	for d.head < len(d.order) {
		entry := d.order[d.head]
		if now.Sub(entry.at) < assistantCallbackDedupTTL {
			break
		}
		if current, ok := d.seen[entry.id]; ok && current.Equal(entry.at) {
			delete(d.seen, entry.id)
		}
		d.order[d.head] = callbackSeenEntry{}
		d.head++
	}
	d.compact()
}

func (d *callbackQueryDeduper) evictOldest() bool {
	for d.head < len(d.order) {
		entry := d.order[d.head]
		d.order[d.head] = callbackSeenEntry{}
		d.head++
		if current, ok := d.seen[entry.id]; ok && current.Equal(entry.at) {
			delete(d.seen, entry.id)
			d.compact()
			return true
		}
	}
	d.compact()
	return false
}

func (d *callbackQueryDeduper) compact() {
	if d.head == 0 {
		return
	}
	if d.head < 1024 && d.head*2 < len(d.order) {
		return
	}
	copy(d.order, d.order[d.head:])
	d.order = d.order[:len(d.order)-d.head]
	d.head = 0
}

func callbackOrderingKey(evt *core.CallbackQueryEvent) string {
	if evt == nil {
		return ""
	}
	if !evt.IsInline() {
		if evt.ChatID != 0 && evt.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d:%d", evt.ChatID, evt.MsgID)
		}
		if evt.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d", evt.MsgID)
		}
		return fmt.Sprintf("callback:%d", evt.QueryID)
	}
	if evt.Target.InlineID != nil {
		switch id := evt.Target.InlineID.(type) {
		case *tg.InputBotInlineMessageID:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		case *tg.InputBotInlineMessageID64:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		}
	}
	if evt.ChatInstance != 0 {
		return fmt.Sprintf("callback:instance:%d", evt.ChatInstance)
	}
	return fmt.Sprintf("inline_callback:%d", evt.QueryID)
}

func dispatchCoreCallback(
	ctx context.Context,
	dispatcher CoreCallbackDispatcher,
	taskClient tasks.Client,
	scopeResolver func(string) (tasks.ScopeIdentity, bool),
	deduper *callbackQueryDeduper,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	logger *zap.Logger,
) (err error) {
	if evt == nil || svc == nil {
		return core.ErrInternal
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Internal server error.", true)
			logger.Error("assistant: callback ingress panic recovered",
				zap.Int64("query_id", evt.QueryID),
				zap.Any("panic", recovered),
			)
			err = fmt.Errorf("callback ingress panic: %v", recovered)
		}
	}()

	namespace, _, _, parseErr := corecallback.ParseCallbackData(evt.Data)
	if parseErr != nil {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Invalid callback", false)
		return nil
	}
	if deduper != nil && !deduper.Admit(evt.QueryID, time.Now()) {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
		logger.Debug("assistant: duplicate callback query ignored", zap.Int64("query_id", evt.QueryID))
		return nil
	}
	if dispatcher == nil {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Interaction service unavailable.", false)
		return nil
	}
	if !dispatcher.HasHandler(namespace) {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available.", false)
		return corecallback.ErrHandlerNotFound
	}

	scope, available := dispatcher.TaskScope(evt.Data, scopeResolver)
	if !available {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available.", false)
		return fmt.Errorf("%w: feature not available for %s", corecallback.ErrHandlerNotFound, namespace)
	}
	if taskClient == nil {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Interaction service unavailable.", true)
		return ErrCallbackTasksNotConfigured
	}

	taskID := fmt.Sprintf("asst:cb:%d", evt.QueryID)
	if evt.IsInline() {
		taskID = fmt.Sprintf("asst:cb:inline:%d", evt.QueryID)
	}
	doneCh := make(chan error, 1)
	ticket, submitErr := taskClient.Submit(ctx, tasks.WorkSpec{
		ID:               tasks.TaskID(taskID),
		Scope:            scope,
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("telegram:user:%d", evt.UserID)),
		Pool:             "interactive",
		Class:            tasks.PriorityInteractive,
		OrderingKey:      callbackOrderingKey(evt),
		ExecutionTimeout: 15 * time.Second,
		Handler: func(taskCtx context.Context) error {
			dispatchErr := dispatcher.Dispatch(taskCtx, evt, svc)
			doneCh <- dispatchErr
			return dispatchErr
		},
	})
	if submitErr != nil {
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Interaction busy. Please retry.", true)
		return fmt.Errorf("task submission failed: %w", submitErr)
	}

	var ticketDone <-chan struct{}
	if ticket != nil {
		ticketDone = ticket.Done()
	}

	select {
	case dispatchErr := <-doneCh:
		return dispatchErr
	case <-ticketDone:
		select {
		case dispatchErr := <-doneCh:
			return dispatchErr
		default:
		}
		if ticket == nil {
			return nil
		}
		result, _ := ticket.Result()
		if result.IsSuccess() {
			return nil
		}
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Action failed. Please retry.", false)
		if result.Failure.Message != "" {
			return errors.New(result.Failure.Message)
		}
		return fmt.Errorf("task finished with outcome %s (%s)", result.Outcome, result.Cause)
	case <-ctx.Done():
		if ticket != nil {
			_, _ = taskClient.Cancel(ticket.TaskID(), tasks.CauseTimeout)
		}
		return ctx.Err()
	}
}
