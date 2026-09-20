package core

import "testing"

func TestMessageEnvelopeMentionHelpers(t *testing.T) {
	e := &MessageEnvelope{
		Chat:     Chat{Type: "supergroup"},
		Sender:   User{ID: 42, FirstName: "Ada", LastName: "Lovelace"},
		Mentions: []MessageMention{{UserID: 7}, {Username: "@Owner"}},
	}
	if !e.IsGroup() || e.IsPrivate() || e.IsChannel() {
		t.Fatal("unexpected chat classification")
	}
	if !e.MentionsUser(7) {
		t.Fatal("missing user-id mention")
	}
	if !e.MentionsUsername("owner") || !e.MentionsUsername("@OWNER") {
		t.Fatal("username mention should be case-insensitive")
	}
	if got := e.SenderName(); got != "Ada Lovelace" {
		t.Fatalf("SenderName=%q", got)
	}
}

func TestMessageEnvelopeMediaSummaryHelper(t *testing.T) {
	info := &MediaInfo{Type: "photo", FileName: "x.jpg", MIMEType: "image/jpeg", Size: 12, Width: 10, Height: 20}
	summary := SummarizeMedia(info)
	if summary == nil || summary.Type != "photo" || summary.Width != 10 || summary.Height != 20 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	envelope := &MessageEnvelope{Media: summary}
	if !envelope.HasMedia() {
		t.Fatal("expected HasMedia")
	}
}
