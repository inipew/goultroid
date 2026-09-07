package menu

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/settings"
)

func TestNextSettingValueIntRespectsBounds(t *testing.T) {
	min, max := int64(1), int64(5)
	def := &settings.SettingDefinition{Namespace: "test", Key: "value", Type: settings.TypeInt, MinVal: &min, MaxVal: &max, UI: settings.UIHint{Step: 2}}

	got, changed, err := nextSettingValue(def, "5", "next")
	if err != nil || !changed || got != "1" {
		t.Fatalf("next at max: got=%q changed=%v err=%v", got, changed, err)
	}
	got, changed, err = nextSettingValue(def, "1", "prev")
	if err != nil || !changed || got != "5" {
		t.Fatalf("prev at min: got=%q changed=%v err=%v", got, changed, err)
	}
}

func TestNextSettingValueDurationRespectsBounds(t *testing.T) {
	min, max := int64(5), int64(20)
	def := &settings.SettingDefinition{Namespace: "test", Key: "duration", Type: settings.TypeDuration, MinVal: &min, MaxVal: &max, UI: settings.UIHint{Step: 5}}

	got, _, err := nextSettingValue(def, "20s", "next")
	if err != nil || got != "20s" {
		t.Fatalf("next at duration max: got=%q err=%v", got, err)
	}
	got, _, err = nextSettingValue(def, "5s", "prev")
	if err != nil || got != "5s" {
		t.Fatalf("prev at duration min: got=%q err=%v", got, err)
	}
}

func TestParseSettingStatePreservesOperation(t *testing.T) {
	ns, key, op, err := parseSettingState("pmpermit:max_warns:prev")
	if err != nil || ns != "pmpermit" || key != "max_warns" || op != "prev" {
		t.Fatalf("unexpected parse result: %q %q %q %v", ns, key, op, err)
	}
}

func TestSettingsCallbackPayloadsFitTelegramLimit(t *testing.T) {
	defs := []settings.SettingDefinition{
		{Namespace: "core", Key: "prefix"},
		{Namespace: "pmpermit", Key: "max_warns"},
		{Namespace: "antispam", Key: "action"},
	}
	for _, def := range defs {
		for _, data := range []string{
			"a1:settings:info:" + def.Namespace + ":" + def.Key,
			"a1:settings:set:" + def.Namespace + ":" + def.Key + ":next",
			"a1:settings:reset:" + def.Namespace + ":" + def.Key + ":reset",
		} {
			if len([]byte(data)) > callback.MaxCallbackDataLen {
				t.Fatalf("callback payload too long (%d): %q", len([]byte(data)), data)
			}
		}
	}
}

func TestPendingSettingTTLIsFinite(t *testing.T) {
	if pendingSettingTTL <= 0 || pendingSettingTTL > 10*time.Minute {
		t.Fatalf("unexpected pending setting TTL: %s", pendingSettingTTL)
	}
	if strings.TrimSpace(string(settings.TypeString)) == "" {
		t.Fatal("string setting type must be non-empty")
	}
}
