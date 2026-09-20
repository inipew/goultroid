package savedresponse

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestRenderHTMLSafeVariablesAndBounds(t *testing.T) {
	vars := TemplateVars{
		Name: "Alice <Admin>",
		First: "Alice",
		Username: "alice",
		UserID: 42,
		Chat: "Group & Friends",
		ChatID: -1001,
		Now: time.Date(2026, 9, 20, 12, 34, 56, 0, time.UTC),
	}
	got, err := RenderHTML("Hi {mention} in {chat} on {date} {time}; {unknown}", vars, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Alice &lt;Admin&gt;") || !strings.Contains(got, "Group &amp; Friends") {
		t.Fatalf("variables were not escaped: %q", got)
	}
	if !strings.Contains(got, `{unknown}`) {
		t.Fatalf("unknown token should be preserved: %q", got)
	}
	if _, err := RenderHTML(strings.Repeat("{name}", 100), TemplateVars{Name: strings.Repeat("x", 100)}, 10); err == nil {
		t.Fatal("expected rendered output limit error")
	}
}

func TestVarsFromEnvelope(t *testing.T) {
	vars := VarsFromEnvelope(&core.MessageEnvelope{
		Sender: core.User{ID: 9, FirstName: "Ada", LastName: "Lovelace", Username: "ada"},
		Chat: core.Chat{ID: 7, Title: "Math"},
		ChatID: 7,
	}, time.Unix(0, 0))
	if vars.Name != "Ada Lovelace" || vars.UserID != 9 || vars.Chat != "Math" || vars.ChatID != 7 {
		t.Fatalf("unexpected vars: %+v", vars)
	}
}

