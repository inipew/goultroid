package broadcast

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
)

var (
	ErrTargetSourceStalled     = errors.New("broadcast: target source made no progress")
	ErrTargetSourceContract    = errors.New("broadcast: target source contract violated")
)

// TargetSource is a bounded, stateful snapshot iterator. One source instance
// belongs to exactly one Broadcast call. Implementations must keep membership
// fixed for the lifetime of the source and return at most limit targets per
// call.
type TargetSource interface {
	Total() int
	Next(context.Context, int) (targets []tg.InputPeerClass, done bool, err error)
}

type sliceTargetSource struct {
	targets []tg.InputPeerClass
	offset  int
}

func newSliceTargetSource(targets []tg.InputPeerClass) *sliceTargetSource {
	return &sliceTargetSource{targets: targets}
}

func (s *sliceTargetSource) Total() int {
	if s == nil {
		return 0
	}
	return len(s.targets)
}

func (s *sliceTargetSource) Next(_ context.Context, limit int) ([]tg.InputPeerClass, bool, error) {
	if s == nil || s.offset >= len(s.targets) {
		return nil, true, nil
	}
	if limit <= 0 {
		limit = maxBroadcastInFlight
	}
	end := min(len(s.targets), s.offset+limit)
	page := s.targets[s.offset:end]
	s.offset = end
	return page, s.offset >= len(s.targets), nil
}
