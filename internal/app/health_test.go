package app

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestHealth_NotRunningIsNotReady(t *testing.T) {
	a := &App{}
	health := a.Health()
	if health.Ready {
		t.Fatal("new application must not be ready")
	}
	if health.Status != HealthDegraded {
		t.Fatalf("new application status = %q, want degraded", health.Status)
	}
	if !health.HasReason("runtime:not-running:") {
		t.Fatalf("missing lifecycle reason: %v", health.Reasons)
	}
}

func TestHealth_RunningIsHealthy(t *testing.T) {
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	a := &App{eventBus: bus, startTime: time.Now()}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.Health()
	if !health.Ready || !health.Healthy() {
		t.Fatalf("running app health = %+v", health)
	}
}

func TestHealth_StoppedWithPluginResourcesIsUnhealthy(t *testing.T) {
	a := &App{}
	a.lifecycleState.Store(uint32(lifecycleStopped))
	health := a.Health()
	if health.Ready || health.Status != HealthUnhealthy {
		t.Fatalf("stopped app health = %+v", health)
	}
}

func TestHealthContext_ReportsDatabase(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	a.lifecycleState.Store(uint32(lifecycleRunning))
	health := a.HealthContext(context.Background())
	if got := health.Subsystems["database"]; got != HealthHealthy {
		t.Fatalf("database health = %q, want %q", got, HealthHealthy)
	}
}
