package savedresponse

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/inipew/goultroid/internal/platform/filesystem"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

func newInlineSourceFixture(
	t *testing.T,
	alias string,
	response Response,
	service *Service,
) (*InlineSource, *Registry, *Registration, *bindingTestResolver, tasks.ScopeIdentity) {
	t.Helper()
	repo, _ := newSurfaceBindingRepository(t)
	registry := NewRegistry()
	resolver := &bindingTestResolver{response: response}
	scope := tasks.ScopeIdentity{Owner: "plugin:inline-saved", Generation: 1}
	registration, err := registry.Register("saved", scope, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)

	bindings := NewBindingService(repo, registry)
	if _, err := bindings.Create(context.Background(), SurfaceBinding{
		Surface:   SurfaceInline,
		Alias:     alias,
		Reference: Reference{Provider: "saved", ScopeID: 7, Key: alias},
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}
	return NewInlineSource(bindings, service), registry, registration, resolver, scope
}

func TestInlineSourceTextBinding(t *testing.T) {
	source, _, _, _, scope := newInlineSourceFixture(
		t,
		"hello",
		NewText("Hello {id}"),
		NewService(nil),
	)
	prepared, matched, err := source.Prepare(context.Background(), "hello ignored args")
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("inline binding did not match")
	}
	if prepared.Scope != scope {
		t.Fatalf("scope=%+v, want %+v", prepared.Scope, scope)
	}
	if len(prepared.Resources) != 0 {
		t.Fatalf("text resources=%+v, want none", prepared.Resources)
	}
	if len(prepared.Args) != 2 || prepared.Args[0] != "ignored" || prepared.Args[1] != "args" {
		t.Fatalf("args=%v", prepared.Args)
	}

	resp, err := source.Execute(context.Background(), prepared, &inlineservice.InlineContext{UserID: 12345})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Type != inlineservice.ResultArticle {
		t.Fatalf("unexpected inline response: %+v", resp)
	}
	if resp.Results[0].Text != "Hello 12345" {
		t.Fatalf("text=%q, want %q", resp.Results[0].Text, "Hello 12345")
	}
	if !resp.Private || resp.Cache != inlineservice.CacheNone {
		t.Fatalf("saved inline response must be private/no-cache: %+v", resp)
	}
}

func TestInlineSourceMediaBindingMaterializesAndCleans(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	asset, err := store.Put(ctx, bytes.NewBufferString("image-bytes"), storage.Metadata{
		Name: "photo.jpg",
		MIME: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responses := NewService(store)
	responses.SetFiles(files.ForOwner("saved-inline-test"))

	response := Response{
		Text: "Photo for {id}",
		Media: &MediaRef{
			AssetID:   asset.ID,
			MediaType: "photo",
			Name:      asset.Name,
			MIMEType:  asset.MIME,
		},
	}
	source, _, _, _, scope := newInlineSourceFixture(t, "photo", response, responses)
	prepared, matched, err := source.Prepare(ctx, "photo")
	if err != nil {
		t.Fatal(err)
	}
	if !matched || prepared.Scope != scope {
		t.Fatalf("prepared=%+v matched=%v", prepared, matched)
	}
	if len(prepared.Resources) != 1 ||
		prepared.Resources[0].Name != "media" ||
		prepared.Resources[0].Amount != 1 {
		t.Fatalf("media resources=%+v, want media:1", prepared.Resources)
	}

	resp, err := source.Execute(ctx, prepared, &inlineservice.InlineContext{UserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results=%d, want 1", len(resp.Results))
	}
	result := resp.Results[0]
	if result.Type != inlineservice.ResultPhoto || result.Text != "Photo for 42" || result.LocalMedia == nil {
		t.Fatalf("unexpected media inline result: %+v", result)
	}
	path := result.LocalMedia.Path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("materialized media unavailable before finalize: %v", err)
	}
	if resp.Finalize == nil {
		t.Fatal("media response has no finalizer")
	}
	resp.Finalize()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("materialized media survived finalize: %v", err)
	}
}

func TestInlineSourcePreparedBindingFailsClosedAcrossProviderReload(t *testing.T) {
	source, registry, registration, _, _ := newInlineSourceFixture(
		t,
		"reload",
		NewText("old"),
		NewService(nil),
	)
	prepared, matched, err := source.Prepare(context.Background(), "reload")
	if err != nil || !matched {
		t.Fatalf("prepare matched=%v err=%v", matched, err)
	}

	registration.Close()
	reloaded, err := registry.Register(
		"saved",
		tasks.ScopeIdentity{Owner: "plugin:inline-saved", Generation: 2},
		&bindingTestResolver{response: NewText("new")},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	if _, err := source.Execute(
		context.Background(),
		prepared,
		&inlineservice.InlineContext{UserID: 1},
	); !errors.Is(err, ErrBindingStale) {
		t.Fatalf("Execute(after provider reload) error=%v, want %v", err, ErrBindingStale)
	}
}

func TestInlineSourceUnknownAliasDoesNotClaimQuery(t *testing.T) {
	source, _, _, _, _ := newInlineSourceFixture(
		t,
		"known",
		NewText("known"),
		NewService(nil),
	)
	_, matched, err := source.Prepare(context.Background(), "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("unknown SavedResponse alias was claimed")
	}
}

func TestInlineSourceRejectsMediaPlusSeparateText(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	asset, err := store.Put(ctx, bytes.NewBufferString("sticker-bytes"), storage.Metadata{
		Name: "sticker.webp",
		MIME: "image/webp",
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responses := NewService(store)
	responses.SetFiles(files.ForOwner("saved-inline-sticker-test"))

	source, _, _, _, _ := newInlineSourceFixture(t, "sticker", Response{
		Text: "separate sticker text",
		Media: &MediaRef{
			AssetID:   asset.ID,
			MediaType: "sticker",
			Name:      asset.Name,
			MIMEType:  asset.MIME,
		},
	}, responses)
	prepared, matched, err := source.Prepare(ctx, "sticker")
	if err != nil || !matched {
		t.Fatalf("prepare matched=%v err=%v", matched, err)
	}
	if _, err := source.Execute(ctx, prepared, &inlineservice.InlineContext{UserID: 1}); !errors.Is(err, ErrInlineMediaNotRepresentable) {
		t.Fatalf("Execute(sticker+text) error=%v, want %v", err, ErrInlineMediaNotRepresentable)
	}
}
