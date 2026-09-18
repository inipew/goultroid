package telegram

import (
	"context"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/runtime"
	"go.uber.org/zap"
)

// Ensure Dispatcher implements runtime.Component.
var _ runtime.Component = (*Dispatcher)(nil)

// Name returns component identifier for runtime.Component.
func (d *Dispatcher) Name() string {
	return "dispatcher"
}

// Dependencies returns prerequisite components for runtime.Component.
func (d *Dispatcher) Dependencies() []string {
	return []string{"eventbus", "taskengine"}
}

// Health probes Dispatcher health.
func (d *Dispatcher) Health(ctx context.Context) runtime.ComponentHealth {
	if d.stopping.Load() {
		return runtime.ComponentHealth{
			Status:  runtime.HealthUnhealthy,
			Details: "dispatcher stopping",
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

const (
	peerBatchTimeout = 100 * time.Millisecond
	peerBatchMaxSize = 50
	peerPendingLimit = 4096
)

func (d *Dispatcher) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.peerSignal != nil {
		return nil
	}
	d.peerSignal = make(chan struct{}, 1)
	d.peerUsers = make(map[int64]*tg.User)
	d.peerChannels = make(map[int64]*tg.Channel)
	d.peerChats = make(map[int64]*tg.Chat)
	peerCtx, peerCancel := context.WithCancel(ctx)
	d.peerCancel = peerCancel
	d.peerDone = make(chan struct{})
	peerProcessors := 1
	d.peerWG.Add(peerProcessors)
	signal := d.peerSignal
	done := d.peerDone
	for i := 0; i < peerProcessors; i++ {
		go d.peerWorker(peerCtx, signal)
	}
	// One lifecycle-owned reaper per Dispatcher start. Stop/Drain never create
	// waiter goroutines, so caller deadlines cannot accumulate detached joins.
	go func() {
		d.peerWG.Wait()
		close(done)
	}()
	return nil
}

func (d *Dispatcher) enqueuePeerEntities(e tg.Entities) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping.Load() || d.peerSignal == nil {
		if !d.stopping.Load() {
			d.peerDropped.Add(1)
		}
		return
	}
	accepted := false
	for id, user := range e.Users {
		if user == nil {
			continue
		}
		if _, exists := d.peerUsers[id]; exists || len(d.peerUsers)+len(d.peerChannels)+len(d.peerChats) < peerPendingLimit {
			d.peerUsers[id] = user
			accepted = true
		} else {
			d.peerDropped.Add(1)
		}
	}
	for id, channel := range e.Channels {
		if channel == nil {
			continue
		}
		if _, exists := d.peerChannels[id]; exists || len(d.peerUsers)+len(d.peerChannels)+len(d.peerChats) < peerPendingLimit {
			d.peerChannels[id] = channel
			accepted = true
		} else {
			d.peerDropped.Add(1)
		}
	}
	for id, chat := range e.Chats {
		if chat == nil {
			continue
		}
		if _, exists := d.peerChats[id]; exists || len(d.peerUsers)+len(d.peerChannels)+len(d.peerChats) < peerPendingLimit {
			d.peerChats[id] = chat
			accepted = true
		} else {
			d.peerDropped.Add(1)
		}
	}
	if accepted {
		d.peerEnqueued.Add(1)
		select {
		case d.peerSignal <- struct{}{}:
		default:
		}
	}
}

func (d *Dispatcher) takePendingPeers() ([]*tg.User, []*tg.Channel, []*tg.Chat) {
	d.mu.Lock()
	defer d.mu.Unlock()
	users := make([]*tg.User, 0, len(d.peerUsers))
	for _, user := range d.peerUsers {
		users = append(users, user)
	}
	channels := make([]*tg.Channel, 0, len(d.peerChannels))
	for _, channel := range d.peerChannels {
		channels = append(channels, channel)
	}
	chats := make([]*tg.Chat, 0, len(d.peerChats))
	for _, chat := range d.peerChats {
		chats = append(chats, chat)
	}
	d.peerUsers = make(map[int64]*tg.User)
	d.peerChannels = make(map[int64]*tg.Channel)
	d.peerChats = make(map[int64]*tg.Chat)
	return users, channels, chats
}

func (d *Dispatcher) peerWorker(ctx context.Context, signal <-chan struct{}) {
	defer d.peerWG.Done()

	flush := func() {
		uList, chList, cList := d.takePendingPeers()
		if len(uList) == 0 && len(chList) == 0 && len(cList) == 0 {
			return
		}

		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		resolver := d.getResolver()
		r, ok := resolver.(*Resolver)
		if !ok || r == nil || r.storage == nil {
			return
		}
		if err := r.storage.SaveEntitiesBatch(saveCtx, uList, chList, cList); err != nil {
			d.peerSaveFailed.Add(1)
			d.logger.Warn("failed to save entities batch to storage", zap.Error(err))
		}
	}

	timer := time.NewTimer(peerBatchTimeout)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timerActive := false

	for {
		select {
		case <-ctx.Done():
			flush()
			return

		case _, ok := <-signal:
			if !ok {
				flush()
				return
			}
			d.mu.RLock()
			pending := len(d.peerUsers) + len(d.peerChannels) + len(d.peerChats)
			d.mu.RUnlock()
			if pending >= peerBatchMaxSize {
				if timerActive && !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timerActive = false
				flush()
			} else if !timerActive {
				timer.Reset(peerBatchTimeout)
				timerActive = true
			}

		case <-timer.C:
			timerActive = false
			flush()
		}
	}
}

// Quiesce shuts the ingress gate immediately, preventing new updates, messages,
// callbacks, or inline queries from entering the dispatch pipeline (ADR 0006 §3.4).
func (d *Dispatcher) Quiesce(ctx context.Context) error {
	d.mu.Lock()
	d.acceptingUpdates.Store(false)
	d.mu.Unlock()
	return nil
}

// Drain waits for ingress accepted before Quiesce to leave the dispatch path,
// then waits for command callbacks. Both counters are context-aware, so a
// shutdown deadline never leaves a detached waiter goroutine behind.
func (d *Dispatcher) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = d.Quiesce(ctx)
	if err := d.inFlight.WaitContext(ctx); err != nil {
		return err
	}
	return d.cmdWG.WaitContext(ctx)
}

