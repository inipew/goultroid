package ui

import (
	"fmt"
	"strings"
	"time"
)

// BuildToggleSwitch creates an inline button representing an on/off toggle.
func BuildToggleSwitch(currentVal bool, onText, offText string, callbackData []byte) Button {
	if onText == "" {
		onText = "Enabled"
	}
	if offText == "" {
		offText = "Disabled"
	}

	if currentVal {
		return NewCallbackButton("✅ "+onText, callbackData)
	}
	return NewCallbackButton("❌ "+offText, callbackData)
}

// BuildStateToggle creates an inline button displaying status badge directly in button text:
// 🟢 Label: ON / 🔴 Label: OFF
func BuildStateToggle(currentVal bool, label string, callbackData []byte) Button {
	if currentVal {
		return NewCallbackButton(fmt.Sprintf("🟢 %s: ON", label), callbackData)
	}
	return NewCallbackButton(fmt.Sprintf("🔴 %s: OFF", label), callbackData)
}

// BuildStepper produces a row of 3 buttons: [ ➖ ] [ value ] [ ➕ ].
func BuildStepper(currentVal int64, minVal, maxVal *int64, decData, incData, noopData []byte) ButtonRow {
	minusText := "➖"
	plusText := "➕"

	var actualDecData, actualIncData []byte
	if minVal != nil && currentVal <= *minVal {
		minusText = "⏹"
		actualDecData = noopData
	} else {
		actualDecData = decData
	}

	if maxVal != nil && currentVal >= *maxVal {
		plusText = "⏹"
		actualIncData = noopData
	} else {
		actualIncData = incData
	}

	return ButtonRow{
		NewCallbackButton(minusText, actualDecData),
		NewCallbackButton(fmt.Sprintf("%d", currentVal), noopData),
		NewCallbackButton(plusText, actualIncData),
	}
}

// BuildMultiStepStepper creates a fine/coarse 5-button stepper:
// [ -10 ] [ -1 ] [ currentVal ] [ +1 ] [ +10 ]
func BuildMultiStepStepper(currentVal int64, minVal, maxVal *int64, makeStepData func(delta int64) []byte, noopData []byte) ButtonRow {
	var row ButtonRow

	// -10
	if minVal != nil && currentVal-10 < *minVal {
		row = append(row, NewCallbackButton("⏹ -10", noopData))
	} else {
		row = append(row, NewCallbackButton("−10", makeStepData(-10)))
	}

	// -1
	if minVal != nil && currentVal-1 < *minVal {
		row = append(row, NewCallbackButton("⏹ -1", noopData))
	} else {
		row = append(row, NewCallbackButton("−1", makeStepData(-1)))
	}

	// Value
	row = append(row, NewCallbackButton(fmt.Sprintf("%d", currentVal), noopData))

	// +1
	if maxVal != nil && currentVal+1 > *maxVal {
		row = append(row, NewCallbackButton("+1 ⏹", noopData))
	} else {
		row = append(row, NewCallbackButton("+1", makeStepData(1)))
	}

	// +10
	if maxVal != nil && currentVal+10 > *maxVal {
		row = append(row, NewCallbackButton("+10 ⏹", noopData))
	} else {
		row = append(row, NewCallbackButton("+10", makeStepData(10)))
	}

	return row
}

// BuildSelector produces inline buttons for single-choice selection with radio markers.
func BuildSelector(options []string, selected string, makeSelectData func(opt string) []byte) ButtonRow {
	buttons := make(ButtonRow, 0, len(options))
	for _, opt := range options {
		text := "○ " + opt
		if strings.EqualFold(opt, selected) {
			text = "● " + opt
		}
		buttons = append(buttons, NewCallbackButton(text, makeSelectData(opt)))
	}
	return buttons
}

