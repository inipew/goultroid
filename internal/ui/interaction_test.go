package ui

import "testing"

func TestScreenRender(t *testing.T) {
	s := NewScreen("test-screen", "My Title", "This is the screen body text.")
	s.AddRow(
		NewCallbackButton("Btn 1", []byte("opaque-callback")),
		NewURLButton("Link", "https://example.com"),
	)

	text, markup := s.Render()
	if text != "<b>My Title</b>\n\nThis is the screen body text." {
		t.Errorf("unexpected rendered text: %q", text)
	}
	if len(markup.Rows) != 1 {
		t.Fatalf("expected 1 markup row, got %v", markup)
	}
	if len(markup.Rows[0]) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(markup.Rows[0]))
	}

	cbBtn := markup.Rows[0][0]
	if cbBtn.Type != ButtonCallback || cbBtn.Text != "Btn 1" || string(cbBtn.Data) != "opaque-callback" {
		t.Errorf("unexpected callback button: %+v", cbBtn)
	}

	urlBtn := markup.Rows[0][1]
	if urlBtn.Type != ButtonURL || urlBtn.Text != "Link" || urlBtn.URL != "https://example.com" {
		t.Errorf("unexpected url button: %+v", urlBtn)
	}

	s2 := NewScreen("empty", "", "Plain text")
	text2, markup2 := s2.Render()
	if text2 != "Plain text" {
		t.Errorf("expected 'Plain text', got %q", text2)
	}
	if len(markup2.Rows) != 0 {
		t.Errorf("expected empty markup for screen with no rows, got %+v", markup2)
	}
}

func TestPaginateSlice(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e", "f", "g"}
	paged, totalPages := PaginateSlice(items, 1, 3)
	if totalPages != 3 || len(paged) != 3 || paged[0] != "a" || paged[2] != "c" {
		t.Errorf("unexpected page 1: %v (total=%d)", paged, totalPages)
	}
	pagedLast, _ := PaginateSlice(items, 3, 3)
	if len(pagedLast) != 1 || pagedLast[0] != "g" {
		t.Errorf("unexpected last page: %v", pagedLast)
	}
}
