package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
)

// u6ExecutionGate provides deterministic barrier synchronization for U6
// integration tests without relying on time.Sleep.
type u6ExecutionGate struct {
	startedOnce sync.Once
	releaseOnce sync.Once
	doneOnce    sync.Once

	startedCh chan struct{}
	releaseCh chan struct{}
	doneCh    chan struct{}
}

func newU6ExecutionGate() *u6ExecutionGate {
	return &u6ExecutionGate{
		startedCh: make(chan struct{}),
		releaseCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (g *u6ExecutionGate) MarkStarted() {
	g.startedOnce.Do(func() {
		close(g.startedCh)
	})
}

func (g *u6ExecutionGate) WaitStarted(ctx context.Context) error {
	select {
	case <-g.startedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *u6ExecutionGate) WaitRelease(ctx context.Context) error {
	select {
	case <-g.releaseCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *u6ExecutionGate) Release() {
	g.releaseOnce.Do(func() {
		close(g.releaseCh)
	})
}

func (g *u6ExecutionGate) MarkDone() {
	g.doneOnce.Do(func() {
		close(g.doneCh)
	})
}

func (g *u6ExecutionGate) WaitDone(ctx context.Context) error {
	select {
	case <-g.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func u6WaitClosed(t *testing.T, ch <-chan struct{}, timeout time.Duration, desc string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("timed out after %v waiting for %s", timeout, desc)
	}
}

func u6TestAppConfig(t *testing.T) *config.Config {
	t.Helper()
	tmpDir := t.TempDir()
	return &config.Config{
		OwnerID:      123456,
		AppID:        123456,
		AppHash:      "hash123",
		BotToken:     "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
		Phone:        "+628123456789",
		SessionFile:  filepath.Join(tmpDir, "session.json"),
		DatabasePath: filepath.Join(tmpDir, "test.db"),
		Prefix:       ".",
		LogLevel:     "error",
	}
}

func TestU6Fixtures_ExecutionGate(t *testing.T) {
	gate := newU6ExecutionGate()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		defer gate.MarkDone()
		gate.MarkStarted()
		_ = gate.WaitRelease(ctx)
	}()

	if err := gate.WaitStarted(ctx); err != nil {
		t.Fatalf("WaitStarted failed: %v", err)
	}
	gate.Release()
	if err := gate.WaitDone(ctx); err != nil {
		t.Fatalf("WaitDone failed: %v", err)
	}
	u6WaitClosed(t, gate.doneCh, time.Second, "gate doneCh")
}
