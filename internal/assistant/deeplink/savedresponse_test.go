package deeplink

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type savedDeepLinkResolver struct {
	response savedresponse.Response
}

func (r *savedDeepLinkResolver) ResolveSavedResponse(context.Context, savedresponse.Reference) (savedresponse.Response, bool, error) {
	return r.response, true, nil
}

type savedDeepLinkFixture struct {
	router       *Router
	provider     *SavedResponseProvider
	bindings     *savedresponse.BindingService
	binding      savedresponse.SurfaceBinding
	registry     *savedresponse.Registry
	registration *savedresponse.Registration
	scope        tasks.ScopeIdentity
}

func newSavedDeepLinkFixture(t *testing.T, response savedresponse.Response) *savedDeepLinkFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(
		ctx,
		db,
		MigrationProvider{},
		savedresponse.MigrationProvider{},
	); err != nil {
		t.Fatalf("RunFeatureMigrations() error=%v", err)
	}

	registry := savedresponse.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 3}
	registration, err := registry.Register("notes", scope, &savedDeepLinkResolver{response: response})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)

	bindings := savedresponse.NewBindingService(
		savedresponse.NewSQLiteSurfaceBindingRepository(db),
		registry,
	)
	binding, err := bindings.Create(ctx, savedresponse.SurfaceBinding{
		Surface:   savedresponse.SurfaceDeepLink,
		Alias:     "welcome",
		Reference: savedresponse.Reference{Provider: "notes", ScopeID: 7, Key: "welcome"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := NewSavedResponseProvider(
		bindings,
		savedresponse.NewResponseDelivery(savedresponse.NewService(nil)),
	)
	router := NewRouter(NewSQLiteRepository(db))
	if _, err := router.Register(SavedResponseKind, provider); err != nil {
		t.Fatal(err)
	}
	return &savedDeepLinkFixture{
		router: router, provider: provider, bindings: bindings, binding: binding,
		registry: registry, registration: registration, scope: scope,
	}
}

func TestSavedResponseDeepLinkIssuesExactBindingLeaseAndDelivers(t *testing.T) {
	fixture := newSavedDeepLinkFixture(t, savedresponse.NewText("Hello {id} in {chat}"))
	token, err := fixture.provider.Issue(
		context.Background(),
		fixture.router,
		"welcome",
		77,
		time.Hour,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.router.Prepare(context.Background(), token.ID, 77)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Scope() != fixture.scope {
		t.Fatalf("scope=%+v, want %+v", prepared.Scope(), fixture.scope)
	}
	if len(prepared.Resources()) != 0 {
		t.Fatalf("text resources=%+v, want none", prepared.Resources())
	}

	var sent string
	if err := fixture.router.ExecutePrepared(context.Background(), prepared, Delivery{
		ActorID: 77,
		ChatID:  990,
		SendText: func(text string) error {
			sent = text
			return nil
		},
	}); err != nil {
		t.Fatalf("ExecutePrepared() error=%v", err)
	}
	if sent != "Hello 77 in 990" {
		t.Fatalf("delivered text=%q", sent)
	}
}

func TestSavedResponseDeepLinkBindingMutationStalesIssuedToken(t *testing.T) {
	fixture := newSavedDeepLinkFixture(t, savedresponse.NewText("original"))
	token, err := fixture.provider.Issue(context.Background(), fixture.router, "welcome", 0, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.bindings.Get(context.Background(), savedresponse.SurfaceDeepLink, "welcome")
	if err != nil || current == nil {
		t.Fatalf("Get(binding)=%+v err=%v", current, err)
	}
	if _, err := fixture.bindings.Update(
		context.Background(),
		current.Surface,
		current.Alias,
		current.Reference,
		current.Enabled,
		current.Revision,
		current.Incarnation,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.router.Prepare(context.Background(), token.ID, 7); !errors.Is(err, savedresponse.ErrBindingStale) {
		t.Fatalf("Prepare(after mutation) error=%v, want %v", err, savedresponse.ErrBindingStale)
	}
}

func TestSavedResponseDeepLinkDeleteRecreateABAFailsClosed(t *testing.T) {
	fixture := newSavedDeepLinkFixture(t, savedresponse.NewText("original"))
	token, err := fixture.provider.Issue(context.Background(), fixture.router, "welcome", 0, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	old := fixture.binding
	if err := fixture.bindings.Delete(
		context.Background(),
		old.Surface,
		old.Alias,
		old.Revision,
		old.Incarnation,
	); err != nil {
		t.Fatal(err)
	}
	recreated, err := fixture.bindings.Create(context.Background(), savedresponse.SurfaceBinding{
		Surface:   savedresponse.SurfaceDeepLink,
		Alias:     old.Alias,
		Reference: old.Reference,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Incarnation == old.Incarnation {
		t.Fatal("delete+recreate reused binding incarnation")
	}
	if _, err := fixture.router.Prepare(context.Background(), token.ID, 7); !errors.Is(err, savedresponse.ErrBindingStale) {
		t.Fatalf("Prepare(after recreate) error=%v, want %v", err, savedresponse.ErrBindingStale)
	}
}

func TestSavedResponseDeepLinkCarriesMediaResourceBeforeExecution(t *testing.T) {
	fixture := newSavedDeepLinkFixture(t, savedresponse.Response{
		Text: "caption",
		Media: &savedresponse.MediaRef{
			AssetID: "asset-1", MediaType: "photo", Name: "photo.jpg", MIMEType: "image/jpeg",
		},
	})
	token, err := fixture.provider.Issue(context.Background(), fixture.router, "welcome", 0, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.router.Prepare(context.Background(), token.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	resources := prepared.Resources()
	if len(resources) != 1 || resources[0].Name != "media" || resources[0].Amount != 1 {
		t.Fatalf("media resources=%+v, want media:1", resources)
	}
}

func TestSavedResponseDeepLinkQueuedProviderReloadFailsClosed(t *testing.T) {
	fixture := newSavedDeepLinkFixture(t, savedresponse.NewText("old"))
	token, err := fixture.provider.Issue(context.Background(), fixture.router, "welcome", 0, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.router.Prepare(context.Background(), token.ID, 7)
	if err != nil {
		t.Fatal(err)
	}

	fixture.registration.Close()
	reloaded, err := fixture.registry.Register(
		"notes",
		tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 4},
		&savedDeepLinkResolver{response: savedresponse.NewText("new")},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	if err := fixture.router.ExecutePrepared(context.Background(), prepared, Delivery{
		ActorID: 7,
		ChatID:  7,
		SendText: func(string) error { return nil },
	}); !errors.Is(err, savedresponse.ErrBindingStale) {
		t.Fatalf("ExecutePrepared(after provider reload) error=%v, want %v", err, savedresponse.ErrBindingStale)
	}
}


func TestSavedResponseDeepLinkMediaDeliveryUsesSharedLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(
		ctx,
		db,
		MigrationProvider{},
		savedresponse.MigrationProvider{},
	); err != nil {
		t.Fatal(err)
	}

	store := storage.NewMemoryStorage()
	asset, err := store.Put(ctx, bytes.NewBufferString("photo-bytes"), storage.Metadata{
		Name: "photo.jpg", MIME: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responseService := savedresponse.NewService(store)
	responseService.SetFiles(files.ForOwner("deeplink-media-test"))

	response := savedresponse.Response{
		Text: "caption {id}",
		Media: &savedresponse.MediaRef{
			AssetID: asset.ID, MediaType: "photo", Name: asset.Name, MIMEType: asset.MIME,
		},
	}
	registry := savedresponse.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:media", Generation: 1}
	registration, err := registry.Register("media", scope, &savedDeepLinkResolver{response: response})
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	bindings := savedresponse.NewBindingService(
		savedresponse.NewSQLiteSurfaceBindingRepository(db),
		registry,
	)
	if _, err := bindings.Create(ctx, savedresponse.SurfaceBinding{
		Surface: savedresponse.SurfaceDeepLink,
		Alias: "photo",
		Reference: savedresponse.Reference{Provider: "media", ScopeID: 1, Key: "photo"},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	provider := NewSavedResponseProvider(bindings, savedresponse.NewResponseDelivery(responseService))
	router := NewRouter(NewSQLiteRepository(db))
	if _, err := router.Register(SavedResponseKind, provider); err != nil {
		t.Fatal(err)
	}
	token, err := provider.Issue(ctx, router, "photo", 7, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := router.Prepare(ctx, token.ID, 7)
	if err != nil {
		t.Fatal(err)
	}

	var deliveredPath, deliveredCaption string
	if err := router.ExecutePrepared(ctx, prepared, Delivery{
		ActorID: 7,
		ChatID:  9,
		SendMedia: func(mediaType, path, caption string) error {
			if mediaType != "photo" {
				t.Fatalf("media type=%q, want photo", mediaType)
			}
			deliveredPath = path
			deliveredCaption = caption
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("materialized media unavailable during delivery: %v", err)
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("ExecutePrepared(media) error=%v", err)
	}
	if deliveredCaption != "caption 7" {
		t.Fatalf("caption=%q, want %q", deliveredCaption, "caption 7")
	}
	if deliveredPath == "" {
		t.Fatal("media delivery did not receive a materialized path")
	}
	if _, err := os.Stat(deliveredPath); !os.IsNotExist(err) {
		t.Fatalf("materialized media survived delivery cleanup: %v", err)
	}
}
