package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

func readDotEnvForTest(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid dotenv line %q", line)
		}
		values[key] = value
	}
	return values
}

func TestTaskEngineConfigRoundTripAllFields(t *testing.T) {
	want := taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"general": {
				Concurrency:    11,
				MinConcurrency: 2,
				IdleTimeout:    1750 * time.Millisecond,
				BacklogLimit:   17,
				PayloadBudget:  1_234_567,
			},
			"bulk-x": {
				Concurrency:    5,
				MinConcurrency: 1,
				IdleTimeout:    2250 * time.Millisecond,
				BacklogLimit:   9,
				PayloadBudget:  7_654_321,
			},
		},
		ResultCapacity:      37,
		MaxTerminalRetained: 29,
		DecisionTimeout:     1750 * time.Millisecond,
		InboxCapacity:       333,
		MaxRetainedBytes:    123_456_789,
		MaxOutputBytes:      234_567,
		MaxFailureBytes:     3456,
		DeliveryConcurrency: 3,
		DeliveryQueueCap:    71,
		TerminalTTL:         2*time.Minute + 3500*time.Millisecond,
		MaxScopeTombstones:  55,
		ResourceCapacities:  map[string]int64{"process": 7, "gpu": 2},
	}
	cfg := &Config{
		AppID:                           123,
		AppHash:                         "hash",
		Phone:                           "+628123",
		SessionFile:                     "data/session.json",
		DatabasePath:                    "data/goultroid.db",
		Prefix:                          ".",
		OwnerID:                         42,
		Mode:                            "userbot",
		LogLevel:                        "info",
		TaskEngine:                      want,
		TaskEngineDurabilityConcurrency: 7,
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := WriteEnv(path, cfg); err != nil {
		t.Fatal(err)
	}
	values := readDotEnvForTest(t, path)
	got, err := LoadFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskEngineDurabilityConcurrency != 7 {
		t.Fatalf("durability concurrency = %d, want 7", got.TaskEngineDurabilityConcurrency)
	}
	if !reflect.DeepEqual(got.TaskEngine, want) {
		t.Fatalf("taskengine config did not round-trip\n got: %#v\nwant: %#v", got.TaskEngine, want)
	}
}

func TestTaskEngineConfigLegacyAliases(t *testing.T) {
	values := map[string]string{
		"APP_ID":                               "123",
		"APP_HASH":                             "hash",
		"PHONE":                                "+628123",
		"TASKENGINE_RETAINED_MB":               "123",
		"TASKENGINE_POOL_GENERAL_IDLE_SECONDS": "17",
	}
	cfg, err := LoadFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskEngine.MaxRetainedBytes != 123<<20 {
		t.Fatalf("legacy retained MB alias = %d", cfg.TaskEngine.MaxRetainedBytes)
	}
	if cfg.TaskEngine.Pools["general"].IdleTimeout != 17*time.Second {
		t.Fatalf("legacy idle seconds alias = %s", cfg.TaskEngine.Pools["general"].IdleTimeout)
	}
}

func TestTaskEngineConfigValidation(t *testing.T) {
	base := map[string]string{"APP_ID": "123", "APP_HASH": "hash", "PHONE": "+628123"}
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "duration", key: "TASKENGINE_DECISION_TIMEOUT", val: "-1s"},
		{name: "payload", key: "TASKENGINE_POOL_GENERAL_PAYLOAD_BYTES", val: "-1"},
		{name: "minimum exceeds maximum", key: "TASKENGINE_POOL_GENERAL_MIN", val: "99"},
		{name: "duplicate resources", key: "TASKENGINE_RESOURCE_CAPACITIES", val: "process=2,process=3"},
		{name: "pool env prefix collision", key: "TASKENGINE_POOLS", val: "foo-bar,foo_bar"},
		{name: "negative max terminal", key: "TASKENGINE_MAX_TERMINAL", val: "-1"},
		{name: "zero durability concurrency", key: "TASKENGINE_DURABILITY_CONCURRENCY", val: "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := make(map[string]string, len(base)+1)
			for k, v := range base {
				values[k] = v
			}
			values[tc.key] = tc.val
			if _, err := LoadFrom(func(key string) string { return values[key] }); err == nil {
				t.Fatalf("expected validation error for %s=%q", tc.key, tc.val)
			}
		})
	}
}

func TestWriteEnvRejectsInvalidTaskEngineConfig(t *testing.T) {
	cfg := &Config{
		AppID: 123, AppHash: "hash", Phone: "+628123",
		TaskEngine: taskengine.Config{
			Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
				"general": {Concurrency: 1, MinConcurrency: 1},
			},
			MaxTerminalRetained: -1,
		},
	}
	if err := WriteEnv(filepath.Join(t.TempDir(), ".env"), cfg); err == nil {
		t.Fatal("expected invalid TaskEngine config to be rejected")
	}
}
