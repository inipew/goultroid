package myxl

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type myxlDeliveryService struct {
	core.MockTelegramServicer
	edits       []string
	sends       []string
	markupSends int
}

func (s *myxlDeliveryService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	s.edits = append(s.edits, text)
	return nil
}

func (s *myxlDeliveryService) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	s.sends = append(s.sends, text)
	return &tg.Message{ID: 100 + len(s.sends), Message: text}, nil
}

func (s *myxlDeliveryService) SendMessageWithMarkup(_ context.Context, _ tg.InputPeerClass, text string, _ tg.ReplyMarkupClass) (*tg.Message, error) {
	s.sends = append(s.sends, text)
	s.markupSends++
	return &tg.Message{ID: 200 + len(s.sends), Message: text}, nil
}

func TestDeliverHTMLChunksLongGeneratedOutput(t *testing.T) {
	svc := &myxlDeliveryService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}
	text := "<b>Quota</b>
" + strings.Repeat("日本語 &amp; data
", 700)

	if err := deliverHTML(ctx, text); err != nil {
		t.Fatal(err)
	}
	if len(svc.edits) != 1 || len(svc.sends) == 0 {
		t.Fatalf("expected one edit plus continuation replies, edits=%d sends=%d", len(svc.edits), len(svc.sends))
	}
	all := append(append([]string(nil), svc.edits...), svc.sends...)
	for i, chunk := range all {
		if utf8.RuneCountInString(chunk) > myxlTelegramMessageRunes {
			t.Fatalf("chunk %d has %d runes, limit=%d", i, utf8.RuneCountInString(chunk), myxlTelegramMessageRunes)
		}
	}
}

func TestDeliverHTMLWithMarkupAttachesMarkupToFinalChunk(t *testing.T) {
	svc := &myxlDeliveryService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}
	text := "<i>" + strings.Repeat("x
", 5000) + "</i>"
	markup := &tg.ReplyInlineMarkup{}

	if err := deliverHTMLWithMarkup(ctx, text, markup); err != nil {
		t.Fatal(err)
	}
	if svc.markupSends != 1 {
		t.Fatalf("markup sends=%d, want 1", svc.markupSends)
	}
	if len(svc.edits) != 1 || len(svc.sends) == 0 {
		t.Fatalf("expected chunked delivery, edits=%d sends=%d", len(svc.edits), len(svc.sends))
	}
	if got := svc.sends[len(svc.sends)-1]; utf8.RuneCountInString(got) > myxlTelegramMessageRunes {
		t.Fatalf("final markup chunk too long: %d", utf8.RuneCountInString(got))
	}
}
