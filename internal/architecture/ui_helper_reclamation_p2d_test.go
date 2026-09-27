package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP2DDeadLegacyUIHelpersStayReclaimed(t *testing.T) {
	root := repositoryRoot(t)
	uiRoot := filepath.Join(root, "internal", "ui")

	for _, name := range []string{
		"actions.go",
		"confirmation.go",
		"navigator.go",
		"wizard.go",
	} {
		if _, err := os.Stat(filepath.Join(uiRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("dead legacy UI helper file returned: %s", name)
		}
	}

	forbidden := map[string]struct{}{
		"BuildUserActionBar":        {},
		"BuildChatActionBar":        {},
		"BuildMessageActionBar":     {},
		"BuildLoadingButton":        {},
		"BuildRetryRow":             {},
		"BuildConfirmationCard":     {},
		"BuildPreviewActionCard":    {},
		"BuildToggleSwitch":         {},
		"BuildStateToggle":          {},
		"BuildStepper":              {},
		"BuildMultiStepStepper":     {},
		"BuildSelector":             {},
		"BuildMultiSelector":        {},
		"BuildSegmentedSlider":      {},
		"BuildDurationPicker":       {},
		"BuildPaginationRow":        {},
		"BuildNavRow":               {},
		"NewNavigator":              {},
		"DeserializeNavigator":      {},
		"BackButton":                {},
		"HomeButton":                {},
		"NewWizard":                 {},
		"NewRoleCallbackButton":     {},
		"NewRoleURLButton":          {},
		"NewRoleSwitchInlineButton": {},
		"NewPaginationRow":          {},
		"NewConfirmCancelRow":       {},
		"NewCloseRow":               {},
		"NewBackRow":                {},
		"NewPaginationMarkup":       {},
		"NewConfirmCancelMarkup":    {},
		"NewCloseMarkup":            {},
		"NewBackMarkup":             {},
		"NewHelpSwitchRow":          {},
		"NewCommonResultRow":        {},
		"NewStandardActionRow":      {},
	}

	fset := token.NewFileSet()
	err := filepath.Walk(uiRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if _, blocked := forbidden[fn.Name.Name]; blocked {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("reclaimed legacy UI helper %s returned in %s", fn.Name.Name, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP2DPureAndProductionUIHelpersRemain(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join("internal", "ui", "card.go"): {
			"func NewCard(",
			"func (c *Card) Render() string",
		},
		filepath.Join("internal", "ui", "menu.go"): {
			"func PaginateSlice[",
		},
		filepath.Join("internal", "ui", "screen.go"): {
			"func NewScreen(",
			"func (s *Screen) Render()",
		},
		filepath.Join("internal", "ui", "toast.go"): {
			"func PresentUserError(",
		},
		filepath.Join("internal", "ui", "render", "telegram.go"): {
			"presentationtelegram.EncodeMarkup(rows)",
		},
	}

	for rel, required := range checks {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, marker := range required {
			if !strings.Contains(source, marker) {
				t.Fatalf("%s lost production/pure UI helper marker %q", rel, marker)
			}
		}
	}
}
