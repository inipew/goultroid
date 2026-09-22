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
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type TargetType string

const (
	TargetAll      TargetType = "all"
	TargetUsers    TargetType = "users"
	TargetGroups   TargetType = "groups"
	TargetChannels TargetType = "channels"

	// Keep only a bounded number of accepted TaskEngine tickets retained by one
	// broadcast. TaskEngine still owns physical concurrency; this window adds
	// producer backpressure so a large target list cannot fill the engine backlog
	// and turn temporary queue saturation into dropped broadcast targets.
	maxBroadcastInFlight = 64
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
	Targets      []tg.InputPeerClass
	TargetSource TargetSource
	Sender       core.TelegramServicer
	Response     savedresponse.Response
	Vars         savedresponse.TemplateVars

	// Text is retained for source compatibility with older callers. New callers
	// should populate Response so media, format, and template semantics remain
	// canonical.
	Text string

	// Delay is retained for API compatibility. Physical pacing belongs to the
	// shared Telegram RPC limiter; Broadcast never sleeps a worker between
	// targets.
	Delay time.Duration

	// Progress receives coalesced snapshots instead of one callback per target.
	// The service always attempts one final snapshot for the terminal state.
	Progress ProgressCallback
}

type Service struct {
	svc       core.TelegramServicer
	svcFunc   func() core.TelegramServicer
	logger    *zap.Logger
	tasks     tasks.Client
	responses *savedresponse.Service
	delivery  *savedresponse.ResponseDelivery
	runSeq    atomic.Uint64

	cancel context.CancelFunc
	scope  tasks.ScopeIdentity
	mu     sync.Mutex
}

