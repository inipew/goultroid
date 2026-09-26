package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1BSingleTelegramKeyboardSerializer(t *testing.T) {
	root := repositoryRoot(t)
	encoderPath := filepath.Join(root, "internal", "presentation", "telegram", "markup.go")
	bridgePath := filepath.Join(root, "internal", "presentation", "telegram", "bridge.go")
	legacyPath := filepath.Join(root, "internal", "ui", "render", "telegram.go")

	encoderRaw, err := os.ReadFile(encoderPath)
	if err != nil {
		t.Fatal(err)
	}
	encoder := string(encoderRaw)
	for _, required := range []string{
		"func EncodeMarkup(rows []presentation.CompiledRow) tg.ReplyMarkupClass",
		"tg.KeyboardButtonCallback",
		"tg.KeyboardButtonURL",
		"tg.KeyboardButtonSwitchInline",
		"append([]byte(nil), button.Data...)",
	} {
		if !strings.Contains(encoder, required) {
			t.Fatalf("canonical Telegram keyboard encoder missing %q", required)
		}
	}

	bridgeRaw, err := os.ReadFile(bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	bridge := string(bridgeRaw)
	if strings.Count(bridge, "EncodeMarkup(view.Rows)") != 3 {
		t.Fatalf("presentation bridge must delegate send/message-edit/inline-edit markup to EncodeMarkup, got %d uses", strings.Count(bridge, "EncodeMarkup(view.Rows)"))
	}
	assertNoTelegramButtonConstruction(t, bridgePath, bridge)

	legacyRaw, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := string(legacyRaw)
	if !strings.Contains(legacy, "presentationtelegram.EncodeMarkup(rows)") {
		t.Fatal("legacy ui/render must delegate keyboard serialization to presentation/telegram")
	}
	assertNoTelegramButtonConstruction(t, legacyPath, legacy)
}

func TestP1BPresentationNeverImportsLegacyUI(t *testing.T) {
	root := repositoryRoot(t)
	presentationDir := filepath.Join(root, "internal", "presentation")
	fset := token.NewFileSet()

	err := filepath.Walk(presentationDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if importPath == modulePath+"/internal/ui" || strings.HasPrefix(importPath, modulePath+"/internal/ui/") {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("presentation must not depend on legacy UI: %s", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoTelegramButtonConstruction(t *testing.T, path, source string) {
	t.Helper()
	for _, forbidden := range []string{
		"tg.KeyboardButtonCallback",
		"tg.KeyboardButtonURL",
		"tg.KeyboardButtonSwitchInline",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("%s constructs %s outside canonical encoder", path, forbidden)
		}
	}
}
