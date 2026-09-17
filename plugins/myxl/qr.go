package myxl

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// RenderQRCompact renders a QR code string into a compact text block
// using Unicode half-block characters (█, ▀, ▄, ' ') with a 2-module quiet zone padding.
// Each text line represents two vertical QR modules, reducing the height by half
// so it fits comfortably within Telegram message bubbles and monospaced text.
func RenderQRCompact(data string) (string, error) {
	if data == "" {
		return "", fmt.Errorf("empty QR code data")
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

	return strings.TrimRight(sb.String(), "\n"), nil
}

// GenerateQRPNG encodes data into a high-resolution PNG image byte slice
// with standard 4-module quiet zone padding.
func GenerateQRPNG(data string) ([]byte, error) {
	if data == "" {
		return nil, fmt.Errorf("empty QR code data")
	}

	code, err := qr.Encode(data, qr.M)
	if err != nil {
		var fallbackErr error
		code, fallbackErr = qr.Encode(data, qr.L)
		if fallbackErr != nil {
			return nil, fmt.Errorf("failed to encode QR code: %w", err)
		}
	}

	// Scale = 10 gives sharp ~400-550px output, ideal for camera and gallery QR scanners.
	code.Scale = 10
	pngBytes := code.PNG()
	if len(pngBytes) == 0 {
		return nil, fmt.Errorf("empty PNG output generated")
	}
	return pngBytes, nil
}
