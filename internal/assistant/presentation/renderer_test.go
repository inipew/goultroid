package presentation_test

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/presentation"
)

func TestRenderScreen(t *testing.T) {
	// Nil screen
	text, markup := presentation.RenderScreen(nil)
	if text != "" || markup != nil {
		t.Fatalf("expected empty for nil screen, got text=%q, markup=%v", text, markup)
	}

	// Screen without buttons
	screenSimple := menu.NewScreen(menu.ScreenIDStart, "Title", "Body text")
	textSimple, markupSimple := presentation.RenderScreen(screenSimple)
	if textSimple != "<b>Title</b>\n\nBody text" || markupSimple != nil {
		t.Fatalf("unexpected simple screen render: text=%q, markup=%v", textSimple, markupSimple)
	}

	// Screen with buttons
	screenFull := menu.NewScreen(menu.ScreenIDStart, "Dashboard", "Welcome")
	screenFull.AddRow(
		menu.NewButton("Settings", "a1:assistant:settings"),
		menu.NewURLButton("Channel", "https://t.me/example"),
	)

	textFull, markupFull := presentation.RenderScreen(screenFull)
	if textFull != "<b>Dashboard</b>\n\nWelcome" {
		t.Fatalf("unexpected text: %q", textFull)
	}
	inlineMarkup, ok := markupFull.(*tg.ReplyInlineMarkup)
	if !ok || len(inlineMarkup.Rows) != 1 {
		t.Fatalf("expected 1 row in inline markup, got %+v", markupFull)
	}
	if len(inlineMarkup.Rows[0].Buttons) != 2 {
		t.Fatalf("expected 2 buttons in row, got %d", len(inlineMarkup.Rows[0].Buttons))
	}

	cbBtn, ok := inlineMarkup.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok || cbBtn.Text != "Settings" || string(cbBtn.Data) != "a1:assistant:settings" {
		t.Fatalf("unexpected callback button: %+v", inlineMarkup.Rows[0].Buttons[0])
	}

	urlBtn, ok := inlineMarkup.Rows[0].Buttons[1].(*tg.KeyboardButtonURL)
	if !ok || urlBtn.Text != "Channel" || urlBtn.URL != "https://t.me/example" {
		t.Fatalf("unexpected URL button: %+v", inlineMarkup.Rows[0].Buttons[1])
	}
}
