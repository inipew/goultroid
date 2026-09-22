package savedresponse

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/tasks"
)

func newSurfaceBindingRepository(t *testing.T) (*SQLiteSurfaceBindingRepository, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error = %v", err)
	}
	return NewSQLiteSurfaceBindingRepository(db), db
}

func TestSurfaceBindingMigrationStoresReferencesOnly(t *testing.T) {
	_, db := newSurfaceBindingRepository(t)

	var tableCount int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'saved_response_surface_bindings'
	`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatalf("surface binding table count=%d, want 1", tableCount)
	}

	for _, forbidden := range []string{"content", "response_text", "media_asset_id", "media_type", "media_name", "media_mime"} {
		var count int
		if err := db.QueryRow(`
			SELECT count(*) FROM pragma_table_info('saved_response_surface_bindings')
			WHERE name = ?
		`, forbidden).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("surface binding table must not duplicate response payload column %q", forbidden)
		}
	}

	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatalf("second RunFeatureMigrations() error = %v", err)
	}
}

func TestSQLiteSurfaceBindingRepositoryCASLifecycle(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()

	created, err := repo.CreateBinding(ctx, SurfaceBinding{
		Surface: SurfaceAssistantCommand,
		Alias:   " Welcome ",
		Reference: Reference{
			Provider: " NOTES ",
			ScopeID:  42,
			Key:      " greeting ",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateBinding() error = %v", err)
	}
	if created.Surface != SurfaceAssistantCommand || created.Alias != "welcome" ||
		created.Reference.Provider != "notes" || created.Reference.Key != "greeting" ||
		created.Revision != 1 || !created.Enabled {
		t.Fatalf("unexpected created binding: %+v", created)
	}
	if _, err := repo.CreateBinding(ctx, created); !errors.Is(err, ErrBindingExists) {
		t.Fatalf("duplicate CreateBinding() error = %v, want %v", err, ErrBindingExists)
	}

	disabled, err := repo.SetBindingEnabled(ctx, created.Surface, created.Alias, false, created.Revision, created.Incarnation)
	if err != nil {
		t.Fatalf("SetBindingEnabled(false) error = %v", err)
	}
	if disabled.Enabled || disabled.Revision != 2 {
		t.Fatalf("unexpected disabled binding: %+v", disabled)
	}
	if _, err := repo.SetBindingEnabled(ctx, created.Surface, created.Alias, true, created.Revision, created.Incarnation); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("stale enable error = %v, want %v", err, ErrBindingConflict)
	}

	updated, err := repo.UpdateBinding(
		ctx,
		disabled.Surface,
		disabled.Alias,
		Reference{Provider: disabled.Reference.Provider, ScopeID: 84, Key: "replacement"},
		disabled.Enabled,
		disabled.Revision,
		disabled.Incarnation,
	)
	if err != nil {
		t.Fatalf("UpdateBinding() error = %v", err)
	}
	if updated.Revision != 3 || updated.Reference.ScopeID != 84 || updated.Reference.Key != "replacement" {
		t.Fatalf("unexpected updated binding: %+v", updated)
	}

	all, err := repo.ListBindings(ctx, SurfaceAssistantCommand, true, 10)
	if err != nil {
		t.Fatalf("ListBindings(all) error = %v", err)
	}
	if len(all) != 1 || all[0].Alias != "welcome" {
		t.Fatalf("unexpected all bindings: %+v", all)
	}
	enabledOnly, err := repo.ListBindings(ctx, SurfaceAssistantCommand, false, 10)
	if err != nil {
		t.Fatalf("ListBindings(enabled) error = %v", err)
	}
	if len(enabledOnly) != 0 {
		t.Fatalf("disabled binding leaked into enabled listing: %+v", enabledOnly)
	}

	if err := repo.DeleteBinding(ctx, updated.Surface, updated.Alias, updated.Revision-1, updated.Incarnation); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("stale DeleteBinding() error = %v, want %v", err, ErrBindingConflict)
	}
	if err := repo.DeleteBinding(ctx, updated.Surface, updated.Alias, updated.Revision, updated.Incarnation); err != nil {
		t.Fatalf("DeleteBinding() error = %v", err)
	}
	got, err := repo.GetBinding(ctx, updated.Surface, updated.Alias)
	if err != nil {
		t.Fatalf("GetBinding(after delete) error = %v", err)
	}
	if got != nil {
		t.Fatalf("deleted binding still present: %+v", got)
	}
}

type bindingTestResolver struct {
	response Response
}

func (r *bindingTestResolver) ResolveSavedResponse(context.Context, Reference) (Response, bool, error) {
	return r.response, true, nil
}

func TestBindingServiceJoinsDurableBindingToLiveProviderGeneration(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()
	binding, err := repo.CreateBinding(ctx, SurfaceBinding{
		Surface:   SurfaceInline,
		Alias:     "card",
		Reference: Reference{Provider: "notes", ScopeID: 7, Key: "card"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 9}
	registration, err := registry.Register("notes", scope, &bindingTestResolver{response: NewText("authoritative")})
	if err != nil {
		t.Fatal(err)
	}
	service := NewBindingService(repo, registry)

	resolved, err := service.Resolve(ctx, SurfaceInline, " CARD ")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Binding.Revision != binding.Revision || resolved.Resolved.Scope != scope ||
		resolved.Resolved.Response.Text != "authoritative" {
		t.Fatalf("unexpected resolved binding: %+v", resolved)
	}

	resolved.Resolved.Response.Text = "mutated"
	again, err := service.Resolve(ctx, SurfaceInline, "card")
	if err != nil {
		t.Fatalf("Resolve(second) error = %v", err)
	}
	if again.Resolved.Response.Text != "authoritative" {
		t.Fatalf("provider payload mutated through binding result: %q", again.Resolved.Response.Text)
	}

	disabled, err := repo.SetBindingEnabled(ctx, binding.Surface, binding.Alias, false, binding.Revision, binding.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, SurfaceInline, binding.Alias); !errors.Is(err, ErrBindingDisabled) {
		t.Fatalf("disabled Resolve() error = %v, want %v", err, ErrBindingDisabled)
	}

	registration.Close()
	if _, err := repo.SetBindingEnabled(ctx, disabled.Surface, disabled.Alias, true, disabled.Revision, disabled.Incarnation); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, SurfaceInline, binding.Alias); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("Resolve(after provider unload) error = %v, want %v", err, ErrResolverUnavailable)
	}
}

func TestBindingServiceMutationRevalidatesProviderButAllowsOfflineCleanup(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()
	registry := NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 3}
	registration, err := registry.Register("notes", scope, &bindingTestResolver{response: NewText("live")})
	if err != nil {
		t.Fatal(err)
	}
	service := NewBindingService(repo, registry)

	created, err := service.Create(ctx, SurfaceBinding{
		Surface:   SurfaceAssistantCommand,
		Alias:     "hello",
		Reference: Reference{Provider: "notes", ScopeID: 10, Key: "hello"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	registration.Close()

	if _, err := service.Update(
		ctx,
		created.Surface,
		created.Alias,
		Reference{Provider: created.Reference.Provider, ScopeID: created.Reference.ScopeID, Key: "replacement"},
		created.Enabled,
		created.Revision,
		created.Incarnation,
	); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("Update(with provider offline) error = %v, want %v", err, ErrResolverUnavailable)
	}
	current, err := service.Get(ctx, created.Surface, created.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Reference.Key != "hello" || current.Revision != created.Revision {
		t.Fatalf("failed update mutated durable binding: %+v", current)
	}

	disabled, err := service.SetEnabled(ctx, created.Surface, created.Alias, false, created.Revision, created.Incarnation)
	if err != nil {
		t.Fatalf("SetEnabled(false with provider offline) error = %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("binding remained enabled: %+v", disabled)
	}
	if _, err := service.SetEnabled(ctx, disabled.Surface, disabled.Alias, true, disabled.Revision, disabled.Incarnation); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("SetEnabled(true with provider offline) error = %v, want %v", err, ErrResolverUnavailable)
	}
	if err := service.Delete(ctx, disabled.Surface, disabled.Alias, disabled.Revision, disabled.Incarnation); err != nil {
		t.Fatalf("Delete(with provider offline) error = %v", err)
	}
}

func TestPreparedBindingRevalidatesRevisionAndProviderGeneration(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()
	registry := NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1}
	resolver1 := &bindingTestResolver{response: NewText("v1")}
	registration1, err := registry.Register("notes", scope1, resolver1)
	if err != nil {
		t.Fatal(err)
	}
	service := NewBindingService(repo, registry)

	created, err := service.Create(ctx, SurfaceBinding{
		Surface:   SurfaceAssistantCommand,
		Alias:     "queued",
		Reference: Reference{Provider: "notes", ScopeID: 77, Key: "queued"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(ctx, created.Surface, created.Alias)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Scope() != scope1 || prepared.Binding().Revision != created.Revision {
		t.Fatalf("unexpected prepared binding: scope=%+v binding=%+v", prepared.Scope(), prepared.Binding())
	}

	// Response content is provider-owned. A content update that does not change
	// routing identity should be observed at execution time, not frozen while queued.
	resolver1.response = NewText("v2")
	resolved, err := service.ResolvePrepared(ctx, prepared)
	if err != nil {
		t.Fatalf("ResolvePrepared(latest content) error = %v", err)
	}
	if resolved.Resolved.Response.Text != "v2" {
		t.Fatalf("prepared execution used stale response content %q", resolved.Resolved.Response.Text)
	}

	if _, err := service.Update(
		ctx,
		created.Surface,
		created.Alias,
		Reference{Provider: created.Reference.Provider, ScopeID: created.Reference.ScopeID, Key: "other"},
		created.Enabled,
		created.Revision,
		created.Incarnation,
	); err != nil {
		t.Fatalf("Update(rebind) error = %v", err)
	}
	if _, err := service.ResolvePrepared(ctx, prepared); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("ResolvePrepared(after rebind) error = %v, want %v", err, ErrBindingStale)
	}

	reloadBinding, err := service.Create(ctx, SurfaceBinding{
		Surface:   SurfaceInline,
		Alias:     "reload",
		Reference: Reference{Provider: "notes", ScopeID: 77, Key: "reload"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	preparedReload, err := service.Prepare(ctx, reloadBinding.Surface, reloadBinding.Alias)
	if err != nil {
		t.Fatal(err)
	}
	registration1.Close()
	scope2 := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 2}
	registration2, err := registry.Register("notes", scope2, &bindingTestResolver{response: NewText("reloaded")})
	if err != nil {
		t.Fatal(err)
	}
	defer registration2.Close()

	if _, err := service.ResolvePrepared(ctx, preparedReload); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("ResolvePrepared(after provider reload) error = %v, want %v", err, ErrBindingStale)
	}
}

func TestUpdateBindingCannotRenameIntoAnotherIdentity(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()

	first, err := repo.CreateBinding(ctx, SurfaceBinding{
		Surface:   SurfaceAssistantCommand,
		Alias:     "first",
		Reference: Reference{Provider: "notes", ScopeID: 1, Key: "one"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateBinding(ctx, SurfaceBinding{
		Surface:   SurfaceAssistantCommand,
		Alias:     "second",
		Reference: Reference{Provider: "notes", ScopeID: 2, Key: "two"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("test requires equal starting revisions: first=%d second=%d", first.Revision, second.Revision)
	}

	updated, err := repo.UpdateBinding(
		ctx,
		first.Surface,
		first.Alias,
		Reference{Provider: "notes", ScopeID: 3, Key: "updated"},
		true,
		first.Revision,
		first.Incarnation,
	)
	if err != nil {
		t.Fatalf("UpdateBinding(first) error = %v", err)
	}
	if updated.Alias != "first" {
		t.Fatalf("binding identity changed unexpectedly: %+v", updated)
	}
	untouched, err := repo.GetBinding(ctx, second.Surface, second.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if untouched == nil || untouched.Reference.ScopeID != 2 || untouched.Reference.Key != "two" ||
		untouched.Revision != second.Revision {
		t.Fatalf("second binding was modified by first update: %+v", untouched)
	}
}

func TestPreparedBindingRejectsResourceClassDrift(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()
	registry := NewRegistry()
	resolver := &bindingTestResolver{response: NewText("text")}
	registration, err := registry.Register(
		"notes",
		tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1},
		resolver,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	service := NewBindingService(repo, registry)
	created, err := service.Create(ctx, SurfaceBinding{
		Surface:   SurfaceAssistantCommand,
		Alias:     "resource-drift",
		Reference: Reference{Provider: "notes", ScopeID: 9, Key: "resource-drift"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(ctx, created.Surface, created.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.HasMedia() {
		t.Fatal("text response unexpectedly requires media resource")
	}

	resolver.response = Response{
		Text: "caption",
		Media: &MediaRef{
			AssetID:   "asset-1",
			MediaType: "photo",
			Name:      "photo.jpg",
			MIMEType:  "image/jpeg",
		},
	}
	if _, err := service.ResolvePrepared(ctx, prepared); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("ResolvePrepared(resource drift) error = %v, want %v", err, ErrBindingStale)
	}
}

func TestPreparedBindingRejectsDeleteRecreateABA(t *testing.T) {
	repo, _ := newSurfaceBindingRepository(t)
	ctx := context.Background()
	registry := NewRegistry()
	registration, err := registry.Register(
		"notes",
		tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1},
		&bindingTestResolver{response: NewText("same")},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	service := NewBindingService(repo, registry)
	original, err := service.Create(ctx, SurfaceBinding{
		Surface:   SurfaceAssistantCommand,
		Alias:     "aba",
		Reference: Reference{Provider: "notes", ScopeID: 12, Key: "same"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(ctx, original.Surface, original.Alias)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Delete(ctx, original.Surface, original.Alias, original.Revision, original.Incarnation); err != nil {
		t.Fatal(err)
	}
	recreated, err := service.Create(ctx, SurfaceBinding{
		Surface:   original.Surface,
		Alias:     original.Alias,
		Reference: original.Reference,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Revision != original.Revision {
		t.Fatalf("ABA test requires reset revision: original=%d recreated=%d", original.Revision, recreated.Revision)
	}
	if recreated.Incarnation == "" || recreated.Incarnation == original.Incarnation {
		t.Fatalf("binding incarnation was not renewed: original=%q recreated=%q", original.Incarnation, recreated.Incarnation)
	}
	if _, err := service.ResolvePrepared(ctx, prepared); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("ResolvePrepared(delete+recreate) error = %v, want %v", err, ErrBindingStale)
	}
}
