package execution

import (
	"context"
	"time"
)

// Drain waits until every compatibility task has observed TaskEngine's terminal
// result and its callback has been delivered. It never owns execution itself.
func (s *LegacySubmitter) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.reapTerminal()
		s.mu.Lock()
		remaining := len(s.pending)
		s.mu.Unlock()
		if remaining == 0 {
			return nil
		}
		s.signal()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *LegacySubmitter) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}