// BuildMultiSelector produces rows of buttons for multiple-choice selection with checkbox markers:
// ☑ Option (selected) or ☐ Option (unselected)
func BuildMultiSelector(options []string, selectedMap map[string]bool, makeToggleData func(opt string) []byte, saveBtn *Button) []ButtonRow {
	var rows []ButtonRow
	var row ButtonRow

	for _, opt := range options {
		text := "☐ " + opt
		if selectedMap != nil && selectedMap[opt] {
			text = "☑ " + opt
		}
		btn := NewCallbackButton(text, makeToggleData(opt))
		row = append(row, btn)
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	if saveBtn != nil {
		rows = append(rows, ButtonRow{*saveBtn})
	}

	return rows
}

// BuildSegmentedSlider produces a row of step buttons representing a bounded value scale
// (e.g. Volume: [ 0% ] [ 25% ] [ 50% ● ] [ 75% ] [ 100% ]).
func BuildSegmentedSlider(steps []string, activeIndex int, makeStepData func(idx int) []byte) ButtonRow {
	row := make(ButtonRow, 0, len(steps))
	for i, step := range steps {
		text := step
		if i == activeIndex {
			text = step + " ●"
		}
		row = append(row, NewCallbackButton(text, makeStepData(i)))
	}
	return row
}

// BuildDurationPicker produces rows of preset duration buttons (e.g. 10s, 1m, 5m, 1h, 1d).
func BuildDurationPicker(presets []time.Duration, selected time.Duration, makeSelectData func(dur time.Duration) []byte) []ButtonRow {
	if len(presets) == 0 {
		presets = []time.Duration{
			10 * time.Second,
			30 * time.Second,
			1 * time.Minute,
			5 * time.Minute,
			15 * time.Minute,
			30 * time.Minute,
			1 * time.Hour,
			6 * time.Hour,
			24 * time.Hour,
		}
	}

	var rows []ButtonRow
	var row ButtonRow

	for _, dur := range presets {
		text := dur.String()
		if dur == selected {
			text = dur.String() + " ●"
		}
		row = append(row, NewCallbackButton(text, makeSelectData(dur)))
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	return rows
}

// BuildPaginationRow returns a standard 3-button pagination row [◀ Prev] [curr/total] [Next ▶].
func BuildPaginationRow(currentPage, totalPages int, makePageData func(page int) []byte, noopData []byte) ButtonRow {
	if totalPages <= 1 {
		return nil
	}

	var prevBtn Button
	if currentPage > 1 {
		prevBtn = NewCallbackButton("◀ Prev", makePageData(currentPage-1))
	} else {
		prevBtn = NewCallbackButton("◀", noopData)
	}

	counterBtn := NewCallbackButton(fmt.Sprintf("%d / %d", currentPage, totalPages), noopData)

	var nextBtn Button
	if currentPage < totalPages {
		nextBtn = NewCallbackButton("Next ▶", makePageData(currentPage+1))
	} else {
		nextBtn = NewCallbackButton("▶", noopData)
	}

	return ButtonRow{prevBtn, counterBtn, nextBtn}
}

// BuildNavRow creates a navigation bar row containing Back, Home, and Close buttons.
func BuildNavRow(backData, homeData, closeData []byte) ButtonRow {
	var row ButtonRow
	if len(backData) > 0 {
		row = append(row, NewCallbackButton("🔙 Back", backData))
	}
	if len(homeData) > 0 {
		row = append(row, NewCallbackButton("🏠 Home", homeData))
	}
	if len(closeData) > 0 {
		row = append(row, NewCallbackButton("❌ Close", closeData))
	}
	return row
}

// PaginateSlice slices a list of items for the specified 1-indexed page.
func PaginateSlice[T any](items []T, page, pageSize int) ([]T, int) {
	if pageSize <= 0 {
		pageSize = 6
	}
	totalItems := len(items)
	if totalItems == 0 {
		return nil, 1
	}

	totalPages := (totalItems + pageSize - 1) / pageSize
	if page < 1 {
		page = 1
	} else if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalItems {
		end = totalItems
	}

	return items[start:end], totalPages
}
