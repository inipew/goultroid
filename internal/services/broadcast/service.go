package broadcast

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// TargetType defines the target scope for broadcasting.
type TargetType string

const (
	TargetAll      TargetType = "all"
	TargetUsers    TargetType = "users"
	TargetGroups   TargetType = "groups"
	TargetChannels TargetType = "channels"
)

// BroadcastReport summarizes the results of a broadcast job.
type BroadcastReport struct {
	Total       int           `json:"total"`
	Sent        int           `json:"sent"`
	Failed      int           `json:"failed"`
	RateLimited int           `json:"rate_limited"`
	Canceled    bool          `json:"canceled"`
	Duration    time.Duration `json:"duration"`
}

// ProgressCallback reports live metrics during a broadcast.
type ProgressCallback func(report BroadcastReport)

// BroadcastRequest specifies parameters for a broadcast job.
type BroadcastRequest struct {
	Targets  []tg.InputPeerClass
	Text     string
	Delay    time.Duration
	Progress ProgressCallback
}

// Service coordinates mass messaging with FloodWait resilience and rate limiting.
type Service struct {
	svc     core.TelegramServicer
	svcFunc func() core.TelegramServicer
	logger  *zap.Logger
	cancel  context.CancelFunc
	mu      sync.Mutex
}

// NewService creates a new broadcast service.
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

func (s *Service) getService() core.TelegramServicer {
	if s.svc != nil {
		return s.svc
	}
	if s.svcFunc != nil {
		return s.svcFunc()
	}
	return nil
}

// CancelActive aborts an ongoing broadcast job if one is running.
func (s *Service) CancelActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		return true
	}
	return false
}

// Broadcast sends the message to all targets with rate-limiting and FloodWait retry logic.
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

	delay := req.Delay
	if delay <= 0 {
		delay = 300 * time.Millisecond // Default 300ms safe interval between messages
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}()

	start := time.Now()
	report := BroadcastReport{
		Total: len(req.Targets),
	}

	for _, target := range req.Targets {
		select {
		case <-runCtx.Done():
			report.Canceled = true
			report.Duration = time.Since(start)
			return &report, runCtx.Err()
		default:
		}

		// Attempt sending with flood-wait handling
		sent := false
		for attempts := 0; attempts < 3; attempts++ {
			_, err := svc.SendMessage(runCtx, target, req.Text)
			if err == nil {
				sent = true
				report.Sent++
				break
			}

			// Check for Telegram FloodWait error
			if waitDuration, ok := tgerr.AsFloodWait(err); ok {
				report.RateLimited++
				if waitDuration > 60*time.Second {
					// Don't sleep more than 60s for a single message in broadcast
					s.logger.Warn("flood wait too long, skipping target", zap.Duration("wait", waitDuration))
					break
				}
				s.logger.Info("flood wait encountered during broadcast, backing off", zap.Duration("wait", waitDuration))
				select {
				case <-runCtx.Done():
					report.Canceled = true
					report.Duration = time.Since(start)
					return &report, runCtx.Err()
				case <-time.After(waitDuration):
				}
				continue
			}

			// Other error
			s.logger.Debug("broadcast message error", zap.Error(err))
			break
		}

		if !sent {
			report.Failed++
		}

		if req.Progress != nil {
			req.Progress(report)
		}

		select {
		case <-runCtx.Done():
			report.Canceled = true
			report.Duration = time.Since(start)
			return &report, runCtx.Err()
		case <-time.After(delay):
		}
	}

	report.Duration = time.Since(start)
	return &report, nil
}
