package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestHelpModulesDeterministicAndDetailedViews(t *testing.T) {
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
	if len(modules[1].Commands) != 2 || modules[1].Commands[0].Name != "alpha" || modules[1].Commands[1].Name != "beta" {
		t.Fatalf("media commands = %+v", modules[1].Commands)
	}

	root := HelpView(HelpModel{Commands: commands, Selected: 1})
	if err := root.Validate(); err != nil {
		t.Fatalf("HelpView() invalid: %v", err)
	}
	if !strings.Contains(root.Text, "Media · 2") {
		t.Fatalf("root help text = %q", root.Text)
	}
	module := HelpModuleView(HelpModuleModel{Module: modules[1], ModuleIndex: 1, ModuleTotal: 3, Selected: 0})
	if err := module.Validate(); err != nil {
		t.Fatalf("HelpModuleView() invalid: %v", err)
	}
	if !strings.Contains(module.Text, "/alpha") || !strings.Contains(module.Text, "First") {
		t.Fatalf("module text = %q", module.Text)
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
}

func TestHelpNavigatorReusesBoundedStateWithoutTouchingSettingBinding(t *testing.T) {
	state := BindSettingState(OpenSettingState(InitialState(), 1), "core", "prefix", 9)
	state = HelpState(state, true)
	decoded := DecodeState(state)
	if decoded.Screen != ScreenHelp || decoded.CategoryIndex != 0 || decoded.SettingIndex != 0 {
		t.Fatalf("help state = %+v", decoded)
	}
	if decoded.SettingBinding != ([bindingBytes]byte{}) || decoded.SchemaVersion != 0 {
		t.Fatalf("help state retained setting mutation authority: %+v", decoded)
	}

	state = StepHelpModuleState(state, 3, -1)
	state = OpenHelpModuleState(state, 3)
	state = StepHelpCommandState(state, 4, 2)
	state = OpenHelpCommandState(state, 4)
	decoded = DecodeState(state)
	if decoded.CategoryIndex != 2 || decoded.SettingIndex != 2 {
		t.Fatalf("help navigator = %+v", decoded)
	}
	state = BackHelpModuleState(state, 2, 2)
	decoded = DecodeState(state)
	if decoded.CategoryIndex != 1 || decoded.SettingIndex != 1 {
		t.Fatalf("clamped help navigator = %+v", decoded)
	}
}
