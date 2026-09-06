package inline

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/ui"
)

func TestSerializer_Article_HTMLFormatting(t *testing.T) {
	reg := NewSerializerRegistry()
	markup := ui.NewMarkup(ui.ButtonRow{ui.NewURLButton("Google", "https://google.com")})

	res := InlineResult{
		ID:          "art-1",
		Type:        ResultArticle,
		Title:       "Test Title",
		Description: "Test Description",
		Text:        "<b>Bold Text</b> and <code>code text</code>",
		Markup:      &markup,
		ThumbURL:    "https://example.com/thumb.jpg",
		URL:         "https://example.com/article",
	}

	tgRes := reg.Serialize(res)
	art, ok := tgRes.(*tg.InputBotInlineResult)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineResult, got %T", tgRes)
	}

	if art.ID != "art-1" || art.Type != "article" || art.Title != "Test Title" {
		t.Errorf("unexpected article metadata: %+v", art)
	}
	if art.URL != "https://example.com/article" {
		t.Errorf("unexpected URL: %s", art.URL)
	}
	if art.Thumb.URL != "https://example.com/thumb.jpg" {
		t.Errorf("unexpected thumb URL: %s", art.Thumb.URL)
	}

	msgText, ok := art.SendMessage.(*tg.InputBotInlineMessageText)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineMessageText, got %T", art.SendMessage)
	}
	if msgText.Message != "Bold Text and code text" {
		t.Errorf("expected parsed plain text 'Bold Text and code text', got %q", msgText.Message)
	}
	if len(msgText.Entities) != 2 {
		t.Fatalf("expected 2 parsed entities, got %d", len(msgText.Entities))
	}
	if _, ok := msgText.Entities[0].(*tg.MessageEntityBold); !ok {
		t.Errorf("expected first entity to be *tg.MessageEntityBold, got %T", msgText.Entities[0])
	}
	if _, ok := msgText.Entities[1].(*tg.MessageEntityCode); !ok {
		t.Errorf("expected second entity to be *tg.MessageEntityCode, got %T", msgText.Entities[1])
	}
	if msgText.ReplyMarkup == nil {
		t.Errorf("expected reply markup to be attached")
	}
}

func TestSerializer_Photo(t *testing.T) {
	reg := NewSerializerRegistry()

	// 1. Photo with MediaURL
	resWithMedia := InlineResult{
		ID:            "photo-1",
		Type:          ResultPhoto,
		Title:         "Sunset",
		Text:          "<i>Beautiful sunset</i>",
		MediaURL:      "https://example.com/sunset.jpg",
		MediaMimeType: "image/jpeg",
	}

	tgRes := reg.Serialize(resWithMedia)
	photo, ok := tgRes.(*tg.InputBotInlineResult)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineResult, got %T", tgRes)
	}
	if photo.Type != "photo" || photo.Content.URL != "https://example.com/sunset.jpg" {
		t.Errorf("unexpected photo content: %+v", photo)
	}
	mediaAuto, ok := photo.SendMessage.(*tg.InputBotInlineMessageMediaAuto)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineMessageMediaAuto for media photo, got %T", photo.SendMessage)
	}
	if mediaAuto.Message != "Beautiful sunset" {
		t.Errorf("unexpected media caption: %q", mediaAuto.Message)
	}
	if len(mediaAuto.Entities) != 1 {
		t.Errorf("expected 1 entity (italic), got %d", len(mediaAuto.Entities))
	}

	// 2. Photo without MediaURL (fallback to text)
	resNoMedia := InlineResult{
		ID:    "photo-2",
		Type:  ResultPhoto,
		Title: "No Media",
		Text:  "Only text caption",
	}
	tgRes2 := reg.Serialize(resNoMedia)
	photo2 := tgRes2.(*tg.InputBotInlineResult)
	if _, ok := photo2.SendMessage.(*tg.InputBotInlineMessageText); !ok {
		t.Errorf("expected *tg.InputBotInlineMessageText when MediaURL is missing, got %T", photo2.SendMessage)
	}
}

func TestSerializer_MediaTypes(t *testing.T) {
	reg := NewSerializerRegistry()

	tests := []struct {
		resType      InlineResultType
		expectedMime string
		mediaURL     string
	}{
		{ResultDocument, "application/pdf", "https://example.com/doc.pdf"},
		{ResultVideo, "video/mp4", "https://example.com/vid.mp4"},
		{ResultGif, "image/gif", "https://example.com/anim.gif"},
		{ResultAudio, "audio/mpeg", "https://example.com/song.mp3"},
	}

	for _, tc := range tests {
		res := InlineResult{
			ID:            string(tc.resType) + "-1",
			Type:          tc.resType,
			Title:         "Media Item",
			MediaURL:      tc.mediaURL,
			MediaMimeType: tc.expectedMime,
			Text:          "Caption for " + string(tc.resType),
		}

		tgRes := reg.Serialize(res)
		item, ok := tgRes.(*tg.InputBotInlineResult)
		if !ok {
			t.Fatalf("expected *tg.InputBotInlineResult for %s, got %T", tc.resType, tgRes)
		}
		if item.Type != string(tc.resType) {
			t.Errorf("expected type %s, got %s", tc.resType, item.Type)
		}
		if item.Content.URL != tc.mediaURL || item.Content.MimeType != tc.expectedMime {
			t.Errorf("unexpected content for %s: %+v", tc.resType, item.Content)
		}
		if _, ok := item.SendMessage.(*tg.InputBotInlineMessageMediaAuto); !ok {
			t.Errorf("expected *tg.InputBotInlineMessageMediaAuto for %s, got %T", tc.resType, item.SendMessage)
		}
	}
}

