package broadcast

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type TargetType string

const (
	TargetAll      TargetType = "all"
	TargetUsers    TargetType = "users"
	TargetGroups   TargetType = "groups"
	TargetChannels TargetType = "channels"
)

type BroadcastReport struct {
	Total       int           `json:"total"`
	Sent        int           `json:"sent"`
	Failed      int           `json:"failed"`
	RateLimited int           `json:"rate_limited"`
	Canceled    bool          `json:"canceled"`
	Duration    time.Duration `json:"duration"`
}

type ProgressCallback func(report BroadcastReport)

type BroadcastRequest struct {
	Targets []tg.InputPeerClass
	Text    string

	// Delay is retained for API compatibility. Physical pacing belongs to the
	// shared Telegram RPC limiter; Broadcast never sleeps a worker between
	// targets.
	Delay time.Duration

	Progress ProgressCallback
}

type Service struct {
	svc     core.TelegramServicer
	svcFunc func() core.TelegramServicer
	logger  *zap.Logger
	tasks   tasks.Client
	runSeq  atomic.Uint64

	cancel context.CancelFunc
	scope  tasks.ScopeIdentity
	mu     sync.Mutex
}

func NewService(svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{logger: logger}
	switch v := svc.(type) {
	case core.TelegramServicer:
		s.svc = v
	case func() core.TelegramServicer:
		s.svcFunc = v
	}
	return s
}

func (s *Service) SetTasks(client tasks.Client) {
	s.mu.Lock()
	s.tasks = client
	s.mu.Unlock()
}

func (s *Service) getService() core.TelegramServicer {
	if s.svc != nil {
		return s.svc
	}
	if s.svcFunc != nil {
		return s.svcFunc()
	}
	return nil
}

// CancelActive aborts the single active broadcast job, including queued/running
// TaskEngine children in its scope.
func (s *Service) CancelActive() bool {
	s.mu.Lock()
	if s.cancel == nil {
		s.mu.Unlock()
		return false
	}
	cancel := s.cancel
	scope := s.scope
	client := s.tasks
	s.cancel = nil
	s.scope = tasks.ScopeIdentity{}
	s.mu.Unlock()

	cancel()
	if client != nil && scope.Owner != "" {
		client.CancelScope(scope, tasks.CauseUserCancel)
	}
	return true
}

type pendingTarget struct {
	ticket  tasks.Ticket
	sendErr *error
}

func broadcastOrderingKey(peer tg.InputPeerClass) string {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return fmt.Sprintf("broadcast:user:%d", p.UserID)
	case *tg.InputPeerChat:
		return fmt.Sprintf("broadcast:chat:%d", p.ChatID)
	case *tg.InputPeerChannel:
		return fmt.Sprintf("broadcast:channel:%d", p.ChannelID)
	case *tg.InputPeerSelf:
		return "broadcast:self"
	default:
		return "broadcast:unknown"
	}
}

func (s *Service) Broadcast(ctx context.Context, req BroadcastRequest) (*BroadcastReport, error) {
	svc := s.getService()
	if svc == nil {
		return nil, fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	if len(req.Targets) == 0 {
		return nil, fmt.Errorf("%w: no targets provided for broadcast", core.ErrInvalidArgs)
	}
	if req.Text == "" {
		return nil, fmt.Errorf("%w: message text cannot be empty", core.ErrInvalidArgs)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: broadcast context is nil", core.ErrInvalidArgs)
	}

	runID := s.runSeq.Add(1)
	scope := tasks.ScopeIdentity{
		Owner:      tasks.OwnerID("service:broadcast"),
		Generation: runID,
	}
	runCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: another broadcast is already running", core.ErrConflict)
	}
	client := s.tasks
	if client == nil {
		s.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: broadcast requires task engine", core.ErrUnavailable)
	}
	s.cancel = cancel
	s.scope = scope
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.scope == scope {
			s.cancel = nil
			s.scope = tasks.ScopeIdentity{}
		}
		s.mu.Unlock()
		cancel()
	}()

	start := time.Now()
	report := BroadcastReport{Total: len(req.Targets)}
	pending := make([]pendingTarget, 0, len(req.Targets))

	for i, target := range req.Targets {
		if err := runCtx.Err(); err != nil {
			report.Canceled = true
			report.Duration = time.Since(start)
			return &report, err
		}

		target := target
		var sendErr error
		ticket, err := client.Submit(runCtx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("broadcast:%d:%d", runID, i)),
			Scope:            scope,
			QuotaOwner:       tasks.OwnerID("system:broadcast"),
			Pool:             tasks.PoolID("general"),
			Class:            tasks.PriorityBackground,
			OrderingKey:      broadcastOrderingKey(target),
			ExecutionTimeout: 30 * time.Second,
			Handler: func(taskCtx context.Context) error {
				_, sendErr = svc.SendMessage(taskCtx, target, req.Text)
				return sendErr
			},
		})
		if err != nil {
			report.Failed++
			if req.Progress != nil {
				req.Progress(report)
			}
			continue
		}
		pending = append(pending, pendingTarget{ticket: ticket, sendErr: &sendErr})
	}

	for _, item := range pending {
		res, waitErr := item.ticket.Wait(runCtx)
		if waitErr != nil {
			report.Canceled = errors.Is(waitErr, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled)
			report.Duration = time.Since(start)
			return &report, waitErr
		}

		err := *item.sendErr
		if res.Outcome == tasks.OutcomeCompleted && err == nil {
			report.Sent++
		} else {
			report.Failed++
			var rateErr *core.RateLimitError
			if errors.As(err, &rateErr) {
				report.RateLimited++
				s.logger.Info("broadcast target deferred by Telegram rate limit",
					zap.Duration("retry_after", rateErr.RateLimitWait()),
				)
			} else if err != nil {
				s.logger.Debug("broadcast message error", zap.Error(err))
			}
		}
		if req.Progress != nil {
			req.Progress(report)
		}
	}

	report.Duration = time.Since(start)
	return &report, nil
}