func NewService(svc any, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{logger: logger}
	s.SetResponses(savedresponse.NewService(nil))
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

// SetResponses binds the canonical SavedResponse preparation/delivery service.
func (s *Service) SetResponses(responses *savedresponse.Service) {
	if responses == nil {
		responses = savedresponse.NewService(nil)
	}
	s.mu.Lock()
	s.responses = responses
	s.delivery = savedresponse.NewResponseDelivery(responses)
	s.mu.Unlock()
}

func (s *Service) CaptureReply(ctx *core.Context) (savedresponse.Response, error) {
	s.mu.Lock()
	responses := s.responses
	s.mu.Unlock()
	if responses == nil {
		return savedresponse.Response{}, savedresponse.ErrMediaPersistenceUnavailable
	}
	return responses.CaptureReply(ctx)
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
	svc := req.Sender
	if svc == nil {
		svc = s.getService()
	}
	if svc == nil {
		return nil, fmt.Errorf("%w: telegram service is nil", core.ErrInternal)
	}
	if req.TargetSource != nil && len(req.Targets) != 0 {
		return nil, fmt.Errorf("%w: broadcast targets and target source are mutually exclusive", core.ErrInvalidArgs)
	}
	source := req.TargetSource
	if source == nil {
		if len(req.Targets) == 0 {
			return nil, fmt.Errorf("%w: no targets provided for broadcast", core.ErrInvalidArgs)
		}
		source = newSliceTargetSource(req.Targets)
	}
	if source.Total() <= 0 {
		return nil, fmt.Errorf("%w: no targets provided for broadcast", core.ErrInvalidArgs)
	}

	response := req.Response.Clone()
	if response.Empty() && req.Text != "" {
		response = savedresponse.NewText(req.Text)
	}
	if response.Empty() {
		return nil, fmt.Errorf("%w: response cannot be empty", core.ErrInvalidArgs)
	}
	compiled, err := savedresponse.Compile(response)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid saved response: %v", core.ErrInvalidArgs, err)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: broadcast context is nil", core.ErrInvalidArgs)
	}

	runID := s.runSeq.Add(1)
	scope := tasks.ScopeIdentity{
		Owner:      "service:broadcast",
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
	delivery := s.delivery
	if client == nil {
		s.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: broadcast requires task engine", core.ErrUnavailable)
	}
	if delivery == nil {
		s.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: broadcast response delivery is unavailable", core.ErrUnavailable)
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
	report := BroadcastReport{Total: source.Total()}
	progress := newProgressCoalescer(req.Progress, report.Total, start)
	defer func() {
		now := time.Now()
		report.Duration = now.Sub(start)
		progress.Emit(report, now, true)
	}()

	pending := make([]pendingTarget, 0, min(report.Total, maxBroadcastInFlight))
	var deliveryResources []tasks.ResourceRequirement
	if response.HasMedia() {
		deliveryResources = []tasks.ResourceRequirement{{Name: "media", Amount: 1}}
	}

	consumeOldest := func() error {
		item := pending[0]
		copy(pending, pending[1:])
		pending = pending[:len(pending)-1]

		res, waitErr := item.ticket.Wait(runCtx)
		if waitErr != nil {
			report.Canceled = errors.Is(waitErr, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled)
			report.Duration = time.Since(start)
			return waitErr
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
		progress.Emit(report, time.Now(), false)
		return nil
	}

	targetIndex := 0
	for {
		if err := runCtx.Err(); err != nil {
			report.Canceled = true
			report.Duration = time.Since(start)
			return &report, err
		}

		page, done, err := source.Next(runCtx, maxBroadcastInFlight)
		if err != nil {
			report.Duration = time.Since(start)
			return &report, err
		}
		if len(page) == 0 && !done {
			report.Duration = time.Since(start)
			return &report, ErrTargetSourceStalled
		}
		if len(page) > maxBroadcastInFlight || targetIndex+len(page) > report.Total {
			report.Duration = time.Since(start)
			return &report, ErrTargetSourceContract
		}

		for _, target := range page {
			if target == nil {
				report.Failed++
				progress.Emit(report, time.Now(), false)
				targetIndex++
				continue
			}
			if err := runCtx.Err(); err != nil {
				report.Canceled = true
				report.Duration = time.Since(start)
				return &report, err
			}

			target := target
			index := targetIndex
			targetIndex++
			var sendErr error
			spec := tasks.WorkSpec{
				ID:               tasks.TaskID(fmt.Sprintf("broadcast:%d:%d", runID, index)),
				Scope:            scope,
				QuotaOwner:       tasks.OwnerID("system:broadcast"),
				Pool:             tasks.PoolID("general"),
				Class:            tasks.PriorityBackground,
				OrderingKey:      broadcastOrderingKey(target),
				ExecutionTimeout: 30 * time.Second,
				Resources:        deliveryResources,
				Handler: func(taskCtx context.Context) error {
					_, sendErr = delivery.DeliverCompiled(taskCtx, response, compiled, req.Vars, savedresponse.DeliverySink{
						SendMedia: func(mediaType, path, caption string) error {
							_, err := svc.SendMedia(taskCtx, target, mediaType, path, caption)
							return err
						},
						SendText: func(text string) error {
							_, err := svc.SendMessage(taskCtx, target, text)
							return err
						},
					})
					return sendErr
				},
			}

			for {
				ticket, err := client.Submit(runCtx, spec)
				if err == nil {
					pending = append(pending, pendingTarget{ticket: ticket, sendErr: &sendErr})
					break
				}

				var admissionErr *tasks.AdmissionError
				retryableAdmission := errors.As(err, &admissionErr) && admissionErr.ExecutionSemantics().ShouldRetry()
				if retryableAdmission {
					if len(pending) == 0 {
						report.Duration = time.Since(start)
						return &report, err
					}
					if err := consumeOldest(); err != nil {
						return &report, err
					}
					continue
				}

				report.Failed++
				progress.Emit(report, time.Now(), false)
				break
			}

			if len(pending) >= maxBroadcastInFlight {
				if err := consumeOldest(); err != nil {
					return &report, err
				}
			}
		}

		if done {
			break
		}
	}
	if targetIndex != report.Total {
		report.Duration = time.Since(start)
		return &report, ErrTargetSourceContract
	}

	for len(pending) > 0 {
		if err := consumeOldest(); err != nil {
			return &report, err
		}
	}

	report.Duration = time.Since(start)
	return &report, nil
}
