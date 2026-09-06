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
	Targets  []tg.InputPeerClass
	Text     string
	Delay    time.Duration
	Progress ProgressCallback
}

type Service struct {
	svc     core.TelegramServicer
	svcFunc func() core.TelegramServicer
	logger  *zap.Logger
	cancel  context.CancelFunc
	mu      sync.Mutex
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

func (s *Service) getService() core.TelegramServicer {
	if s.svc != nil {
		return s.svc
	}
	if s.svcFunc != nil {
		return s.svcFunc()
	}
	return nil
}

// CancelActive aborts the single active broadcast job, if any. Broadcasts are
// deliberately serialized: allowing a second run to replace the cancellation
// handle of the first run creates a race where cleanup can cancel the wrong job.
func (s *Service) CancelActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return false
	}
	s.cancel()
	s.cancel = nil
	return true
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

	delay := req.Delay
	if delay <= 0 {
		delay = 300 * time.Millisecond
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: another broadcast is already running", core.ErrConflict)
	}
	s.cancel = cancel
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		// Because concurrent Broadcast calls are rejected while cancel is set,
		// this cleanup can only clear the cancellation handle belonging to this run.
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}()

	start := time.Now()
	report := BroadcastReport{Total: len(req.Targets)}

	for _, target := range req.Targets {
		select {
		case <-runCtx.Done():
			report.Canceled = true
			report.Duration = time.Since(start)
			return &report, runCtx.Err()
		default:
		}

		sent := false
		for attempts := 0; attempts < 3; attempts++ {
			_, err := svc.SendMessage(runCtx, target, req.Text)
			if err == nil {
				sent = true
				report.Sent++
				break
			}
			if waitDuration, ok := tgerr.AsFloodWait(err); ok {
				report.RateLimited++
				if waitDuration > 60*time.Second {
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
