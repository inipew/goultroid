package shell

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestHelpModulesDeterministicAndDirectGridViews(t *testing.T) {
	commands := []core.Command{
		{Name: "zeta", Category: "System", Description: "Last", Permission: core.PermissionOwner},
		{Name: "alpha", Category: "Media", Description: "First", Usage: "alpha <url>", Aliases: []string{"a"}, Permission: core.PermissionSudo, Cooldown: time.Second, Timeout: 5 * time.Second},
		{Name: "beta", Category: "Media", Description: "Second"},
		{Name: "general", Description: "General command"},
	}
	modules := HelpModules(commands)
	if len(modules) != 3 || modules[0].Name != "General" || modules[1].Name != "Media" || modules[2].Name != "System" {
		t.Fatalf("modules = %+v", modules)
	}

	root := HelpView(HelpModel{Commands: commands, Page: 0})
	if err := root.Validate(); err != nil {
		t.Fatalf("HelpView() invalid: %v", err)
	}
	if len(root.Rows) != 3 || len(root.Rows[0]) != 2 || root.Rows[0][0].Text != "General" || root.Rows[0][1].Text != "Media" {
		t.Fatalf("root help grid = %+v", root.Rows)
	}
	if root.Rows[0][0].ActionID != helpModuleSlotActions[0] || root.Rows[0][1].ActionID != helpModuleSlotActions[1] || root.Rows[1][0].ActionID != helpModuleSlotActions[2] {
		t.Fatalf("root help slot actions = %+v", root.Rows)
	}

	module := HelpModuleView(HelpModuleModel{Module: modules[1], ModuleIndex: 1, ModuleTotal: 3, Page: 0})
	if err := module.Validate(); err != nil {
		t.Fatalf("HelpModuleView() invalid: %v", err)
	}
	if len(module.Rows) != 2 || len(module.Rows[0]) != 2 || module.Rows[0][0].Text != "/alpha" || module.Rows[0][1].Text != "/beta" {
		t.Fatalf("module command grid = %+v", module.Rows)
	}
	if module.Rows[0][0].ActionID != helpCommandSlotActions[0] || module.Rows[0][1].ActionID != helpCommandSlotActions[1] {
		t.Fatalf("module slot actions = %+v", module.Rows)
	}

	detail := HelpCommandView(HelpCommandModel{Command: modules[1].Commands[0]})
	if err := detail.Validate(); err != nil {
		t.Fatalf("HelpCommandView() invalid: %v", err)
	}
	for _, want := range []string{"/alpha", "alpha &lt;url&gt;", "/a", "Media", "Sudo", "1s", "5s"} {
		if !strings.Contains(detail.Text, want) {
			t.Fatalf("detail missing %q: %q", want, detail.Text)
		}
	}
	if len(detail.Rows) < 1 || len(detail.Rows[0]) != 2 {
		t.Fatalf("detail navigation row = %+v", detail.Rows)
	}
	if detail.Rows[0][0].ActionID != ActionHelpBack || detail.Rows[0][0].Text != "« Bᴀᴄᴋ" {
		t.Fatalf("detail command back button = %+v", detail.Rows[0][0])
	}
	if detail.Rows[0][1].ActionID != ActionHelp || detail.Rows[0][1].Text != "Modules" {
		t.Fatalf("detail modules shortcut = %+v", detail.Rows[0][1])
	}
}

