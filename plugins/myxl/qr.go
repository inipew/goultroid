package myxl

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"rsc.io/qr"
)

const (
	maxQRPayloadBytes    = 2953
	maxQRPNGBytes        = 1 << 20
	maxCompactQRRunes    = 3900
	maxQRImageDimension  = 768
	maxInlineQRPreview   = 512
)

func normalizeQRPayload(data string) (string, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return "", fmt.Errorf("empty QR code data")
	}
	if len(data) > maxQRPayloadBytes {
		return "", fmt.Errorf("QR code payload too large: %d bytes (max %d)", len(data), maxQRPayloadBytes)
	}
	return data, nil
}

// RenderQRCompact renders a QR code string into a compact text block
// using Unicode half-block characters (█, ▀, ▄, ' ') with a 2-module quiet zone padding.
// Each text line represents two vertical QR modules, reducing the height by half
// so it fits comfortably within Telegram message bubbles and monospaced text.
func RenderQRCompact(data string) (string, error) {
	data, err := normalizeQRPayload(data)
	if err != nil {
		return "", err
	}

	code, err := qr.Encode(data, qr.M)
	if err != nil {
		// Fallback to qr.L if qr.M exceeds capacity
		var fallbackErr error
		code, fallbackErr = qr.Encode(data, qr.L)
		if fallbackErr != nil {
			return "", fmt.Errorf("failed to encode QR code: %w", err)
		}
	}

	width := code.Size
	quiet := 2
	paddedWidth := width + quiet*2
	paddedHeight := width + quiet*2

	// isDark returns true if the module at row r, col c (including quiet zone) is black/dark.
	isDark := func(r, c int) bool {
		qrRow := r - quiet
		qrCol := c - quiet
		if qrRow < 0 || qrRow >= width || qrCol < 0 || qrCol >= width {
			return false // quiet zone is light/white
		}
		return code.Black(qrCol, qrRow)
	}

	var sb strings.Builder
	// Each text row covers two module rows (r and r+1)
	for r := 0; r < paddedHeight; r += 2 {
		for c := 0; c < paddedWidth; c++ {
			top := isDark(r, c)
			bottom := false
			if r+1 < paddedHeight {
				bottom = isDark(r+1, c)
			}

			switch {
			case top && bottom:
				sb.WriteRune('█') // U+2588 Full block (both dark)
			case top && !bottom:
				sb.WriteRune('▀') // U+2580 Upper half block (top dark, bottom light)
			case !top && bottom:
				sb.WriteRune('▄') // U+2584 Lower half block (top light, bottom dark)
			default:
				sb.WriteRune(' ') // Space (both light)
			}
		}
		sb.WriteByte('\n')
	}

	rendered := strings.TrimRight(sb.String(), "\n")
	if utf8.RuneCountInString(rendered) > maxCompactQRRunes {
		return "", fmt.Errorf("compact QR output too large for Telegram text; use PNG output")
	}
	return rendered, nil
}

func qrScaleForModules(modules int) int {
	if modules <= 0 {
		return 10
	}
	scale := 10
	if modules*scale > maxQRImageDimension {
		scale = maxQRImageDimension / modules
	}
	if scale < 2 {
		scale = 2
	}
	return scale
}

func inlineQRPreview(data string) (string, bool) {
	data = strings.TrimSpace(data)
	if data == "" {
		return "", false
	}
	runes := []rune(data)
	if len(runes) <= maxInlineQRPreview {
		return data, false
	}
	return string(runes[:maxInlineQRPreview]) + "…", true
}

// GenerateQRPNG encodes data into a high-resolution PNG image byte slice
// with standard 4-module quiet zone padding.
func GenerateQRPNG(data string) ([]byte, error) {
	data, err := normalizeQRPayload(data)
	if err != nil {
		return nil, err
	}

	code, err := qr.Encode(data, qr.M)
	if err != nil {
		var fallbackErr error
		code, fallbackErr = qr.Encode(data, qr.L)
		if fallbackErr != nil {
			return nil, fmt.Errorf("failed to encode QR code: %w", err)
		}
	}

	code.Scale = qrScaleForModules(code.Size)
	pngBytes := code.PNG()
	if len(pngBytes) == 0 {
		return nil, fmt.Errorf("empty PNG output generated")
	}
	if len(pngBytes) > maxQRPNGBytes {
		return nil, fmt.Errorf("generated QR PNG too large: %d bytes (max %d)", len(pngBytes), maxQRPNGBytes)
	}
	return pngBytes, nil
}
