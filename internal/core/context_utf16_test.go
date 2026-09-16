package core

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestMessageEntitiesUseTelegramUTF16Offsets(t *testing.T) {
	msg := &Message{
		Text: "🎵 @alice https://example.com/song.mp3",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 3, Length: 6},
			&tg.MessageEntityURL{Offset: 10, Length: 28},
		},
	}

	mentions := msg.Mentions()
	if len(mentions) != 1 || mentions[0] != "@alice" {
		t.Fatalf("expected UTF-16 mention extraction to preserve @alice, got %v", mentions)
	}

	urls := msg.URLs()
	if len(urls) != 1 || urls[0] != "https://example.com/song.mp3" {
		t.Fatalf("expected UTF-16 URL extraction to preserve exact URL, got %v", urls)
	}
}

func TestTelegramEntityTextRejectsSurrogatePairSplit(t *testing.T) {
	// The emoji occupies UTF-16 units [0,2). Starting at unit 1 would split its surrogate pair.
	if got, ok := telegramEntityText("🎵 hello", 1, 1); ok {
		t.Fatalf("expected surrogate-pair split to be rejected, got %q", got)
	}
}
