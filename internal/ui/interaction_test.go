package ui

import (
	"strings"
	"testing"
	"time"
)

func TestScreenRender(t *testing.T) {
	// 1. Screen with title and buttons
	s := NewScreen("test-screen", "My Title", "This is the screen body text.")
	s.AddRow(
		NewCallbackButton("Btn 1", []byte("v1:test:act1:noop")),
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
	if cbBtn.Type != ButtonCallback || cbBtn.Text != "Btn 1" || string(cbBtn.Data) != "v1:test:act1:noop" {
		t.Errorf("unexpected callback button: %+v", cbBtn)
	}

	urlBtn := markup.Rows[0][1]
	if urlBtn.Type != ButtonURL || urlBtn.Text != "Link" || urlBtn.URL != "https://example.com" {
		t.Errorf("unexpected url button: %+v", urlBtn)
	}

	// 2. Screen without title and no buttons
	s2 := NewScreen("empty", "", "Plain text")
	text2, markup2 := s2.Render()
	if text2 != "Plain text" {
		t.Errorf("expected 'Plain text', got %q", text2)
	}
	if len(markup2.Rows) != 0 {
		t.Errorf("expected empty markup for screen with no rows, got %+v", markup2)
	}
}

func TestNavigator(t *testing.T) {
	nav := NewNavigator("home", map[string]string{"cat": "general"})

	if nav.Depth() != 1 {
		t.Errorf("expected depth 1, got %d", nav.Depth())
	}
	if nav.CanPop() {
		t.Error("expected CanPop to be false at root")
	}
	if nav.Current().ScreenID != "home" || nav.Param("cat") != "general" {
		t.Errorf("unexpected current screen: %+v", nav.Current())
	}

	// Push sub-screen
	nav.Push("detail", map[string]string{"key": "prefix"})
	if nav.Depth() != 2 || !nav.CanPop() {
		t.Errorf("expected depth 2 and CanPop true, got depth=%d", nav.Depth())
	}
	if nav.Current().ScreenID != "detail" || nav.Param("key") != "prefix" {
		t.Errorf("unexpected current screen: %+v", nav.Current())
	}

	// Modify param
	nav.SetParam("key", "new_prefix")
	if nav.Param("key") != "new_prefix" {
		t.Errorf("expected updated param 'new_prefix', got %s", nav.Param("key"))
	}

	// Serialize & Deserialize
	data, err := nav.Serialize()
	if err != nil {
		t.Fatalf("failed to serialize navigator: %v", err)
	}
	deserialized, err := DeserializeNavigator(data)
	if err != nil {
		t.Fatalf("failed to deserialize navigator: %v", err)
	}
	if deserialized.Depth() != 2 || deserialized.Current().ScreenID != "detail" {
		t.Errorf("mismatch after deserialization: %+v", deserialized)
	}

	// Pop
	top, ok := nav.Pop()
	if !ok || top.ScreenID != "detail" {
		t.Errorf("expected pop of 'detail', got %+v, ok=%v", top, ok)
	}
	if nav.Depth() != 1 || nav.CanPop() {
		t.Errorf("expected depth 1 and CanPop false, got depth=%d", nav.Depth())
	}
	if nav.Current().ScreenID != "home" {
		t.Errorf("expected return to 'home', got %s", nav.Current().ScreenID)
	}

	// Cannot pop root
	_, ok = nav.Pop()
	if ok {
		t.Error("expected pop of root to return false")
	}

	// Reset
	nav.Reset("settings", nil)
	if nav.Current().ScreenID != "settings" || nav.Depth() != 1 {
		t.Errorf("unexpected screen after reset: %+v", nav.Current())
	}
}

func TestMenuComponents(t *testing.T) {
	// 1. Toggle Switch
	toggleOn := BuildToggleSwitch(true, "Aktif", "Nonaktif", []byte("toggle"))
	if toggleOn.Text != "✅ Aktif" {
		t.Errorf("expected '✅ Aktif', got %s", toggleOn.Text)
	}
	toggleOff := BuildToggleSwitch(false, "", "", []byte("toggle"))
	if toggleOff.Text != "❌ Disabled" {
		t.Errorf("expected '❌ Disabled', got %s", toggleOff.Text)
	}

	// 2. Stepper
	minV, maxV := int64(1), int64(10)
	noop := []byte("noop")
	stepperMid := BuildStepper(5, &minV, &maxV, []byte("dec"), []byte("inc"), noop)
	if len(stepperMid) != 3 {
		t.Fatalf("expected 3 buttons, got %d", len(stepperMid))
	}
	if stepperMid[0].Text != "➖" || string(stepperMid[0].Data) != "dec" {
		t.Errorf("unexpected dec button: %+v", stepperMid[0])
	}
	if stepperMid[1].Text != "5" {
		t.Errorf("unexpected center button: %+v", stepperMid[1])
	}
	if stepperMid[2].Text != "➕" || string(stepperMid[2].Data) != "inc" {
		t.Errorf("unexpected inc button: %+v", stepperMid[2])
	}

	// Stepper at min
	stepperMin := BuildStepper(1, &minV, &maxV, []byte("dec"), []byte("inc"), noop)
	if stepperMin[0].Text != "⏹" || string(stepperMin[0].Data) != "noop" {
		t.Errorf("expected disabled min button: %+v", stepperMin[0])
	}

	// Stepper at max
	stepperMax := BuildStepper(10, &minV, &maxV, []byte("dec"), []byte("inc"), noop)
	if stepperMax[2].Text != "⏹" || string(stepperMax[2].Data) != "noop" {
		t.Errorf("expected disabled max button: %+v", stepperMax[2])
	}

	// 3. Selector
	opts := []string{"warn", "mute", "kick", "ban"}
	selBtns := BuildSelector(opts, "mute", func(opt string) []byte {
		return []byte("sel:" + opt)
	})
	if len(selBtns) != 4 {
		t.Fatalf("expected 4 buttons, got %d", len(selBtns))
	}
	if selBtns[0].Text != "○ warn" {
		t.Errorf("expected '○ warn', got %s", selBtns[0].Text)
	}
	if selBtns[1].Text != "● mute" {
		t.Errorf("expected '● mute', got %s", selBtns[1].Text)
	}

	// 4. Pagination row
	pageRow := BuildPaginationRow(2, 5, func(page int) []byte {
		return []byte("page")
	}, noop)
	if len(pageRow) != 3 {
		t.Fatalf("expected 3 pagination buttons, got %d", len(pageRow))
	}
	if pageRow[0].Text != "◀ Prev" || pageRow[1].Text != "2 / 5" || pageRow[2].Text != "Next ▶" {
		t.Errorf("unexpected pagination buttons: %+v", pageRow)
	}

	// Single page returns nil
	singleRow := BuildPaginationRow(1, 1, nil, noop)
	if singleRow != nil {
		t.Errorf("expected nil for single page pagination, got %+v", singleRow)
	}

	// 5. Nav row
	navRow := BuildNavRow([]byte("back"), []byte("home"), []byte("close"))
	if len(navRow) != 3 {
		t.Fatalf("expected 3 nav buttons, got %d", len(navRow))
	}
	if navRow[0].Text != "🔙 Back" || navRow[1].Text != "🏠 Home" || navRow[2].Text != "❌ Close" {
		t.Errorf("unexpected nav row: %+v", navRow)
	}

	// 6. PaginateSlice
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

func TestToast(t *testing.T) {
	// Nil context safe
	if err := AnswerToast(nil, "hello", false); err != nil {
		t.Errorf("expected nil error on nil context: %v", err)
	}
	if err := AnswerSuccessToast(nil, ""); err != nil {
		t.Errorf("expected nil error on nil context: %v", err)
	}
	if err := AnswerErrorToast(nil, ""); err != nil {
		t.Errorf("expected nil error on nil context: %v", err)
	}
}

func TestAdvancedUIPrimitives(t *testing.T) {
	// 1. StateToggle
	stOn := BuildStateToggle(true, "AntiFlood", []byte("toggle"))
	if stOn.Text != "🟢 AntiFlood: ON" {
		t.Errorf("expected '🟢 AntiFlood: ON', got %s", stOn.Text)
	}
	stOff := BuildStateToggle(false, "AntiFlood", []byte("toggle"))
	if stOff.Text != "🔴 AntiFlood: OFF" {
		t.Errorf("expected '🔴 AntiFlood: OFF', got %s", stOff.Text)
	}

	// 2. MultiStepStepper
	minV, maxV := int64(0), int64(100)
	noop := []byte("noop")
	stepRow := BuildMultiStepStepper(50, &minV, &maxV, func(delta int64) []byte {
		return []byte("step")
	}, noop)
	if len(stepRow) != 5 {
		t.Fatalf("expected 5 buttons, got %d", len(stepRow))
	}
	if stepRow[0].Text != "−10" || stepRow[1].Text != "−1" || stepRow[2].Text != "50" || stepRow[3].Text != "+1" || stepRow[4].Text != "+10" {
		t.Errorf("unexpected multi-step stepper texts: %+v", stepRow)
	}

	// 3. MultiSelector
	opts := []string{"Errors", "Warnings", "Success", "Debug"}
	selected := map[string]bool{"Errors": true, "Warnings": true}
	saveBtn := NewCallbackButton("💾 Save", []byte("save"))
	multiRows := BuildMultiSelector(opts, selected, func(opt string) []byte {
		return []byte("toggle:" + opt)
	}, &saveBtn)
	if len(multiRows) != 3 { // 2 rows of items + 1 row for save
		t.Fatalf("expected 3 rows, got %d", len(multiRows))
	}
	if multiRows[0][0].Text != "☑ Errors" || multiRows[0][1].Text != "☑ Warnings" {
		t.Errorf("unexpected checked items in row 0: %+v", multiRows[0])
	}
	if multiRows[1][0].Text != "☐ Success" || multiRows[1][1].Text != "☐ Debug" {
		t.Errorf("unexpected unchecked items in row 1: %+v", multiRows[1])
	}
	if multiRows[2][0].Text != "💾 Save" {
		t.Errorf("unexpected save button: %+v", multiRows[2])
	}

	// 4. SegmentedSlider
	levels := []string{"0%", "25%", "50%", "75%", "100%"}
	sliderRow := BuildSegmentedSlider(levels, 2, func(idx int) []byte {
		return []byte("vol")
	})
	if len(sliderRow) != 5 {
		t.Fatalf("expected 5 buttons, got %d", len(sliderRow))
	}
	if sliderRow[2].Text != "50% ●" {
		t.Errorf("expected active marker on 50%%, got %s", sliderRow[2].Text)
	}
	if sliderRow[0].Text != "0%" {
		t.Errorf("expected '0%%', got %s", sliderRow[0].Text)
	}

	// 5. DurationPicker
	durRows := BuildDurationPicker(nil, 5*time.Minute, func(dur time.Duration) []byte {
		return []byte("dur")
	})
	if len(durRows) < 3 {
		t.Fatalf("expected at least 3 duration rows, got %d", len(durRows))
	}
	foundActive := false
	for _, r := range durRows {
		for _, b := range r {
			if strings.Contains(b.Text, "5m") && strings.Contains(b.Text, "●") {
				foundActive = true
			}
		}
	}
	if !foundActive {
		t.Error("expected 5m ● to be present in duration picker")
	}

	// 6. ConfirmationCard
	confText, confMarkup := BuildConfirmationCard(
		"Delete All Jobs",
		"This will delete ALL scheduled tasks.",
		map[string]string{"Count": "12", "Scope": "Chat"},
		"Yes, Delete", []byte("confirm_del"),
		"Cancel", []byte("cancel"),
	)
	if !strings.Contains(confText, "Delete All Jobs") || len(confMarkup.Rows) == 0 {
		t.Errorf("unexpected confirmation card output: %s", confText)
	}

	// 7. PreviewActionCard
	prevText, prevMarkup := BuildPreviewActionCard(
		"User Moderation",
		"Ban User",
		map[string]string{"User": "@badactor", "Reason": "Spam"},
		"🔨 Ban", []byte("ban"),
		"✏️ Edit", []byte("edit"),
		[]byte("cancel"),
	)
	if !strings.Contains(prevText, "User Moderation") || len(prevMarkup.Rows) == 0 {
		t.Errorf("unexpected preview action card: %s", prevText)
	}

	// 8. ActionBars
	userActions := BuildUserActionBar(12345, "badactor", func(act string) []byte {
		return []byte("user:" + act)
	})
	if len(userActions) != 2 || len(userActions[0]) != 2 {
		t.Fatalf("expected 2x2 user action bar, got %+v", userActions)
	}

	chatActions := BuildChatActionBar(-100123, func(act string) []byte {
		return []byte("chat:" + act)
	})
	if len(chatActions) != 2 || len(chatActions[0]) != 2 {
		t.Fatalf("expected 2x2 chat action bar, got %+v", chatActions)
	}

	msgActions := BuildMessageActionBar(456, func(act string) []byte {
		return []byte("msg:" + act)
	})
	if len(msgActions) != 2 || len(msgActions[0]) != 2 {
		t.Fatalf("expected 2x2 msg action bar, got %+v", msgActions)
	}

	// 9. Loading & Retry
	loadingBtn := BuildLoadingButton("")
	if loadingBtn.Text != "⏳ Processing..." {
		t.Errorf("expected loading text, got %s", loadingBtn.Text)
	}

	retryRow := BuildRetryRow([]byte("retry"), []byte("details"), []byte("cancel"))
	if len(retryRow) != 3 {
		t.Fatalf("expected 3 retry buttons, got %d", len(retryRow))
	}

	// 10. Wizard
	wiz := NewWizard("Create Filter", 4)
	wizText, wizMarkup := wiz.RenderStep(
		2,
		"Choose Action",
		"Select the punishment action when filter triggers:",
		[]ButtonRow{
			{NewCallbackButton("Mute", []byte("act:mute")), NewCallbackButton("Ban", []byte("act:ban"))},
		},
		[]byte("back"),
		[]byte("cancel"),
		[]byte("next"),
	)
	if !strings.Contains(wizText, "Step 2 / 4: Choose Action") || len(wizMarkup.Rows) == 0 {
		t.Errorf("unexpected wizard step output: %s", wizText)
	}
}

