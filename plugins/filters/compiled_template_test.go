package filters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

func TestCompileFilterItemCachesReusableTemplateAndClonesResponse(t *testing.T) {
	original := savedresponse.NewHTML("Hi {name}")
	original.Media = &savedresponse.MediaRef{AssetID: "asset-1", MediaType: "photo"}

	compiled := compileFilterItem(Filter{Keyword: " Welcome ", Response: original})
	if compiled.keyword != "welcome" {
		t.Fatalf("keyword=%q", compiled.keyword)
	}
	if compiled.templateErr != nil {
		t.Fatalf("compile template: %v", compiled.templateErr)
	}
	if compiled.template == nil {
		t.Fatal("compiled template was not cached")
	}

	original.Text = "mutated"
	original.Media.AssetID = "asset-2"
	if compiled.response.Text != "Hi {name}" || compiled.response.MediaAssetID() != "asset-1" {
		t.Fatalf("cached response was not detached: %+v", compiled.response)
	}

	got, err := compiled.template.Render(savedresponse.TemplateVars{Name: "Alice"}, savedresponse.DefaultMaxOutputRunes)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hi Alice" {
		t.Fatalf("render=%q", got)
	}
}

func TestCompileFilterItemCachesInvalidLegacyTemplateError(t *testing.T) {
	compiled := compileFilterItem(Filter{Keyword: "legacy",
		Response: savedresponse.Response{
			Text:   "legacy response",
			Format: savedresponse.Format("legacy-format"),
		},
	})
	if compiled.template != nil {
		t.Fatal("invalid legacy template unexpectedly compiled")
	}
	if !errors.Is(compiled.templateErr, savedresponse.ErrUnsupportedFormat) {
		t.Fatalf("compile error=%v", compiled.templateErr)
	}
}

func TestInvalidLegacyTemplateIsReportedOnlyWhenMatched(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	chatID := int64(700)
	if err := repo.SaveFilter(context.Background(), chatID, "legacy", savedresponse.Response{
		Text: "broken", Format: savedresponse.Format("legacy-format"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveFilter(context.Background(), chatID, "hello", savedresponse.NewHTML("Hi {name}")); err != nil {
		t.Fatal(err)
	}

	svc := &mockService{}
	p := New(repo, func() core.TelegramServicer { return svc })

	valid := &core.MessageEnvelope{
		ID: 1, ChatID: chatID,
		Peer:   core.PeerRef{Kind: core.PeerKindChat, ID: chatID},
		Chat:   core.Chat{ID: chatID, Title: "Room"},
		Sender: core.User{ID: 42, FirstName: "Alice"},
		Text:   "hello",
	}
	if err := p.HandleMessageEvent(context.Background(), valid); err != nil {
		t.Fatalf("valid sibling filter was blocked by invalid legacy row: %v", err)
	}
	if svc.sent != "Hi Alice" {
		t.Fatalf("valid response=%q", svc.sent)
	}

	svc.sent = ""
	invalid := *valid
	invalid.ID = 2
	invalid.Text = "legacy"
	err = p.HandleMessageEvent(context.Background(), &invalid)
	if !errors.Is(err, savedresponse.ErrUnsupportedFormat) {
		t.Fatalf("legacy match error=%v", err)
	}
	if !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy error lacks filter identity: %v", err)
	}
	if svc.sent != "" {
		t.Fatalf("invalid legacy filter sent response %q", svc.sent)
	}
}
