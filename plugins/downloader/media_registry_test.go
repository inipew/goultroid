package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func newDownloaderRegistryTest(t *testing.T) (*mediaregistry.Registry, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, mediaregistry.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	return mediaregistry.New(db), db
}

func TestRegisterRetainedAssetTracksLifecycleWithoutReference(t *testing.T) {
	ctx := context.Background()
	registry, _ := newDownloaderRegistryTest(t)
	store := storage.NewMemoryStorage()
	asset, err := store.Put(ctx, strings.NewReader("download"), storage.Metadata{Name: "song.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	p := New(store, registry)
	if err := p.registerRetainedAsset(ctx, store, asset, "downloader.http"); err != nil {
		t.Fatal(err)
	}
	record, err := registry.Asset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Producer != "downloader.http" || record.Owner != downloaderRegistryOwner || record.Lifecycle != mediaregistry.LifecycleRetained {
		t.Fatalf("unexpected retained ownership metadata: %+v", record)
	}
	refs, err := registry.ReferenceCount(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatalf("retained asset reference count=%d, want 0", refs)
	}
}

func TestRegisterRetainedAssetConflictRollsBackPhysicalAsset(t *testing.T) {
	ctx := context.Background()
	registry, _ := newDownloaderRegistryTest(t)
	store := storage.NewMemoryStorage()
	asset, err := store.Put(ctx, strings.NewReader("download"), storage.Metadata{Name: "collision.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAsset(ctx, mediaregistry.AssetRegistration{
		AssetID: asset.ID, Producer: "other", Owner: "other", Lifecycle: mediaregistry.LifecyclePersistent,
	}); err != nil {
		t.Fatal(err)
	}
	p := New(store, registry)
	err = p.registerRetainedAsset(ctx, store, asset, "downloader.http")
	if !errors.Is(err, mediaregistry.ErrOwnershipConflict) {
		t.Fatalf("registration error=%v, want ownership conflict", err)
	}
	if _, err := store.Stat(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("conflicting retained asset was not rolled back: %v", err)
	}
}

func TestTelegramDownloadCreatesManagedRetainedAsset(t *testing.T) {
	ctx := context.Background()
	registry, _ := newDownloaderRegistryTest(t)
	store, err := storage.NewFileStorage(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	p := New(store, registry)
	attachDownloaderTestFilesystem(t, p)
	tgSvc := &mockTelegramService{}
	coreCtx := &core.Context{
		Ctx:    ctx,
		Svc:    tgSvc,
		PeerID: &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true, Media: &core.MediaInfo{
			Type: "audio", FileName: "managed.mp3", MimeType: "audio/mpeg", Size: 19,
			Location: &tg.InputDocumentFileLocation{},
		}},
	}

	if err := p.executeMediaDownload(ctx, coreCtx, store.BasePath(), 19); err != nil {
		t.Fatal(err)
	}
	page, err := store.Enumerate(ctx, storage.EnumerationOptions{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].State != storage.EnumerationManaged || page.Entries[0].Asset == nil {
		t.Fatalf("telegram download did not create exactly one managed asset: %+v", page.Entries)
	}
	assetID := page.Entries[0].Asset.ID
	record, err := registry.Asset(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Producer != downloaderTelegramProducer || record.Owner != downloaderRegistryOwner || record.Lifecycle != mediaregistry.LifecycleRetained {
		t.Fatalf("unexpected telegram ownership metadata: %+v", record)
	}
}
