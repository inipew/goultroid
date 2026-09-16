package telegram

import (
	"context"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/runtime"
	"go.uber.org/zap"
)

type peerUpdateJob struct {
	users    []*tg.User
	channels []*tg.Channel
	chats    []*tg.Chat
}

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
)

func (d *Dispatcher) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.peerQueue != nil {
		return nil
	}
	d.peerQueue = make(chan peerUpdateJob, 1024)
	peerCtx, peerCancel := context.WithCancel(ctx)
	d.peerCancel = peerCancel
	d.peerDone = make(chan struct{})
	peerProcessors := 1
	d.peerWG.Add(peerProcessors)
	q := d.peerQueue
	done := d.peerDone
	for i := 0; i < peerProcessors; i++ {
		go d.peerWorker(peerCtx, q)
	}
	// One lifecycle-owned reaper per Dispatcher start. Stop/Drain never create
	// waiter goroutines, so caller deadlines cannot accumulate detached joins.
	go func() {
		d.peerWG.Wait()
		close(done)
	}()
	return nil
}

func (d *Dispatcher) peerWorker(ctx context.Context, q <-chan peerUpdateJob) {
	defer d.peerWG.Done()

	users := make(map[int64]*tg.User)
	channels := make(map[int64]*tg.Channel)
	chats := make(map[int64]*tg.Chat)

	flush := func() {
		if len(users) == 0 && len(channels) == 0 && len(chats) == 0 {
			return
		}
		uList := make([]*tg.User, 0, len(users))
		for _, u := range users {
			uList = append(uList, u)
		}
		chList := make([]*tg.Channel, 0, len(channels))
		for _, ch := range channels {
			chList = append(chList, ch)
		}
		cList := make([]*tg.Chat, 0, len(chats))
		for _, c := range chats {
			cList = append(cList, c)
		}

		users = make(map[int64]*tg.User)
		channels = make(map[int64]*tg.Channel)
		chats = make(map[int64]*tg.Chat)

		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

		case job, ok := <-q:
			if !ok {
				flush()
				return
			}
			for _, u := range job.users {
				if u != nil {
					users[u.ID] = u
				}
			}
			for _, ch := range job.channels {
				if ch != nil {
					channels[ch.ID] = ch
				}
			}
			for _, c := range job.chats {
				if c != nil {
					chats[c.ID] = c
				}
			}

			if len(users)+len(channels)+len(chats) >= peerBatchMaxSize {
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
	q := d.peerQueue
	d.peerQueue = nil
	done := d.peerDone
	d.mu.Unlock()
	d.peerStopOnce.Do(func() {
		if q != nil {
			close(q)
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
