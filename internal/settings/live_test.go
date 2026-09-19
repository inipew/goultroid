package settings

import (
	"context"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestLiveBinderAppliesInitialAndCommittedGlobalValues(t *testing.T) {
	repo := newMockRepo()
	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bus.Close() }()

	svc := NewService(repo, reg, bus)
	binder := NewLiveBinder(svc, bus)

	var mu sync.Mutex
	applied := ""
	if err := binder.Bind("core", "prefix", func(value SettingValue) error {
		v := value.String()
		mu.Lock()
		applied = v
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := binder.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binder.Stop(context.Background()) }()

	get := func() string {
		mu.Lock()
		defer mu.Unlock()
		return applied
	}
	if got := get(); got != "." {
		t.Fatalf("initial applied prefix=%q, want .", got)
	}

	if err := svc.Set(context.Background(), ScopeGlobal, 0, "core", "prefix", "!", 1); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "!" {
		t.Fatalf("committed prefix=%q, want !", got)
	}

	// Contextual overrides must not mutate process-global consumers.
	if err := svc.Set(context.Background(), ScopeChat, 77, "core", "prefix", "#", 1); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "!" {
		t.Fatalf("chat override changed global live binding to %q", got)
	}

	if err := svc.Reset(context.Background(), ScopeGlobal, 0, "core", "prefix", 1); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "." {
		t.Fatalf("reset prefix=%q, want schema default .", got)
	}
}

func TestLiveBinderDurableReplayResolvesCurrentState(t *testing.T) {
	repo := newMockRepo()
	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bus.Close() }()
	svc := NewService(repo, reg, bus)
	binder := NewLiveBinder(svc, bus)

	applied := ""
	if err := binder.Bind("core", "prefix", func(value SettingValue) error {
		applied = value.String()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := binder.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binder.Stop(context.Background()) }()

	if err := repo.SetSetting(context.Background(), &SettingItem{
		ScopeType: string(ScopeGlobal),
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		ValueType: string(TypeString),
		Value:     "$",
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.PublishDurable(context.Background(), &core.SettingChangedEvent{
		ScopeType: string(ScopeGlobal),
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		OldVal:    "!",
		NewVal:    "stale-event-value",
	}); err != nil {
		t.Fatal(err)
	}
	if applied != "$" {
		t.Fatalf("durable replay applied %q, want current persisted $", applied)
	}
}

func TestLiveBinderStopFencesInFlightApply(t *testing.T) {
	repo := newMockRepo()
	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bus.Close() }()
	svc := NewService(repo, reg, bus)
	binder := NewLiveBinder(svc, bus)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	var applied []string
	if err := binder.Bind("core", "prefix", func(value SettingValue) error {
		v := value.String()
		mu.Lock()
		applied = append(applied, v)
		mu.Unlock()
		if v == "!" {
			entered <- struct{}{}
			<-release
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := binder.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	setDone := make(chan struct{})
	go func() {
		_ = svc.Set(context.Background(), ScopeGlobal, 0, "core", "prefix", "!", 1)
		close(setDone)
	}()
	<-entered

	stopDone := make(chan struct{})
	go func() {
		_ = binder.Stop(context.Background())
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("Stop returned while an apply callback was still in flight")
	default:
	}

	close(release)
	<-setDone
	<-stopDone

	if err := svc.Set(context.Background(), ScopeGlobal, 0, "core", "prefix", "#", 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, v := range applied {
		if v == "#" {
			t.Fatal("setting applied after LiveBinder.Stop")
		}
	}
}
