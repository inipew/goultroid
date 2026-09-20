package myxl

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/ui/render"
)

func TestRenderQRCompact(t *testing.T) {
	// Sample EMVCo QRIS payload
	qrisPayload := "00020101021226570011ID.CO.QRIS.WWW01189360002140000000001030301UBE51440014ID.LINKAJA.WWW0215ID20201770500670303UBE52040000530336054031005802ID5921TEST MERCHANT QRIS6013JAKARTA PUSAT610512345630421A6"

	rendered, err := RenderQRCompact(qrisPayload)
	if err != nil {
		t.Fatalf("RenderQRCompact returned unexpected error: %v", err)
	}

	if rendered == "" {
		t.Fatal("RenderQRCompact returned empty string")
	}

	lines := strings.Split(rendered, "\n")
	if len(lines) < 15 {
		t.Fatalf("expected at least 15 lines of QR output, got %d", len(lines))
	}

	// Verify that each line has consistent character width
	firstLineWidth := len([]rune(lines[0]))
	for i, l := range lines {
		runeCount := len([]rune(l))
		if runeCount != firstLineWidth {
			t.Errorf("line %d width mismatch: expected %d runes, got %d", i, firstLineWidth, runeCount)
		}
	}

	// Verify allowed characters
	for _, r := range rendered {
		if r != '\n' && r != '█' && r != '▀' && r != '▄' && r != ' ' {
			t.Errorf("unexpected rune in QR compact output: %q (U+%04X)", r, r)
			break
		}
	}

	// Test empty input
	if _, err := RenderQRCompact(""); err == nil {
		t.Error("expected error for empty QR input, got nil")
	}
}

func TestGenerateQRPNG(t *testing.T) {
	qrisPayload := "00020101021226570011ID.CO.QRIS.WWW01189360002140000000001030301UBE51440014ID.LINKAJA.WWW0215ID20201770500670303UBE52040000530336054031005802ID5921TEST MERCHANT QRIS6013JAKARTA PUSAT610512345630421A6"

	pngBytes, err := GenerateQRPNG(qrisPayload)
	if err != nil {
		t.Fatalf("GenerateQRPNG returned unexpected error: %v", err)
	}

	if len(pngBytes) == 0 {
		t.Fatal("GenerateQRPNG returned empty slice")
	}

	// Verify that the output is a valid decodable PNG image
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("failed to decode PNG bytes: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() <= 100 || bounds.Dy() <= 100 {
		t.Errorf("expected PNG dimension > 100px, got %dx%d", bounds.Dx(), bounds.Dy())
	}

	// Test empty input
	if _, err := GenerateQRPNG(""); err == nil {
		t.Error("expected error for empty QR input, got nil")
	}
}

func TestQRHTMLParse(t *testing.T) {
	qrisPayload := "00020101021226570011ID.CO.QRIS.WWW01189360002140000000001030301UBE51440014ID.LINKAJA.WWW0215ID20201770500670303UBE52040000530336054031005802ID5921TEST MERCHANT QRIS6013JAKARTA PUSAT610512345630421A6"

	screen := (&MenuManager{}).BuildPurchaseResultScreen(&SettlementResult{
		IsSuccess:       true,
		TransactionCode: "TRX-12345",
		QRCode:          qrisPayload,
	}, "Combo 10GB", 25000, "QRIS", "OPT-QRIS")

	text, _ := render.ToTelegram(screen)
	t.Logf("Full text:\n%s", text)

	var eb entity.Builder
	err := styling.Perform(&eb, html.String(nil, text))
	if err != nil {
		t.Fatalf("styling.Perform error: %v", err)
	}
	plain, ents := eb.Complete()
	plainRunes := []rune(plain)
	plainUTF16 := utf16.Encode(plainRunes)
	t.Logf("plain bytes: %d, runes: %d, utf16 len: %d", len(plain), len(plainRunes), len(plainUTF16))
	for i, ent := range ents {
		t.Logf("ent[%d]: %#v", i, ent)
	}

	for i, ent := range ents {
		var offset, length int
		switch e := ent.(type) {
		case *tg.MessageEntityPre:
			offset = e.Offset
			length = e.Length
		case *tg.MessageEntityCode:
			offset = e.Offset
			length = e.Length
		case *tg.MessageEntityBold:
			offset = e.Offset
			length = e.Length
		case *tg.MessageEntityItalic:
			offset = e.Offset
			length = e.Length
		}

		if offset+length > len(plainUTF16) {
			t.Errorf("entity[%d] bounds [%d, %d] exceeds message UTF16 length %d", i, offset, offset+length, len(plainUTF16))
		}
	}

	// Verify no MessageEntityPre and MessageEntityCode share identical bounds (causes ENTITY_BOUNDS_INVALID)
	for _, e1 := range ents {
		pre, isPre := e1.(*tg.MessageEntityPre)
		if !isPre {
			continue
		}
		for _, e2 := range ents {
			code, isCode := e2.(*tg.MessageEntityCode)
			if !isCode {
				continue
			}
			if pre.Offset == code.Offset && pre.Length == code.Length {
				t.Fatalf("duplicate pre and code entity found at same bounds [%d, %d] (causes ENTITY_BOUNDS_INVALID in Telegram)", pre.Offset, pre.Offset+pre.Length)
			}
		}
	}
}

func TestQRPayloadBoundsAndWhitespace(t *testing.T) {
	trimmed, err := GenerateQRPNG("  hello  ")
	if err != nil || len(trimmed) == 0 {
		t.Fatalf("trimmed QR payload failed: len=%d err=%v", len(trimmed), err)
	}
	if _, err := GenerateQRPNG(strings.Repeat("x", maxQRPayloadBytes+1)); err == nil {
		t.Fatal("expected oversized QR payload to be rejected")
	}
	if _, err := RenderQRCompact(strings.Repeat("x", maxQRPayloadBytes+1)); err == nil {
		t.Fatal("expected oversized compact QR payload to be rejected")
	}
}
