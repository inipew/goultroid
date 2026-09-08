package quote

import (
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestDisplayUserName(t *testing.T) {
	if got := displayUserName("Alice", "Wonderland", "alice"); got != "Alice Wonderland" {
		t.Fatalf("unexpected full name: %q", got)
	}
	if got := displayUserName("", "", "alice"); got != "@alice" {
		t.Fatalf("unexpected username fallback: %q", got)
	}
	if got := displayUserName("", "", ""); got != "" {
		t.Fatalf("expected empty name, got %q", got)
	}
}

func TestMediaPlaceholder(t *testing.T) {
	if got := mediaPlaceholder("photo"); got != "[Photo]" {
		t.Fatalf("unexpected photo placeholder: %q", got)
	}
	if got := mediaPlaceholder("voice"); got != "[Voice message]" {
		t.Fatalf("unexpected voice placeholder: %q", got)
	}
	if got := mediaPlaceholder("unknown"); got != "[Media]" {
		t.Fatalf("unexpected fallback placeholder: %q", got)
	}
}

func TestPeerToInputUser(t *testing.T) {
	peer := &tg.InputPeerUser{UserID: 123, AccessHash: 456}
	user, ok := peerToInputUser(peer)
	if !ok || user == nil {
		t.Fatal("expected a usable InputUser")
	}
	got, ok := user.(*tg.InputUser)
	if !ok || got.UserID != 123 || got.AccessHash != 456 {
		t.Fatalf("unexpected InputUser: %#v", user)
	}

	if _, ok := peerToInputUser(&tg.InputPeerUser{UserID: 123}); ok {
		t.Fatal("expected zero access hash to be rejected")
	}
}

func TestRenderPreservesUnicodePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote.jpg")
	text := "Halo dunia 👋 — Indonesia 日本語 العربية"
	msg := &core.Message{Date: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	if err := render(path, "Dhimas", text, msg, ""); err != nil {
		t.Fatalf("render failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("rendered file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("rendered file is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := jpeg.Decode(f); err != nil {
		t.Fatalf("rendered file is not a valid JPEG: %v", err)
	}

	lines := wrapStyledSegments(styledSegments(strings.Repeat("Unicode ", 100), nil), loadFont("regular", 24), 804)
	if len(lines) < 2 {
		t.Fatal("expected long text to wrap into multiple lines")
	}
}

func TestTelegramChatBubbleRender(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_bubble.png")

	err := renderV3WithOpts(RenderOptions{
		Path:      path,
		Name:      "Kobo Kanaeru [DC2]",
		Badge:     "Bot Mirror",
		Text:      "Memproses link...",
		SenderID:  4, // Cyan
		Timestamp: "10.44",
		ReplyPreview: &ReplyPreview{
			Author: "",
			Text:   "Deleted message",
		},
	})
	if err != nil {
		t.Fatalf("renderV3WithOpts failed: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open output: %v", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode rendered PNG: %v", err)
	}

	b := img.Bounds()
	if b.Dx() < 300 || b.Dy() < 100 {
		t.Fatalf("image bounds unexpectedly small: %v", b)
	}

	// Verify chat background color at top-left
	r, g, bVal, a := img.At(0, 0).RGBA()
	r8, g8, b8, a8 := uint8(r>>8), uint8(g>>8), uint8(bVal>>8), uint8(a>>8)
	if r8 != 14 || g8 != 22 || b8 != 33 || a8 != 255 {
		t.Fatalf("expected canvas background #0e1621ff, got #%02x%02x%02x%02x", r8, g8, b8, a8)
	}
}

func TestRenderWithReplyAuthorAndText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_reply.png")

	err := renderV3WithOpts(RenderOptions{
		Path:     path,
		Name:     "Alice",
		Badge:    "admin",
		Text:     "Check this message",
		SenderID: 12345,
		ReplyPreview: &ReplyPreview{
			Author: "Bob",
			Text:   "Original question here",
		},
	})
	if err != nil {
		t.Fatalf("render with reply author failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output file invalid: %v", err)
	}
}

func TestStyledSegmentsEntities(t *testing.T) {
	text := "Hello bold italic code"
	entities := []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 6, Length: 4},
		&tg.MessageEntityItalic{Offset: 11, Length: 6},
		&tg.MessageEntityCode{Offset: 18, Length: 4},
	}
	segs := styledSegments(text, entities)
	if len(segs) < 4 {
		t.Fatalf("expected at least 4 styled segments, got %d", len(segs))
	}
	if segs[0].style != styleNormal || segs[0].text != "Hello " {
		t.Errorf("seg 0 mismatch: %+v", segs[0])
	}
	if segs[1].style != styleBold || segs[1].text != "bold" {
		t.Errorf("seg 1 mismatch: %+v", segs[1])
	}
	if segs[2].style != styleNormal {
		t.Errorf("seg 2 mismatch: %+v", segs[2])
	}
	if segs[3].style != styleItalic || segs[3].text != "italic" {
		t.Errorf("seg 3 mismatch: %+v", segs[3])
	}
}

func TestTelegramNameColorsAndAbsInt64(t *testing.T) {
	if absInt64(-42) != 42 || absInt64(42) != 42 || absInt64(0) != 0 {
		t.Fatal("absInt64 failed")
	}

	for i := -14; i <= 14; i++ {
		idx := int(absInt64(int64(i)) % 7)
		if idx < 0 || idx >= len(telegramNameColors) {
			t.Fatalf("invalid color index %d for input %d", idx, i)
		}
	}
}

func TestFontLoading(t *testing.T) {
	regular := loadFont("regular", 24)
	if regular == nil {
		t.Fatal("regular font face is nil")
	}
	bold := loadFont("bold", 24)
	if bold == nil {
		t.Fatal("bold font face is nil")
	}
	italic := loadFont("italic", 24)
	if italic == nil {
		t.Fatal("italic font face is nil")
	}
	mono := loadFont("mono", 24)
	if mono == nil {
		t.Fatal("mono font face is nil")
	}
}

func TestRenderFullReferenceSample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_sample.png")
	msg := &core.Message{
		SenderID: 4,
		Date:     time.Date(2026, 9, 8, 10, 44, 0, 0, time.Local),
	}
	err := RenderV3WithOpts(RenderOptions{
		Path:      path,
		Name:      "Kobo Kanaeru [DC2]",
		Badge:     "Bot Mirror",
		Text:      "Memproses link...",
		Message:   msg,
		SenderID:  4,
		Timestamp: "10.44",
		ReplyPreview: &ReplyPreview{
			Author: "",
			Text:   "Deleted message",
		},
	})
	if err != nil {
		t.Fatalf("failed to render reference sample: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("rendered reference sample file is missing or empty")
	}
}

func TestSymbolsRendering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_symbols.png")
	symbolsText := "Status: ✓ Success • ✗ Failed • ★ Top • ⚡ Fast • ❤ Love • ➜ Next • ∞ Loop • ₹100 • ₿1"
	err := RenderV3WithOpts(RenderOptions{
		Path:      path,
		Name:      "Symbols Test ⚡",
		Badge:     "VIP ★",
		Text:      symbolsText,
		SenderID:  2,
		Timestamp: "12.30",
	})
	if err != nil {
		t.Fatalf("failed to render symbols: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("rendered symbols file is missing or empty")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode rendered symbols PNG: %v", err)
	}
	if img.Bounds().Dx() < 400 || img.Bounds().Dy() < 100 {
		t.Fatalf("unexpected image bounds: %v", img.Bounds())
	}
}
