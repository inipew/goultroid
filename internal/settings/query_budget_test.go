package settings

import (
	"context"
	"sync/atomic"
	"testing"
)

type countingRepository struct {
	Repository
	effectiveQueries atomic.Int64
}

func (c *countingRepository) GetEffectiveSetting(ctx context.Context, namespace, key string, chatID, userID int64) (*SettingItem, error) {
	c.effectiveQueries.Add(1)
	return c.Repository.GetEffectiveSetting(ctx, namespace, key, chatID, userID)
}

func TestSettings_QueryBudget_ZeroQueriesAfterWarmup(t *testing.T) {
	repo := newMockRepo()
	counter := &countingRepository{Repository: repo}

	// Register setting
	reg := NewRegistry()
	_ = reg.Register(SettingDefinition{
		Namespace:    "core",
		Key:          "prefix",
		Type:         TypeString,
		DefaultValue: ".",
	})

	// Pre-seed setting in repo
	_ = counter.SetSetting(context.Background(), &SettingItem{
		ScopeType: "global",
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		Value:     "!",
	})

	svc := NewService(counter, reg, nil)
	ctx := context.Background()

	// 1. Initial resolution triggers 1 query (warmup)
	val, err := svc.Resolve(ctx, 123, 456, "core", "prefix")
	if err != nil {
		t.Fatalf("unexpected error resolving setting: %v", err)
	}
	if val != "!" {
		t.Fatalf("expected '!', got %q", val)
	}
	if counter.effectiveQueries.Load() != 1 {
		t.Fatalf("expected exactly 1 query during warmup, got %d", counter.effectiveQueries.Load())
	}

	// 2. Next 100 repeated resolutions must produce exactly ZERO queries
	for i := 0; i < 100; i++ {
		v, err := svc.Resolve(ctx, 123, 456, "core", "prefix")
		if err != nil || v != "!" {
			t.Fatalf("unexpected resolution result at iteration %d: %v, %q", i, err, v)
		}
	}

	if counter.effectiveQueries.Load() != 1 {
		t.Fatalf("query budget exceeded: expected 1 total query, got %d", counter.effectiveQueries.Load())
	}
}

func TestSettings_QueryBudget_SchemaDefaultZeroQueriesAfterWarmup(t *testing.T) {
	repo := newMockRepo()
	counter := &countingRepository{Repository: repo}

	reg := NewRegistry()
	err := reg.Register(SettingDefinition{
		Namespace:    "core",
		Key:          "timeout",
		Type:         TypeString,
		DefaultValue: "30s",
	})
	if err != nil {
		t.Fatalf("failed to register definition: %v", err)
	}

	svc := NewService(counter, reg, nil)
	ctx := context.Background()

	// 1. Warmup query against empty repo returns default
	val, err := svc.Resolve(ctx, 0, 0, "core", "timeout")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "30s" {
		t.Fatalf("expected '30s', got %q", val)
	}
	if counter.effectiveQueries.Load() != 1 {
		t.Fatalf("expected 1 query during warmup, got %d", counter.effectiveQueries.Load())
	}

	// 2. Next 50 resolutions must produce 0 queries (cached default)
	for i := 0; i < 50; i++ {
		v, err := svc.Resolve(ctx, 0, 0, "core", "timeout")
		if err != nil || v != "30s" {
			t.Fatalf("unexpected result: %v, %q", err, v)
		}
	}

	if counter.effectiveQueries.Load() != 1 {
		t.Fatalf("query budget exceeded for schema default: expected 1, got %d", counter.effectiveQueries.Load())
	}
}
