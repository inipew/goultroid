package telegram

import (
	"context"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

type peerUpdateJob struct {
	users    []*tg.User
	channels []*tg.Channel
	chats    []*tg.Chat
}

func (d *Dispatcher) Start(ctx context.Context) {
	if ctx == nil { ctx = context.Background() }
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.peerQueue != nil { return }
	d.peerQueue = make(chan peerUpdateJob, 1024)
	workers := 2
	d.peerWG.Add(workers)
	q := d.peerQueue
	for i := 0; i < workers; i++ { go d.peerWorker(ctx, q) }
}

func (d *Dispatcher) peerWorker(ctx context.Context, q <-chan peerUpdateJob) {
	defer d.peerWG.Done()
	for job := range q {
		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resolver := d.getResolver()
		r, ok := resolver.(*Resolver)
		if !ok || r == nil || r.storage == nil { cancel(); continue }
		if err := r.storage.SaveEntitiesBatch(saveCtx, job.users, job.channels, job.chats); err != nil {
			d.peerSaveFailed.Add(1)
			d.logger.Warn("failed to save entities batch to storage", zap.Error(err))
		}
		cancel()
	}
}

func (d *Dispatcher) Stop(ctx context.Context) error {
	if ctx == nil { ctx = context.Background() }
	var err error
	d.peerStopOnce.Do(func() {
		d.admissionMu.Lock()
		d.acceptingUpdates.Store(false)
		d.admissionMu.Unlock()
		d.stopping.Store(true)

		doneInFlight := make(chan struct{})
		go func() { d.inFlight.Wait(); close(doneInFlight) }()
		select { case <-doneInFlight: case <-ctx.Done(): err = ctx.Err() }

		doneCmds := make(chan struct{})
		go func() { d.cmdWG.Wait(); close(doneCmds) }()
		select { case <-doneCmds: case <-ctx.Done(): if err == nil { err = ctx.Err() } }

		d.mu.Lock()
		q := d.peerQueue
		d.peerQueue = nil
		d.mu.Unlock()
		if q == nil { return }
		close(q)
		doneWorkers := make(chan struct{})
		go func() { d.peerWG.Wait(); close(doneWorkers) }()
		select { case <-doneWorkers: case <-ctx.Done(): if err == nil { err = ctx.Err() } }
	})
	return err
}

func (d *Dispatcher) PeerCacheStats() (enqueued, dropped, saveFailed int64) {
	return d.peerEnqueued.Load(), d.peerDropped.Load(), d.peerSaveFailed.Load()
}
