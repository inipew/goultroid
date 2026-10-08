package app

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	interactionsqlite "github.com/inipew/goultroid/internal/interaction/sqlite"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/plugins/afk"
	"github.com/inipew/goultroid/plugins/blacklist"
	"github.com/inipew/goultroid/plugins/filters"
	"github.com/inipew/goultroid/plugins/pmpermit"
)

type a7c2Proof struct {
	feature string
	action  string
	binding rootinteraction.Binding
	token   []byte
	session string
}

func a7c2Specs(t *testing.T) []feature.Spec {
	t.Helper()
	specs := []feature.Spec{
		afk.New(nil, 1001, nil).FeatureSpec(),
		pmpermit.New(nil).FeatureSpec(),
		blacklist.NewWithMessageDeleter(nil, nil).FeatureSpec(),
		filters.New(nil, nil).FeatureSpec(),
	}
	for _, spec := range specs {
		if spec.DurabilityVersion == "" {
			t.Fatalf("%s missing durable contract", spec.ID)
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("%s: invalid real plugin feature spec: %v", spec.ID, err)
		}
	}
	return specs
}

func a7c2Registry(t *testing.T, specs []feature.Spec, generation uint64) (*feature.Registry, map[string]tasks.ScopeIdentity, []func()) {
	t.Helper()
	registry := feature.NewRegistry()
	scopes := make(map[string]tasks.ScopeIdentity, len(specs))
	cleanups := make([]func(), 0, len(specs))
	for _, spec := range specs {
		scope := tasks.ScopeIdentity{Owner: "plugin:" + spec.ID, Generation: generation}
		registration, err := registry.Register(feature.Owner{ID: spec.ID, Scope: scope}, spec)
		if err != nil {
			t.Fatalf("register %s: %v", spec.ID, err)
		}
		scopes[spec.ID] = scope
		cleanups = append(cleanups, registration.Close)
	}
	return registry, scopes, cleanups
}

func a7c2FirstAction(t *testing.T, spec feature.Spec) string {
	t.Helper()
	for _, action := range spec.Interactions {
		if action.Kind == feature.InteractionAction {
			return action.ID
		}
	}
	t.Fatalf("feature %s has no native action", spec.ID)
	return ""
}

// This is one *shared* canonical a2 catalog/runtime/SQLite store with the
// genuine AFK, PMPermit, Blacklist and Filters declarations. The owning-plugin
// A5 tests exercise their transport handlers and permission checks; this gate
// specifically detects cross-feature restoration, scope and capacity leaks.
func TestA7C2FourFeatureDurableRestartAndScopedCallbackPressure(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, interactionsqlite.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	store := interactionsqlite.NewStore(db.DB)
	specs := a7c2Specs(t)
	config := rootinteraction.Config{
		MaxSessions:         32,
		MaxSessionsPerScope: 32,
		MaxSessionsPerActor: 32,
		MaxStateBytes:       128,
		MaxTotalStateBytes:  4096,
	}
	var before, peak, settled goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	baselineG := goruntime.NumGoroutine()

	registry1, _, cleanup1 := a7c2Registry(t, specs, 1)
	first, err := rootinteraction.NewRuntime(registry1, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	proofs := make([]a7c2Proof, 0, 32)
	for _, spec := range specs {
		action := a7c2FirstAction(t, spec)
		for i := 0; i < 8; i++ {
			binding := rootinteraction.Binding{ActorID: 1001, ChatID: 500 + int64(i), MessageID: 100 + i}
			state := []byte(fmt.Sprintf("feature=%s;index=%d", spec.ID, i))
			created, err := first.Create(ctx, rootinteraction.CreateRequest{
				FeatureID: spec.ID, Binding: binding, State: state, TTL: time.Hour,
			})
			if err != nil {
				t.Fatalf("create %s/%d: %v", spec.ID, i, err)
			}
			token, err := first.CallbackData(ctx, created.Session.ID, action)
			if err != nil {
				t.Fatal(err)
			}
			proofs = append(proofs, a7c2Proof{spec.ID, action, binding, token, created.Session.ID})
		}
	}
	if stats := first.Stats(); stats.Sessions != 32 || stats.StateBytes > 4096 {
		t.Fatalf("unexpected mixed feature bound: %+v", stats)
	}
	if _, err := first.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: specs[0].ID, Binding: rootinteraction.Binding{ActorID: 2002}, State: []byte("overflow"),
	}); !errors.Is(err, rootinteraction.ErrCapacity) {
		t.Fatalf("capacity did not fail closed: %v", err)
	}
	for _, proof := range proofs {
		for retry := 0; retry < 32; retry++ {
			result, err := first.ResolveCallback(ctx, proof.token, proof.binding)
			if err != nil || result.Session.FeatureID != proof.feature || result.Token.ActionID != proof.action {
				t.Fatalf("callback scope crossed feature %s: %+v %v", proof.feature, result, err)
			}
		}
	}
	goruntime.ReadMemStats(&peak)
	first.PreserveDurableOnShutdown()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for _, closeRegistration := range cleanup1 {
		closeRegistration()
	}

	registry2, scopes2, cleanup2 := a7c2Registry(t, specs, 2)
	defer func() {
		for _, closeRegistration := range cleanup2 {
			closeRegistration()
		}
	}()
	second, err := rootinteraction.NewRuntime(registry2, config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	if err := second.RestoreDurable(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := second.Stats(); stats.Sessions != 32 || stats.Restored != 32 {
		t.Fatalf("durable shared restore incomplete: %+v", stats)
	}
	for _, proof := range proofs {
		result, err := second.ResolveCallback(ctx, proof.token, proof.binding)
		if err != nil || result.Session.Scope != scopes2[proof.feature] {
			t.Fatalf("old callback not restored to correct generation for %s: %+v %v", proof.feature, result, err)
		}
		for _, other := range specs {
			if other.ID == proof.feature {
				continue
			}
			forged, err := rootinteraction.EncodeCallbackToken(other.ID, proof.action, proof.session, result.Session.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := second.ResolveCallback(ctx, forged, proof.binding); err == nil {
				t.Fatalf("cross-feature token %s -> %s accepted", proof.feature, other.ID)
			}
		}
	}
	canceled := second.CancelScope(scopes2["blacklist"])
	if canceled != 8 {
		t.Fatalf("blacklist generation canceled %d sessions, want 8", canceled)
	}
	for _, proof := range proofs {
		_, err := second.ResolveCallback(ctx, proof.token, proof.binding)
		if proof.feature == "blacklist" && err == nil {
			t.Fatal("canceled blacklist callback still executable")
		}
		if proof.feature != "blacklist" && err != nil {
			t.Fatalf("blacklist unload invalidated %s callback: %v", proof.feature, err)
		}
	}
	if stats := second.Stats(); stats.Sessions != 24 {
		t.Fatalf("scope cancel retained or destroyed unrelated sessions: %+v", stats)
	}
	goruntime.GC()
	goruntime.ReadMemStats(&settled)
	t.Logf("A7-C2 platform=%s/%s Go=%s goroutines baseline=%d settled=%d heap before=%d peak=%d settled=%d sessions restored=32 canceled=8",
		goruntime.GOOS, goruntime.GOARCH, goruntime.Version(), baselineG, goruntime.NumGoroutine(), before.HeapAlloc, peak.HeapAlloc, settled.HeapAlloc)
}
