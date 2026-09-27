package ui

import "testing"

func TestButtonBuilders(t *testing.T) {
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
