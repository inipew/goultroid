package deeplink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/tasks"
)

func newTestRouter(t *testing.T) (*Router, *SQLiteRepository) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error = %v", err)
	}
	repo := NewSQLiteRepository(db)
	return NewRouter(repo), repo
}

type testProvider struct {
	scope     tasks.ScopeIdentity
	resources []tasks.ResourceRequirement
	prepares  int
	executes  int
	payload   string
}

func (p *testProvider) Prepare(_ context.Context, payload string, _ int64) (PreparedTarget, error) {
	p.prepares++
	p.payload = payload
	return PreparedTarget{
		Scope:     p.scope,
		Resources: append([]tasks.ResourceRequirement(nil), p.resources...),
		State:     payload,
	}, nil
}

func (p *testProvider) Execute(_ context.Context, target PreparedTarget, _ Delivery) error {
	p.executes++
	p.payload, _ = target.State.(string)
	return nil
}

func TestRouterActorBoundSingleUseClaimsOnlyAtExecution(t *testing.T) {
	router, _ := newTestRouter(t)
	provider := &testProvider{
		scope:     tasks.ScopeIdentity{Owner: "plugin:test", Generation: 4},
		resources: []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
	}
	if _, err := router.Register("test", provider); err != nil {
		t.Fatal(err)
	}
	token, err := router.Issue(context.Background(), IssueRequest{
		Kind: "test", Payload: "payload", ActorID: 7, SingleUse: true, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !LooksLikeToken(token.ID) || len(token.ID) > 64 {
		t.Fatalf("token=%q is not Telegram-safe", token.ID)
	}
	if _, err := router.Prepare(context.Background(), token.ID, 8); !errors.Is(err, ErrTokenUnauthorized) {
		t.Fatalf("unauthorized Prepare() error=%v, want %v", err, ErrTokenUnauthorized)
	}

	first, err := router.Prepare(context.Background(), token.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	// A prepared token that never reaches admitted execution must remain usable.
	second, err := router.Prepare(context.Background(), token.ID, 7)
	if err != nil {
		t.Fatalf("second Prepare() before execution error=%v", err)
	}
	if first.Scope() != provider.scope || second.Scope() != provider.scope {
		t.Fatalf("prepared scope first=%+v second=%+v", first.Scope(), second.Scope())
	}
	resources := first.Resources()
	if len(resources) != 1 || resources[0].Name != "media" || resources[0].Amount != 1 {
		t.Fatalf("prepared resources=%+v", resources)
	}
	if err := router.ExecutePrepared(context.Background(), first, Delivery{ActorID: 7}); err != nil {
		t.Fatalf("ExecutePrepared() error=%v", err)
	}
	if provider.executes != 1 {
		t.Fatalf("provider executes=%d, want 1", provider.executes)
	}
	if _, err := router.Prepare(context.Background(), token.ID, 7); !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("Prepare(after single use) error=%v, want %v", err, ErrTokenConsumed)
	}
}

func TestRouterPublicReusableTokenSurvivesRestart(t *testing.T) {
	router, repo := newTestRouter(t)
	scope := tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}
	provider := &testProvider{scope: scope}
	if _, err := router.Register("test", provider); err != nil {
		t.Fatal(err)
	}
	token, err := router.Issue(context.Background(), IssueRequest{
		Kind: "test", Payload: "durable", TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewRouter(repo)
	restartedProvider := &testProvider{scope: scope}
	if _, err := restarted.Register("test", restartedProvider); err != nil {
		t.Fatal(err)
	}
	for actor := int64(41); actor <= 42; actor++ {
		prepared, err := restarted.Prepare(context.Background(), token.ID, actor)
		if err != nil {
			t.Fatalf("Prepare(actor=%d) error=%v", actor, err)
		}
		if err := restarted.ExecutePrepared(context.Background(), prepared, Delivery{ActorID: actor}); err != nil {
			t.Fatalf("ExecutePrepared(actor=%d) error=%v", actor, err)
		}
	}
	if restartedProvider.executes != 2 {
		t.Fatalf("reusable executes=%d, want 2", restartedProvider.executes)
	}
}

func TestRouterPreparedProviderGenerationFailsClosed(t *testing.T) {
	router, _ := newTestRouter(t)
	scope := tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}
	oldProvider := &testProvider{scope: scope}
	registration, err := router.Register("test", oldProvider)
	if err != nil {
		t.Fatal(err)
	}
	token, err := router.Issue(context.Background(), IssueRequest{
		Kind: "test", Payload: "generation", SingleUse: true, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := router.Prepare(context.Background(), token.ID, 7)
	if err != nil {
		t.Fatal(err)
	}

	registration.Close()
	newProvider := &testProvider{scope: tasks.ScopeIdentity{Owner: "plugin:test", Generation: 2}}
	if _, err := router.Register("test", newProvider); err != nil {
		t.Fatal(err)
	}
	if err := router.ExecutePrepared(context.Background(), prepared, Delivery{ActorID: 7}); !errors.Is(err, ErrProviderStale) {
		t.Fatalf("stale ExecutePrepared() error=%v, want %v", err, ErrProviderStale)
	}
	// Provider fencing happens before the durable claim, so the token is not burned.
	fresh, err := router.Prepare(context.Background(), token.ID, 7)
	if err != nil {
		t.Fatalf("Prepare(after provider replacement) error=%v", err)
	}
	if fresh.Scope() != newProvider.scope {
		t.Fatalf("fresh scope=%+v, want %+v", fresh.Scope(), newProvider.scope)
	}
}

func TestRouterExpiryFailsClosed(t *testing.T) {
	router, _ := newTestRouter(t)
	provider := &testProvider{scope: tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}}
	if _, err := router.Register("test", provider); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return base }
	token, err := router.Issue(context.Background(), IssueRequest{
		Kind: "test", Payload: "expires", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	router.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := router.Prepare(context.Background(), token.ID, 7); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired Prepare() error=%v, want %v", err, ErrTokenExpired)
	}
}

func TestMigrationStoresOpaqueRoutingPayloadOnly(t *testing.T) {
	router, repo := newTestRouter(t)
	provider := &testProvider{scope: tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}}
	if _, err := router.Register("test", provider); err != nil {
		t.Fatal(err)
	}
	token, err := router.Issue(context.Background(), IssueRequest{
		Kind: "test", Payload: "opaque-provider-state", TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload != "opaque-provider-state" || got.Kind != "test" {
		t.Fatalf("persisted token=%+v", got)
	}
}
