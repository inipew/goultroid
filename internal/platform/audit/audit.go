package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// AuditEvent represents a logged high-impact or security action.
type AuditEvent struct {
	ID            string         `json:"id"`
	Timestamp     time.Time      `json:"timestamp"`
	ActorID       int64          `json:"actor_id"`
	Action        string         `json:"action"`
	Target        string         `json:"target,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
}

// Auditor defines the contract for recording audit events.
type Auditor interface {
	Record(ctx context.Context, event AuditEvent) error
	Recent(limit int) []AuditEvent
}

// Service implements Auditor using a structured logger and an in-memory ring buffer.
type Service struct {
	logger  *zap.Logger
	mu      sync.RWMutex
	buffer  []AuditEvent
	maxSize int
	counter uint64
}

// NewService creates a new Audit Service with a structured logger and ring buffer.
func NewService(logger *zap.Logger, ringBufferSize int) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	if ringBufferSize <= 0 {
		ringBufferSize = 100
	}
	return &Service{
		logger:  logger,
		buffer:  make([]AuditEvent, 0, ringBufferSize),
		maxSize: ringBufferSize,
	}
}

// Record writes the audit event to the structured log and appends to the ring buffer.
func (s *Service) Record(ctx context.Context, event AuditEvent) error {
	if strings.TrimSpace(event.Action) == "" {
		return errors.New("audit action cannot be empty")
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	if event.CorrelationID == "" && ctx != nil {
		event.CorrelationID = core.GetCorrelationID(ctx)
	}

	s.mu.Lock()
	s.counter++
	if event.ID == "" {
		event.ID = fmt.Sprintf("audit-%d-%d", event.Timestamp.UnixNano(), s.counter)
	}

	if len(s.buffer) >= s.maxSize {
		s.buffer = s.buffer[1:]
	}
	s.buffer = append(s.buffer, event)
	s.mu.Unlock()

	s.logger.Info("audit event",
		zap.String("audit_id", event.ID),
		zap.String("action", event.Action),
		zap.Int64("actor_id", event.ActorID),
		zap.String("target", event.Target),
		zap.String("correlation_id", event.CorrelationID),
		zap.Any("details", event.Details),
	)

	return nil
}

// Recent returns the last n audit events, newest first.
func (s *Service) Recent(limit int) []AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	n := len(s.buffer)
	if limit <= 0 || limit > n {
		limit = n
	}

	result := make([]AuditEvent, 0, limit)
	for i := n - 1; i >= n-limit; i-- {
		result = append(result, s.buffer[i])
	}
	return result
}
