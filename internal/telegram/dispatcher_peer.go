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
	return []string{"eventbus"}
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
	workers := 2
	d.peerWG.Add(workers)
	q := d.peerQueue
	for i := 0; i < workers; i++ {
		go d.peerWorker(ctx, q)
	}
	return nil
}

func (d *Dispatcher) peerWorker(_ context.Context, q <-chan peerUpdateJob) {
	defer d.peerWG.Done()
	for job := range q {
		// Shutdown may occur after the application root context is canceled. Peer
		// cache persistence is drain work, so it gets its own bounded context.
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resolver := d.getResolver()
		r, ok := resolver.(*Resolver)
		if !ok || r == nil || r.storage == nil {
			cancel()
			continue
		}
		if err := r.storage.SaveEntitiesBatch(saveCtx, job.users, job.channels, job.chats); err != nil {
			d.peerSaveFailed.Add(1)
			d.logger.Warn("failed to save entities batch to storage", zap.Error(err))
		}
		cancel()
	}
}

// Stop first closes ingress admission, then drains all in-flight dispatches, then
// closes the peer queue. The admission transition and WaitGroup.Add are serialized
// by d.mu so no new Add can occur after Wait starts.
func (d *Dispatcher) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	d.peerStopOnce.Do(func() {
		d.mu.Lock()
		d.acceptingUpdates.Store(false)
		d.mu.Unlock()

		doneInFlight := make(chan struct{})
		go func() {
			d.inFlight.Wait()
			close(doneInFlight)
		}()
		select {
		case <-doneInFlight:
		case <-ctx.Done():
			err = ctx.Err()
		}

		doneCmds := make(chan struct{})
		go func() {
			d.cmdWG.Wait()
			close(doneCmds)
		}()
		select {
		case <-doneCmds:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
		}

		d.stopping.Store(true)
		d.mu.Lock()
		q := d.peerQueue
		d.peerQueue = nil
		d.mu.Unlock()
		if q == nil {
			return
		}
		close(q)
		doneWorkers := make(chan struct{})
		go func() {
			d.peerWG.Wait()
			close(doneWorkers)
		}()
		select {
		case <-doneWorkers:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
		}
	})
	return err
}

func (d *Dispatcher) PeerCacheStats() (enqueued, dropped, saveFailed int64) {
	return d.peerEnqueued.Load(), d.peerDropped.Load(), d.peerSaveFailed.Load()
}
