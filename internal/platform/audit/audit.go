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

// auditDetails sanitizes metadata at the shared audit boundary. Audit callers
// are not trusted to supply log-safe maps: args, response bodies, tokens,
// nested objects and arbitrary strings must never enter structured logs or
// the retained ring buffer. Only typed, low-cardinality diagnostics survive.
func auditDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	var safe map[string]any
	for _, key := range []string{"found", "secret_present", "owner_present", "arg_count"} {
		value, ok := details[key]
		if !ok {
			continue
		}
		switch key {
		case "arg_count":
			count, ok := value.(int)
			if !ok || count < 0 {
				continue
			}
		default:
			if _, ok := value.(bool); !ok {
				continue
			}
		}
		if safe == nil {
			safe = make(map[string]any, 4)
		}
		safe[key] = value
	}
	return safe
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
	// Snapshot only a tiny allowlist of scalar fields before logging or
	// retention; do not retain a caller-owned map (which can later mutate).
	event.Details = auditDetails(event.Details)

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
		event := s.buffer[i]
		if event.Details != nil {
			snapshot := make(map[string]any, len(event.Details))
			for key, value := range event.Details {
				snapshot[key] = value
			}
			event.Details = snapshot
		}
		result = append(result, event)
	}
	return result
}
