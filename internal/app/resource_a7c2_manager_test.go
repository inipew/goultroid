package app

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	interactionsqlite "github.com/inipew/goultroid/internal/interaction/sqlite"
	"github.com/inipew/goultroid/internal/plugin"
)

// The real feature specifications are registered through the production Plugin
// Manager, using a metadata-only test plugin so effect delivery remains covered
// in each owning feature's existing integration tests.
type a7c2ManagedDeclaration struct{ spec feature.Spec }

func (p a7c2ManagedDeclaration) Name() string              { return p.spec.ID }
func (p a7c2ManagedDeclaration) Init() error               { return nil }
func (p a7c2ManagedDeclaration) Commands() []core.Command  { return nil }
func (p a7c2ManagedDeclaration) FeatureSpec() feature.Spec { return p.spec }

func TestA7C2ManagerReloadOneFeatureKeepsSiblingDurableCallbacks(t *testing.T) {
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
	newManager := func() *plugin.Manager {
		manager := plugin.NewManager(core.NewRouter("."))
		for _, spec := range specs {
			if err := manager.RegisterWithContext(ctx, a7c2ManagedDeclaration{spec}); err != nil {
				t.Fatalf("register managed %s: %v", spec.ID, err)
			}
		}
		if err := manager.InteractionRuntime().SetDurableStore(store); err != nil {
			t.Fatal(err)
		}
		return manager
	}
	first := newManager()
	runtime := first.InteractionRuntime()
	proofs := make(map[string]a7c2Proof, len(specs))
	for i, spec := range specs {
		binding := rootinteraction.Binding{ActorID: 1001, ChatID: int64(500 + i), MessageID: 100 + i}
		created, err := runtime.Create(ctx, rootinteraction.CreateRequest{FeatureID: spec.ID, Binding: binding, State: []byte(spec.ID), TTL: time.Hour})
		if err != nil {
			t.Fatalf("create %s: %v", spec.ID, err)
		}
		action := a7c2FirstAction(t, spec)
		token, err := runtime.CallbackData(ctx, created.Session.ID, action)
		if err != nil {
			t.Fatal(err)
		}
		proofs[spec.ID] = a7c2Proof{spec.ID, action, binding, token, created.Session.ID}
	}
	old := proofs["blacklist"]
	original, err := runtime.ResolveCallback(ctx, old.token, old.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Disable(ctx, "blacklist"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ResolveCallback(ctx, old.token, old.binding); err == nil {
		t.Fatal("disabled Blacklist callback remained valid")
	}
	for featureID, proof := range proofs {
		if featureID == "blacklist" {
			continue
		}
		if _, err := runtime.ResolveCallback(ctx, proof.token, proof.binding); err != nil {
			t.Fatalf("disabling Blacklist invalidated %s: %v", featureID, err)
		}
	}
	if err := first.Enable(ctx, "blacklist"); err != nil {
		t.Fatal(err)
	}
	fresh, err := runtime.Create(ctx, rootinteraction.CreateRequest{
		FeatureID: "blacklist", Binding: old.binding, State: []byte("fresh"), TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	freshToken, err := runtime.CallbackData(ctx, fresh.Session.ID, old.action)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Session.Scope == original.Session.Scope {
		t.Fatal("BlackList reused stale plugin generation after enable")
	}
	if _, err := runtime.ResolveCallback(ctx, old.token, old.binding); err == nil {
		t.Fatal("old callback became valid when Blacklist was re-enabled")
	}
	if err := first.ShutdownWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	second := newManager()
	defer second.ShutdownWithContext(ctx)
	rt2 := second.InteractionRuntime()
	if err := rt2.RestoreDurable(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := rt2.ResolveCallback(ctx, old.token, old.binding); err == nil {
		t.Fatal("deleted old-generation token resurrected on process restart")
	}
	if _, err := rt2.ResolveCallback(ctx, freshToken, old.binding); err != nil {
		t.Fatalf("new Blacklist session did not survive restart: %v", err)
	}
	for id, proof := range proofs {
		if id == "blacklist" {
			continue
		}
		resolved, err := rt2.ResolveCallback(ctx, proof.token, proof.binding)
		if err != nil || resolved.Session.FeatureID != id {
			t.Fatalf("sibling %s lost durable callback: %+v %v", id, resolved, err)
		}
	}
	if stats := rt2.Stats(); stats.Sessions != 4 || stats.Restored != 4 {
		t.Fatalf("managed multi-plugin restart retained unexpected state: %+v", stats)
	}
}
