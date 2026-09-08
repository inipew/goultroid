package quote

import (
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
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
	if got := mediaPlaceholder("photo"); got != "[photo]" {
		t.Fatalf("unexpected photo placeholder: %q", got)
	}
	if got := mediaPlaceholder("voice"); got != "[voice message]" {
		t.Fatalf("unexpected voice placeholder: %q", got)
	}
	if got := mediaPlaceholder("unknown"); got != "[media]" {
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
	if err := render(path, "Dhimas", text, time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)); err != nil {
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

	lines := wrapToWidth(strings.Repeat("Unicode ", 100), loadFont(24), 804)
	if len(lines) < 2 {
		t.Fatal("expected long text to wrap into multiple lines")
	}
}