func TestHelpGridStateIsBoundedAndRejectsCatalogRemap(t *testing.T) {
	commands := make([]core.Command, 0, 10)
	for i := 0; i < 10; i++ {
		commands = append(commands, core.Command{Name: fmt.Sprintf("cmd%02d", i), Category: fmt.Sprintf("Cat%02d", i)})
	}
	state := BindSettingState(SettingDetailState(InitialState(), 1), "core", "prefix", 9)
	state = HelpState(state, commands, true)
	decoded := DecodeState(state)
	if decoded.Screen != ScreenHelp || decoded.CategoryIndex != 0 || decoded.SettingIndex != 0 || decoded.SchemaVersion != 0 {
		t.Fatalf("help state = %+v", decoded)
	}
	if decoded.SettingBinding == ([bindingBytes]byte{}) || SettingBindingMatches(state, "core", "prefix") {
		t.Fatalf("help state did not replace setting authority with help fingerprint: %+v", decoded)
	}

	var ok bool
	state, ok = StepHelpModuleState(state, commands, 1)
	if !ok || DecodeState(state).CategoryIndex != 1 {
		t.Fatalf("next help page state = %+v ok=%v", DecodeState(state), ok)
	}
	state, module, moduleIndex, ok := OpenHelpModuleSlotState(state, commands, 0)
	if !ok || moduleIndex != 8 || module.Name != "Cat08" {
		t.Fatalf("module slot = index:%d module:%+v ok=%v", moduleIndex, module, ok)
	}
	state, command, _, commandIndex, ok := OpenHelpCommandSlotState(state, commands, 0)
	if !ok || commandIndex != 0 || command.Name != "cmd08" {
		t.Fatalf("command slot = index:%d command:%+v ok=%v", commandIndex, command, ok)
	}
	state, module, moduleIndex, ok = BackHelpModuleState(state, commands)
	if !ok || moduleIndex != 8 || DecodeState(state).SettingIndex != 0 {
		t.Fatalf("back module = index:%d state:%+v ok=%v", moduleIndex, DecodeState(state), ok)
	}

	root := HelpState(InitialState(), commands, true)
	changed := append([]core.Command{{Name: "aardvark", Category: "Aardvark"}}, commands...)
	if _, _, _, ok := OpenHelpModuleSlotState(root, changed, 0); ok {
		t.Fatal("slot remapped after catalog change instead of failing stale")
	}

	many := make([]core.Command, 0, 10)
	for i := 0; i < 10; i++ {
		many = append(many, core.Command{Name: fmt.Sprintf("item%02d", i), Category: "Media"})
	}
	state = HelpState(InitialState(), many, true)
	state, _, _, ok = OpenHelpModuleSlotState(state, many, 0)
	if !ok {
		t.Fatal("open Media module failed")
	}
	state, _, _, ok = StepHelpCommandState(state, many, 1)
	if !ok || DecodeState(state).SettingIndex != 1 {
		t.Fatalf("next command page state = %+v ok=%v", DecodeState(state), ok)
	}
	state, command, _, commandIndex, ok = OpenHelpCommandSlotState(state, many, 1)
	if !ok || commandIndex != 9 || command.Name != "item09" {
		t.Fatalf("second command page slot = index:%d command:%+v ok=%v", commandIndex, command, ok)
	}
	state, _, _, ok = BackHelpModuleState(state, many)
	if !ok || DecodeState(state).SettingIndex != 1 {
		t.Fatalf("command page was not preserved on back: %+v ok=%v", DecodeState(state), ok)
	}
}

func TestPublicStartViewIsCompactLocalizedAndRelayAware(t *testing.T) {
	base := PublicStartView(PublicStartModel{
		Username: "bot<unsafe>",
		Locale:   "en",
	})
	if !strings.Contains(base.Text, "Hey there! This is @bot&lt;unsafe&gt;, the GoUltroid Assistant.") {
		t.Fatalf("public start text = %q", base.Text)
	}
	if strings.Contains(base.Text, "Send your message") {
		t.Fatalf("relay-disabled public start advertised relay: %q", base.Text)
	}
	if len(base.Rows) != 0 {
		t.Fatalf("public start rows = %+v, want stateless text-only view", base.Rows)
	}

	relay := PublicStartView(PublicStartModel{
		Username:       "goultroidbot",
		Locale:         "id",
		RelayAvailable: true,
	})
	if !strings.Contains(relay.Text, "Halo! Ini @goultroidbot, GoUltroid Assistant.") ||
		!strings.Contains(relay.Text, "Kirim pesan Anda dan saya akan meneruskannya ke owner.") {
		t.Fatalf("localized relay public start = %q", relay.Text)
	}
}
