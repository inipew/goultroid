package savedresponse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
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
