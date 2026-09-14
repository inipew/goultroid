package taskengine

import (
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func validConfig() Config {
	return Config{
		Pools: map[tasks.PoolID]PoolLimits{
			"default": {Workers: 4, MaxWaiting: 128, MaxWaitingBytes: 8 << 20},
		},
		ClassQuantum: map[tasks.PriorityClass]int{
			tasks.PriorityInteractive: 8,
			tasks.PriorityNormal:      4,
			tasks.PriorityBackground:  2,
			tasks.PriorityMaintenance: 1,
		},
		ResourceCapacity:     map[string]uint32{"media": 2},
		ResultCredits:        256,
		ControlInboxCapacity: 64,
		PrepareQueueCapacity: 64,
		PersistenceCapacity:  64,
		DefaultOwner: OwnerLimits{
			MaxWaiting:      32,
			MaxActive:       4,
			MaxWaitingBytes: 2 << 20,
			Weight:          1,
		},
		AdmissionDecisionLimit: time.Second,
		QueueTimeout:           time.Minute,
		PrepareTimeout:         2 * time.Second,
		ExecutionTimeout:       time.Minute,
		ResultRetention:        5 * time.Minute,
	}
}

func TestConfigValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestConfigValidate_ResultCreditsMustBeBounded(t *testing.T) {
	cfg := validConfig()
	cfg.ResultCredits = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected zero result credits to be rejected")
	}
}

func TestConfigValidate_AllPriorityClassesNeedQuantum(t *testing.T) {
	cfg := validConfig()
	delete(cfg.ClassQuantum, tasks.PriorityMaintenance)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing class quantum to be rejected")
	}
}

func TestConfigValidate_PoolWaitingBytesMustBeBounded(t *testing.T) {
	cfg := validConfig()
	cfg.Pools["default"] = PoolLimits{Workers: 4, MaxWaiting: 128}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected zero waiting-byte limit to be rejected")
	}
}

func TestConfigValidate_OwnerOverrideMustBeBounded(t *testing.T) {
	cfg := validConfig()
	cfg.Owners = map[tasks.QuotaOwner]OwnerLimits{
		"actor:1": {MaxWaiting: 1, MaxActive: 0, MaxWaitingBytes: 1024, Weight: 1},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid owner override to be rejected")
	}
}
