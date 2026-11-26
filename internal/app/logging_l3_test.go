package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/logging/zaplog"
	"github.com/inipew/goultroid/internal/resource"
	"github.com/inipew/goultroid/internal/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type failingSyncCore struct {
	zapcore.Core
	syncMu     sync.Mutex
	syncCalled int
	syncErr    error
	onSync     func()
}

func (c *failingSyncCore) Sync() error {
	c.syncMu.Lock()
	c.syncCalled++
	cb := c.onSync
	err := c.syncErr
	c.syncMu.Unlock()
	if cb != nil {
		cb()
	}
	return err
}

func TestInitAppLogger_ProductionJSONAndDevelopmentConsole(t *testing.T) {
	prodAdapter, prodZap, prodLevel := initAppLogger(&config.Config{
		LogLevel:  "info",
		LogEnv:    "production",
		LogFormat: "json",
	})
	if prodAdapter == nil || prodZap == nil {
		t.Fatal("expected non-nil production logger")
	}
	if prodLevel.Level() != zapcore.InfoLevel {
		t.Fatalf("expected info level, got %v", prodLevel.Level())
	}
	_ = syncAppLogger(prodAdapter, nil)

	devAdapter, devZap, devLevel := initAppLogger(&config.Config{
		LogLevel:  "debug",
		LogEnv:    "development",
		LogFormat: "console",
	})
	if devAdapter == nil || devZap == nil {
		t.Fatal("expected non-nil development logger")
	}
	if devLevel.Level() != zapcore.DebugLevel {
		t.Fatalf("expected debug level, got %v", devLevel.Level())
	}
	_ = syncAppLogger(devAdapter, nil)
}

func TestInitAppLogger_FallbackOnBuildFailure(t *testing.T) {
	var stderr bytes.Buffer
	adapter, z, _ := initAppLoggerWith(&config.Config{LogLevel: "warn"}, &stderr, func(zap.Config, ...zap.Option) (*zap.Logger, error) {
		return nil, errors.New("simulated zap build error")
	})
	if adapter == nil || z == nil {
		t.Fatal("expected fallback logger on build error")
	}
	adapter.Warn("startup warning survived")
	if !strings.Contains(stderr.String(), "logger initialization failed") {
		t.Fatalf("expected fallback diagnostic on stderr, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "startup warning survived") {
		t.Fatalf("expected fallback logger to emit messages to stderr, got %q", stderr.String())
	}
}

func TestSyncAppLogger_ReportsFailureWithoutPanicking(t *testing.T) {
	obsCore, _ := observer.New(zapcore.InfoLevel)
	fc := &failingSyncCore{Core: obsCore, syncErr: errors.New("disk full")}
	adapter := zaplog.Wrap(zap.New(fc))

	var stderr bytes.Buffer
	err := syncAppLogger(adapter, &stderr)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected sync error returned, got %v", err)
	}
	if !strings.Contains(stderr.String(), "logger sync failed") {
		t.Fatalf("expected fallback stderr report, got %q", stderr.String())
	}
}

func TestSafePanicReporters_RedactSecrets(t *testing.T) {
	obsCore, logs := observer.New(zapcore.ErrorLevel)
	adapter := zaplog.Wrap(zap.New(obsCore))

	coreRep := safeCorePanicReporter{logger: adapter.Named("plugin.panic")}
	coreRep.ReportPanic(core.PanicReport{
		Owner:     "plugin:test",
		Component: "test",
		Value:     "panic with https://user:pw@example.com/p?token=secret123",
		Stack:     []byte("goroutine 1:\n/home/alice/secret.go:42\n"),
		At:        time.Now().UTC(),
	})

	rtRep := safeRuntimePanicReporter{logger: adapter.Named("supervisor.panic")}
	rtRep.ReportPanic(runtime.PanicReport{
		Owner:     "supervisor",
		Component: "worker",
		Value:     errors.New("Bearer super-secret-jwt"),
		Stack:     []byte("goroutine 2:\n/root/private.go:9\n"),
		At:        time.Now().UTC(),
	})

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 panic log entries, got %d", len(entries))
	}
	for _, entry := range entries {
		for k, v := range entry.ContextMap() {
			s, _ := v.(string)
			for _, bad := range []string{"secret123", "super-secret-jwt", "/home/alice", "/root/private", "pw"} {
				if strings.Contains(s, bad) {
					t.Errorf("field %s leaked %q in %q", k, bad, s)
				}
			}
		}
	}
}

func TestApp_ShutdownSyncsOnceAfterResourceCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		OwnerID:      123456,
		AppID:        123456,
		AppHash:      "hash123",
		Phone:        "+628123456789",
		SessionFile:  filepath.Join(tmpDir, "session.json"),
		DatabasePath: filepath.Join(tmpDir, "test.db"),
		Prefix:       ".",
		LogLevel:     "error",
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var order []string
	var orderMu sync.Mutex
	obsCore, _ := observer.New(zapcore.InfoLevel)
	fc := &failingSyncCore{
		Core: obsCore,
		onSync: func() {
			orderMu.Lock()
			order = append(order, "sync")
			orderMu.Unlock()
		},
	}
	app.opLogger = zaplog.Wrap(zap.New(fc))

	if err := app.resources.RegisterWithCleanupContext(resource.Resource{
		ID:        "test-res-1",
		Owner:     "test",
		Type:      resource.TypeLock,
		CreatedAt: time.Now().UTC(),
	}, func(context.Context) error {
		orderMu.Lock()
		order = append(order, "resource_cleanup")
		orderMu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("register resource: %v", err)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	orderMu.Lock()
	defer orderMu.Unlock()
	if len(order) != 2 || order[0] != "resource_cleanup" || order[1] != "sync" {
		t.Fatalf("expected [resource_cleanup, sync], got %v", order)
	}
	if fc.syncCalled != 1 {
		t.Fatalf("expected Sync called exactly once, got %d", fc.syncCalled)
	}
}
