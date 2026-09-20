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