func (d *Dispatcher) beginPeerStop() {
	d.stopping.Store(true)
	d.mu.Lock()
	signal := d.peerSignal
	d.peerSignal = nil
	done := d.peerDone
	d.mu.Unlock()
	d.peerStopOnce.Do(func() {
		if signal != nil {
			close(signal)
			return
		}
		// Stop-before-Start: no worker reaper exists, so complete the already
		// empty peer lifecycle synchronously.
		if done != nil {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
}

// Stop uses the explicit Drain phase and never lets peer-cache workers
// outlive an expired shutdown budget while retaining DB access.
func (d *Dispatcher) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	drainErr := d.Drain(ctx)
	d.beginPeerStop()
	select {
	case <-d.peerDone:
		d.mu.RLock()
		cancel := d.peerCancel
		d.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		return drainErr
	case <-ctx.Done():
		d.mu.RLock()
		cancel := d.peerCancel
		d.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		if drainErr != nil {
			return drainErr
		}
		return ctx.Err()
	}
}

// ForceStop is the runtime emergency path after graceful budget expiry.
func (d *Dispatcher) ForceStop(context.Context) error {
	_ = d.Quiesce(context.Background())
	d.beginPeerStop()
	d.mu.RLock()
	cancel := d.peerCancel
	d.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (d *Dispatcher) PeerCacheStats() (enqueued, dropped, saveFailed int64) {
	return d.peerEnqueued.Load(), d.peerDropped.Load(), d.peerSaveFailed.Load()
}
