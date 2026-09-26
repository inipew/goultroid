package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestP2BPresentationStaysLocalizationAgnostic(t *testing.T) {
	root := repositoryRoot(t)
	presentationRoot := filepath.Join(root, "internal", "presentation")
	err := filepath.Walk(presentationRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "github.com/inipew/goultroid/internal/services/localization" ||
				importPath == "github.com/inipew/goultroid/internal/settings" {
				t.Errorf("presentation must stay localization/settings agnostic: %s imports %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP2BUserbotLocaleUsesCanonicalSettingsAuthority(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "app", "wiring_core.go"): {
			"localization.New(localization.DefaultLocale)",
		},
		filepath.Join(root, "internal", "app", "wiring_services.go"): {
			"SetLocalizerResolver",
			"localization.ResolveLocale(ctx, settingsService, userID, chatID)",
			"localization.Bind(core.localizer, locale)",
		},
		filepath.Join(root, "internal", "telegram", "dispatcher_dispatch.go"): {
			"getLocalizerResolver()",
			"resolve(execCtx, sender.ID, chat.ID)",
		},
		filepath.Join(root, "internal", "assistant", "shell", "locale.go"): {
			"LocaleSettingNamespace = localization.LocaleSettingNamespace",
			"return localization.ResolveLocale(ctx, svc, userID, chatID)",
		},
	}
	for path, markers := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, marker := range markers {
			if !strings.Contains(source, marker) {
				t.Errorf("P2-B canonical locale wiring missing %q from %s", marker, path)
			}
		}
	}

	coreWiring, err := os.ReadFile(filepath.Join(root, "internal", "app", "wiring_core.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(coreWiring), "localization.New(\"id\")") {
		t.Fatal("userbot bootstrap regressed to hard-coded Indonesian locale")
	}
}

func TestP2BMetricPathsDoNotDependOnLocalization(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "core", "metrics.go"),
		filepath.Join("internal", "database", "metrics.go"),
		filepath.Join("internal", "telegram", "rpc_metrics.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "services/localization") {
			t.Errorf("metric label path must not depend on localization: %s", rel)
		}
	}
}
