package menu

import (
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/assistant/callback"
)

func TestParseSettingRef(t *testing.T) {
	ns, key, err := parseSettingRef("pmpermit:max_warns")
	if err != nil || ns != "pmpermit" || key != "max_warns" {
		t.Fatalf("unexpected parse result: %q %q %v", ns, key, err)
	}
}

func TestLegacySettingsPayloadStillFitsCompatibilityLimit(t *testing.T) {
	for _, data := range []string{
		"a1:settings:home",
		"a1:settings:category:general",
		"a1:settings:info:core:prefix",
		"a1:settings:set:core:prefix:next",
		"a1:settings:reset:core:prefix:reset",
	} {
		if len([]byte(data)) > callback.MaxCallbackDataLen {
			t.Fatalf("callback payload too long (%d): %q", len([]byte(data)), data)
		}
	}
}

func TestClassicSettingsScreenIsCutoverNotice(t *testing.T) {
	screen := BuildSettingsScreen("TestBot")
	if screen == nil {
		t.Fatal("settings screen is nil")
	}
	if !strings.Contains(screen.Text(), "Settings moved to a2") || !strings.Contains(screen.Text(), "/start") {
		t.Fatalf("unexpected settings cutover text: %q", screen.Text())
	}
	for _, row := range screen.Rows {
		for _, button := range row {
			if strings.HasPrefix(string(button.Data), "a1:settings:") {
				t.Fatalf("cutover screen generated new legacy Settings callback: %q", button.Data)
			}
		}
	}
}

func TestClassicRootNoLongerGeneratesSettingsEntry(t *testing.T) {
	screen := BuildStartScreen("TestBot", 0)
	for _, row := range screen.Rows {
		for _, button := range row {
			if string(button.Data) == "a1:assistant:settings" {
				t.Fatal("classic root still generates legacy Settings entry")
			}
		}
	}
}
