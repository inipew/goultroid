package ui

import (
	"fmt"
	"strings"
)

const (
	DefaultProgressFilled = "■"
	DefaultProgressEmpty  = "□"
	DefaultProgressLength = 10
)

// ProgressBar returns a visual progress bar with percentage, e.g.: [■■■■□□□□□□] 40.0%
func ProgressBar(current, total int64, length int) string {
	return ProgressBarCustom(current, total, length, DefaultProgressFilled, DefaultProgressEmpty)
}

// ProgressBarCustom returns a visual progress bar with custom characters.
func ProgressBarCustom(current, total int64, length int, filledChar, emptyChar string) string {
	if length <= 0 {
		length = DefaultProgressLength
	}

	if total <= 0 {
		return fmt.Sprintf("[%s] 0.0%%", strings.Repeat(emptyChar, length))
	}

	if current < 0 {
		current = 0
	}
	if current > total {
		current = total
	}

	pct := float64(current) / float64(total) * 100.0
	filled := int(float64(length) * (float64(current) / float64(total)))
	if filled > length {
		filled = length
	}
	empty := length - filled

	return fmt.Sprintf("[%s%s] %.1f%%",
		strings.Repeat(filledChar, filled),
		strings.Repeat(emptyChar, empty),
		pct,
	)
}

// FormatProgress generates a progress bar with size statistics, e.g. [■■■■□□□□□□] 40.0% (40.0 MB / 100.0 MB).
func FormatProgress(current, total int64, length int) string {
	bar := ProgressBar(current, total, length)
	if total <= 0 {
		return bar
	}
	return fmt.Sprintf("%s (%s / %s)", bar, FormatBytes(current), FormatBytes(total))
}
