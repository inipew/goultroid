package ui

import (
	"testing"
)

func TestButton_Builders(t *testing.T) {
	cb := NewCallbackButton("Click Me", []byte("data123"))
	if cb.Type != ButtonCallback || cb.Text != "Click Me" || string(cb.Data) != "data123" {
		t.Errorf("unexpected callback button: %+v", cb)
	}

	ub := NewURLButton("Open Google", "https://google.com")
	if ub.Type != ButtonURL || ub.Text != "Open Google" || ub.URL != "https://google.com" {
		t.Errorf("unexpected URL button: %+v", ub)
	}

	sb := NewSwitchInlineButton("Search", "test query", true)
	if sb.Type != ButtonSwitchInline || sb.Text != "Search" || sb.InlineQuery != "test query" || !sb.SamePeer {
		t.Errorf("unexpected switch inline button: %+v", sb)
	}
}

func TestPaginationRow(t *testing.T) {
	// First page (no prev button)
	row1 := NewPaginationRow(nil, []byte("page:2"), 1, 5)
	if len(row1) != 2 {
		t.Fatalf("expected 2 buttons on page 1, got %d", len(row1))
	}
	if row1[0].Text != "1 / 5" || row1[1].Text != "Next ▶" {
		t.Errorf("unexpected page 1 buttons: %+v", row1)
	}

	// Middle page (both prev and next buttons)
	row3 := NewPaginationRow([]byte("page:2"), []byte("page:4"), 3, 5)
	if len(row3) != 3 {
		t.Fatalf("expected 3 buttons on page 3, got %d", len(row3))
	}
	if row3[0].Text != "◀ Prev" || row3[1].Text != "3 / 5" || row3[2].Text != "Next ▶" {
		t.Errorf("unexpected page 3 buttons: %+v", row3)
	}

	// Last page (no next button)
	row5 := NewPaginationRow([]byte("page:4"), nil, 5, 5)
	if len(row5) != 2 {
		t.Fatalf("expected 2 buttons on page 5, got %d", len(row5))
	}
	if row5[0].Text != "◀ Prev" || row5[1].Text != "5 / 5" {
		t.Errorf("unexpected page 5 buttons: %+v", row5)
	}
}

func TestConfirmCancelRow(t *testing.T) {
	row := NewConfirmCancelRow([]byte("yes"), []byte("no"))
	if len(row) != 2 {
		t.Fatalf("expected 2 buttons in confirm cancel row, got %d", len(row))
	}
	if row[0].Text != "✅ Confirm" || string(row[0].Data) != "yes" {
		t.Errorf("unexpected confirm button: %+v", row[0])
	}
	if row[1].Text != "❌ Cancel" || string(row[1].Data) != "no" {
		t.Errorf("unexpected cancel button: %+v", row[1])
	}
}
