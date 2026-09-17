package presentation

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/inipew/goultroid/internal/ui"
)

const (
	MaxScreenRows         = 20
	MaxButtonsPerRow      = 8
	MaxTotalButtons       = 100
	MaxCallbackDataBytes  = 64
	MaxTelegramTextLength = 4096
)

var allowedURLSchemes = map[string]bool{
	"http":  true,
	"https": true,
	"tg":    true,
}

// ValidateScreen ensures a built ui.Screen complies with UI, Telegram, and security invariants.
func ValidateScreen(screen *ui.Screen, result BuildResult) error {
	if screen == nil {
		return fmt.Errorf("%w: screen is nil", ErrOutputValidationFailed)
	}
	if strings.TrimSpace(screen.ID) == "" {
		return fmt.Errorf("%w: screen ID cannot be empty", ErrOutputValidationFailed)
	}

	renderedText := screen.Text()
	if len(renderedText) > MaxTelegramTextLength {
		return fmt.Errorf("%w: rendered text exceeds Telegram limit of %d characters (got %d)",
			ErrOutputValidationFailed, MaxTelegramTextLength, len(renderedText))
	}

	if result.Sensitivity == SensitivitySensitive && result.CacheControl == CacheControlGlobal {
		return fmt.Errorf("%w: sensitive screens must not use global cache control", ErrOutputValidationFailed)
	}

	if len(screen.Rows) > MaxScreenRows {
		return fmt.Errorf("%w: screen row count %d exceeds maximum of %d",
			ErrOutputValidationFailed, len(screen.Rows), MaxScreenRows)
	}

	totalButtons := 0
	for rowIdx, row := range screen.Rows {
		if len(row) > MaxButtonsPerRow {
			return fmt.Errorf("%w: row %d button count %d exceeds maximum of %d",
				ErrOutputValidationFailed, rowIdx, len(row), MaxButtonsPerRow)
		}
		for btnIdx, btn := range row {
			totalButtons++
			if totalButtons > MaxTotalButtons {
				return fmt.Errorf("%w: total buttons exceed limit of %d",
					ErrOutputValidationFailed, MaxTotalButtons)
			}

			if strings.TrimSpace(btn.Text) == "" {
				return fmt.Errorf("%w: row %d button %d has empty text",
					ErrOutputValidationFailed, rowIdx, btnIdx)
			}

			switch btn.Type {
			case ui.ButtonCallback:
				if len(btn.Data) > MaxCallbackDataBytes {
					return fmt.Errorf("%w: callback data exceeds %d bytes (got %d: %q)",
						ErrOutputValidationFailed, MaxCallbackDataBytes, len(btn.Data), btn.Data)
				}
			case ui.ButtonURL:
				if btn.URL == "" {
					return fmt.Errorf("%w: URL button has empty URL", ErrOutputValidationFailed)
				}
				parsed, err := url.Parse(btn.URL)
				if err != nil || !allowedURLSchemes[strings.ToLower(parsed.Scheme)] {
					return fmt.Errorf("%w: URL button has disallowed or invalid scheme: %q",
						ErrOutputValidationFailed, btn.URL)
				}
			}
		}
	}

	return nil
}