func TestSerializer_GeoAndVenue(t *testing.T) {
	reg := NewSerializerRegistry()

	// Geo
	geoRes := InlineResult{
		ID:        "geo-1",
		Type:      ResultGeo,
		Title:     "Monas",
		Latitude:  -6.1754,
		Longitude: 106.8272,
	}
	tgGeo := reg.Serialize(geoRes).(*tg.InputBotInlineResult)
	geoMsg, ok := tgGeo.SendMessage.(*tg.InputBotInlineMessageMediaGeo)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineMessageMediaGeo, got %T", tgGeo.SendMessage)
	}
	pt, ok := geoMsg.GeoPoint.(*tg.InputGeoPoint)
	if !ok || pt.Lat != -6.1754 || pt.Long != 106.8272 {
		t.Errorf("unexpected geo coordinates: %+v", geoMsg.GeoPoint)
	}

	// Venue
	venueRes := InlineResult{
		ID:        "venue-1",
		Type:      ResultVenue,
		Title:     "National Monument",
		Address:   "Gambir, Central Jakarta",
		Latitude:  -6.1754,
		Longitude: 106.8272,
	}
	tgVenue := reg.Serialize(venueRes).(*tg.InputBotInlineResult)
	venueMsg, ok := tgVenue.SendMessage.(*tg.InputBotInlineMessageMediaVenue)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineMessageMediaVenue, got %T", tgVenue.SendMessage)
	}
	if venueMsg.Title != "National Monument" || venueMsg.Address != "Gambir, Central Jakarta" {
		t.Errorf("unexpected venue info: %+v", venueMsg)
	}
}

func TestSerializer_ContactAndGame(t *testing.T) {
	reg := NewSerializerRegistry()

	// Contact
	contactRes := InlineResult{
		ID:          "contact-1",
		Type:        ResultContact,
		Title:       "Alice",
		PhoneNumber: "+1234567890",
		FirstName:   "Alice",
		LastName:    "Smith",
		VCard:       "BEGIN:VCARD\nFN:Alice Smith\nEND:VCARD",
	}
	tgContact := reg.Serialize(contactRes).(*tg.InputBotInlineResult)
	contactMsg, ok := tgContact.SendMessage.(*tg.InputBotInlineMessageMediaContact)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineMessageMediaContact, got %T", tgContact.SendMessage)
	}
	if contactMsg.PhoneNumber != "+1234567890" || contactMsg.FirstName != "Alice" || contactMsg.LastName != "Smith" {
		t.Errorf("unexpected contact details: %+v", contactMsg)
	}

	// Game with ShortName
	gameRes := InlineResult{
		ID:            "game-1",
		Type:          ResultGame,
		GameShortName: "lumberjack",
	}
	tgGame := reg.Serialize(gameRes)
	gameResult, ok := tgGame.(*tg.InputBotInlineResultGame)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineResultGame, got %T", tgGame)
	}
	if gameResult.ShortName != "lumberjack" {
		t.Errorf("expected ShortName 'lumberjack', got %s", gameResult.ShortName)
	}

	// Game without ShortName falls back to article
	gameNoShortName := InlineResult{
		ID:    "game-2",
		Type:  ResultGame,
		Title: "Game without shortname",
		Text:  "Fallback description",
	}
	tgFallback := reg.Serialize(gameNoShortName)
	if _, ok := tgFallback.(*tg.InputBotInlineResult); !ok {
		t.Errorf("expected fallback to *tg.InputBotInlineResult, got %T", tgFallback)
	}
}

func TestSerializer_CustomRegistration(t *testing.T) {
	reg := NewSerializerRegistry()

	customSerializer := SerializerFunc(func(res InlineResult) (tg.InputBotInlineResultClass, error) {
		return &tg.InputBotInlineResult{
			ID:    "custom-" + res.ID,
			Type:  "custom_article",
			Title: "CUSTOM: " + res.Title,
			SendMessage: &tg.InputBotInlineMessageText{
				Message: "Custom prefix: " + res.Text,
			},
		}, nil
	})

	reg.Register(ResultArticle, customSerializer)

	res := InlineResult{
		ID:    "101",
		Type:  ResultArticle,
		Title: "Original",
		Text:  "Body",
	}

	serialized := reg.Serialize(res)
	item, ok := serialized.(*tg.InputBotInlineResult)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineResult, got %T", serialized)
	}
	if item.Title != "CUSTOM: Original" {
		t.Errorf("expected custom title, got %s", item.Title)
	}
}