func TestCommitReplacementCleansAssets(t *testing.T) {
	store := storage.NewMemoryStorage()
	svc := NewService(store)
	oldAsset, err := store.Put(context.Background(), strings.NewReader("old"), storage.Metadata{Name: "old.txt"})
	if err != nil {
		t.Fatal(err)
	}
	newAsset, err := store.Put(context.Background(), strings.NewReader("new"), storage.Metadata{Name: "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	oldResp := Response{Media: &MediaRef{AssetID: oldAsset.ID}}
	newResp := Response{Media: &MediaRef{AssetID: newAsset.ID}}
	if err := svc.CommitReplacement(context.Background(), oldResp, newResp, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), oldAsset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old asset still present: %v", err)
	}
	if _, err := store.Stat(context.Background(), newAsset.ID); err != nil {
		t.Fatalf("new asset missing: %v", err)
	}
}

func TestRenderPlainEscapesLiteralHTMLAndVariables(t *testing.T) {
	got, err := Render(NewPlainText("2 < 3 & hello {name}"), TemplateVars{Name: "Alice <Admin>"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := "2 &lt; 3 &amp; hello Alice &lt;Admin&gt;"
	if got != want {
		t.Fatalf("Render plain=%q, want %q", got, want)
	}

	htmlText, err := Render(NewHTML("<b>{name}</b>"), TemplateVars{Name: "Alice <Admin>"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if htmlText != "<b>Alice &lt;Admin&gt;</b>" {
		t.Fatalf("Render HTML=%q", htmlText)
	}
}

func TestRenderRejectsTooManyTemplateTokens(t *testing.T) {
	template := strings.Repeat("{name}", MaxTemplateTokens+1)
	_, err := Render(NewHTML(template), TemplateVars{Name: "Alice"}, 4096)
	if !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("error=%v, want ErrTooManyTokens", err)
	}
}

func newPreparedTestService(t *testing.T) (*Service, storage.Storage) {
	t.Helper()
	store := storage.NewMemoryStorage()
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	svc.SetFiles(manager.ForOwner("savedresponse-test"))
	return svc, store
}

func putPreparedAsset(t *testing.T, store storage.Storage, name, content string) *storage.Asset {
	t.Helper()
	asset, err := store.Put(context.Background(), strings.NewReader(content), storage.Metadata{Name: name, MIME: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func TestPrepareFallsBackToStandaloneTextWhenCaptionTooLong(t *testing.T) {
	svc, store := newPreparedTestService(t)
	asset := putPreparedAsset(t, store, "photo.png", "fake image bytes")
	response := NewPlainText(strings.Repeat("x", MaxCaptionRunes+20))
	response.Media = &MediaRef{AssetID: asset.ID, MediaType: "photo", Name: asset.Name, MIMEType: asset.MIME}

	prepared, err := svc.Prepare(context.Background(), response, TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()

	if prepared.Caption != "" {
		t.Fatalf("caption should be empty after overflow fallback, got %d chars", len([]rune(prepared.Caption)))
	}
	if len([]rune(prepared.Text)) != MaxCaptionRunes+20 {
		t.Fatalf("standalone text runes=%d", len([]rune(prepared.Text)))
	}
	if prepared.MediaPath == "" || prepared.MediaType != "photo" {
		t.Fatalf("unexpected prepared media: %+v", prepared)
	}
	if _, err := os.Stat(prepared.MediaPath); err != nil {
		t.Fatalf("materialized media missing before cleanup: %v", err)
	}
	path := prepared.MediaPath
	prepared.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("materialized media still exists after cleanup: %v", err)
	}
}

func TestPrepareStickerUsesStandaloneText(t *testing.T) {
	svc, store := newPreparedTestService(t)
	asset := putPreparedAsset(t, store, "sticker.webp", "fake sticker bytes")
	response := NewHTML("<b>Hello {name}</b>")
	response.Media = &MediaRef{AssetID: asset.ID, MediaType: "sticker", Name: asset.Name, MIMEType: "image/webp"}

	prepared, err := svc.Prepare(context.Background(), response, TemplateVars{Name: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()

	if prepared.Caption != "" {
		t.Fatalf("sticker must not carry caption: %q", prepared.Caption)
	}
	if prepared.Text != "<b>Hello Alice</b>" {
		t.Fatalf("unexpected standalone sticker text: %q", prepared.Text)
	}
	if prepared.MediaType != "sticker" || prepared.MediaPath == "" {
		t.Fatalf("unexpected prepared sticker: %+v", prepared)
	}
}

func TestResponseCloneDetachesMediaAndDefaultsFormat(t *testing.T) {
	original := Response{
		Text: "hello",
		Media: &MediaRef{AssetID: "asset-1", MediaType: "photo"},
	}
	if original.EffectiveFormat() != FormatHTML {
		t.Fatalf("effective format=%q, want %q", original.EffectiveFormat(), FormatHTML)
	}
	if !original.HasMedia() {
		t.Fatal("expected media")
	}

	cloned := original.Clone()
	cloned.Media.AssetID = "asset-2"
	if original.Media.AssetID != "asset-1" {
		t.Fatalf("clone mutated original media: %+v", original.Media)
	}
}

func TestCommitReplacementPersistFailureKeepsSharedAsset(t *testing.T) {
	store := storage.NewMemoryStorage()
	svc := NewService(store)
	asset, err := store.Put(context.Background(), strings.NewReader("existing"), storage.Metadata{Name: "same.bin"})
	if err != nil {
		t.Fatal(err)
	}
	previous := Response{Media: &MediaRef{AssetID: asset.ID}}
	next := previous.Clone()
	want := errors.New("db write failed")

	if err := svc.CommitReplacement(context.Background(), previous, next, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("CommitReplacement error=%v, want %v", err, want)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("persist failure deleted previously committed shared asset: %v", err)
	}
}

func TestCommitReplacementPersistFailureDeletesOnlyNewAsset(t *testing.T) {
	store := storage.NewMemoryStorage()
	svc := NewService(store)
	oldAsset, err := store.Put(context.Background(), strings.NewReader("old"), storage.Metadata{Name: "old.bin"})
	if err != nil {
		t.Fatal(err)
	}
	newAsset, err := store.Put(context.Background(), strings.NewReader("new"), storage.Metadata{Name: "new.bin"})
	if err != nil {
		t.Fatal(err)
	}
	previous := Response{Media: &MediaRef{AssetID: oldAsset.ID}}
	next := Response{Media: &MediaRef{AssetID: newAsset.ID}}
	want := errors.New("db write failed")

	if err := svc.CommitReplacement(context.Background(), previous, next, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("CommitReplacement error=%v, want %v", err, want)
	}
	if _, err := store.Stat(context.Background(), oldAsset.ID); err != nil {
		t.Fatalf("old committed asset was removed: %v", err)
	}
	if _, err := store.Stat(context.Background(), newAsset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("new uncommitted asset still exists: %v", err)
	}
}

func TestCopyBoundedRejectsActualOversizeStream(t *testing.T) {
	var dst strings.Builder
	written, err := copyBounded(&dst, strings.NewReader("12345"), 4)
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("copyBounded error=%v, want ErrMediaTooLarge", err)
	}
	if written != 5 {
		t.Fatalf("written=%d, want 5 bytes observed before rejection", written)
	}
}

func TestSafeTempExtension(t *testing.T) {
	cases := map[string]string{
		"photo.PNG":        ".png",
		"archive.tar.gz":   ".gz",
		"unsafe.jp*g":      "",
		"too.abcdefghijklmnopq": "",
		"noext":            "",
	}
	for name, want := range cases {
		if got := safeTempExtension(name); got != want {
			t.Errorf("safeTempExtension(%q)=%q, want %q", name, got, want)
		}
	}
}

func TestPreparedCleanupIsIdempotent(t *testing.T) {
	calls := 0
	prepared := &Prepared{cleanup: func() { calls++ }}
	prepared.Cleanup()
	prepared.Cleanup()
	if calls != 1 {
		t.Fatalf("cleanup calls=%d, want 1", calls)
	}
}

func TestSavedResponseTypedErrors(t *testing.T) {
	svc := NewService(nil)
	if _, err := svc.CaptureReply(nil); !errors.Is(err, ErrNilContext) {
		t.Fatalf("CaptureReply(nil) error=%v, want ErrNilContext", err)
	}
	if _, err := svc.Prepare(context.Background(), Response{}, TemplateVars{}); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("Prepare(empty) error=%v, want ErrEmptyResponse", err)
	}
	if err := svc.CommitReplacement(context.Background(), Response{}, Response{}, nil); !errors.Is(err, ErrNilPersist) {
		t.Fatalf("CommitReplacement(nil callback) error=%v, want ErrNilPersist", err)
	}
	if err := svc.CommitDelete(context.Background(), Response{}, nil); !errors.Is(err, ErrNilDelete) {
		t.Fatalf("CommitDelete(nil callback) error=%v, want ErrNilDelete", err)
	}
}
