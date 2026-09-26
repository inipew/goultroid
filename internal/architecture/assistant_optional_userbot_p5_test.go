package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type p5UserbotPackage struct {
	commands        []string
	imports         map[string]struct{}
	source          string
	hasCommandsFunc bool
}

func TestP5SurfaceUserbotCommandsHaveNoUnfencedAssistantDependency(t *testing.T) {
	root := repositoryRoot(t)
	packages := collectP5UserbotPackages(t, filepath.Join(root, "plugins"))
	if len(packages) == 0 {
		t.Fatal("P5 audit discovered no userbot command packages")
	}

	allowedEnhancements := map[string]struct{}{
		"calculator": {},
		"downloader": {},
		"help":       {},
		"myxl":       {},
	}
	seenEnhancements := make(map[string]struct{}, len(allowedEnhancements))
	userbotCommands := 0

	for name, pkg := range packages {
		userbotCommands += len(pkg.commands)
		if strings.Contains(pkg.source, `callback.EncodeCallbackData("assistant"`) ||
			strings.Contains(pkg.source, `"assistant", "start"`) {
			t.Fatalf("userbot package %q still emits Assistant-owned callback navigation", name)
		}
		hasAssistant := p5HasImportPrefix(pkg.imports, "github.com/inipew/goultroid/internal/assistant")
		hasSelfInline := p5HasImportPrefix(pkg.imports, "github.com/inipew/goultroid/internal/presentation/selfinline")
		if !hasAssistant && !hasSelfInline {
			continue
		}
		if _, ok := allowedEnhancements[name]; !ok {
			t.Fatalf("userbot package %q commands=%v gained Assistant/self-inline dependency without P5 fallback contract", name, pkg.commands)
		}
		seenEnhancements[name] = struct{}{}
	}

	if userbotCommands == 0 {
		t.Fatal("P5 audit discovered no SurfaceUserbot commands")
	}
	for name := range allowedEnhancements {
		if _, ok := seenEnhancements[name]; !ok {
			t.Fatalf("expected progressive-enhancement package %q was not discovered by userbot audit", name)
		}
	}
	if pkg, ok := packages["downloader"]; !ok || !p5Contains(pkg.commands, "download") {
		t.Fatal("P5 audit must treat commands with unspecified Surfaces as SurfaceUserbot by compatibility default")
	}
}

func TestP5KnownAssistantEnhancementsRetainNativeUserbotFallbacks(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "help", "help.go"): {
			"selfinline.FallbackSafe(err)",
			"return p.handleNativeHelp(ctx, prefix, source)",
		},
		filepath.Join(root, "plugins", "calculator", "calculator.go"): {
			"selfinline.FallbackSafe(err)",
			"return handleNativeCommand(ctx, expression)",
			"evaluateExpression(expression)",
		},
		filepath.Join(root, "plugins", "downloader", "downloader.go"): {
			"selfinline.FallbackSafe(err)",
			"return p.startNativeURLDownload(ctx, normalized, provider.Name())",
		},
		filepath.Join(root, "plugins", "downloader", "url_native.go"): {
			"Mode:      download.MediaModeDefault",
			"Format:    download.MediaFormatDefault",
			"MaxHeight: 0",
			"p.submitURLPipeline(",
		},
		filepath.Join(root, "plugins", "myxl", "myxl.go"): {
			"if ctx.IsAssistant() {",
			"return p.openAssistant(ctx)",
			"📱 <b>MyXL Plugin Menu</b>",
			"subCmd := strings.ToLower(ctx.Args[0])",
		},
		filepath.Join(root, "plugins", "settings", "settings.go"): {
			`callback.EncodeCallbackData("settings", callback.ActionNav`,
			`callback.EncodeCallbackData("settings", callback.ActionClose`,
		},
		filepath.Join(root, "plugins", "wikipedia", "wikipedia.go"): {
			"Handler:     p.handle",
			"func (p *Plugin) handle(ctx *core.Context) error",
			"execution.SurfaceUserbot | execution.SurfaceAssistant",
			"Kind:        feature.InteractionInline",
			"Surfaces:    execution.SurfaceInline",
		},
	}

	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Fatalf("P5 userbot fallback invariant missing from %s: %q", path, invariant)
			}
		}
	}

	settingsRaw, err := os.ReadFile(filepath.Join(root, "plugins", "settings", "settings.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settingsRaw), `callback.EncodeCallbackData("assistant"`) {
		t.Fatal("native settings regressed to an Assistant-owned callback dependency")
	}
}

func collectP5UserbotPackages(t *testing.T, pluginsRoot string) map[string]p5UserbotPackage {
	t.Helper()
	fset := token.NewFileSet()
	packages := make(map[string]p5UserbotPackage)

	entries, err := os.ReadDir(pluginsRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := filepath.Join(pluginsRoot, name)
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		pkg := p5UserbotPackage{imports: make(map[string]struct{})}
		aliases := make(map[string]ast.Expr)
		var source strings.Builder
		var parsedFiles []*ast.File
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, file.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source.Write(raw)
			source.WriteByte('\n')
			parsed, err := parser.ParseFile(fset, path, raw, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			parsedFiles = append(parsedFiles, parsed)
			for _, imp := range parsed.Imports {
				value, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("parse import %s in %s: %v", imp.Path.Value, path, err)
				}
				pkg.imports[value] = struct{}{}
			}
			p5CollectSurfaceAliases(parsed, aliases)
		}

		pkg.source = source.String()
		for _, parsed := range parsedFiles {
			for _, decl := range parsed.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name == nil || fn.Name.Name != "Commands" || fn.Body == nil {
					continue
				}
				pkg.hasCommandsFunc = true
				pkg.commands = append(pkg.commands, p5UserbotCommandsFromFunction(fn, aliases)...)
			}
		}
		if pkg.hasCommandsFunc && len(pkg.commands) == 0 {
			t.Fatalf("P5 audit could not resolve any userbot command from plugin %q Commands()", name)
		}
		if len(pkg.commands) > 0 {
			packages[name] = pkg
		}
	}
	return packages
}

func p5CollectSurfaceAliases(file *ast.File, aliases map[string]ast.Expr) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, ident := range valueSpec.Names {
				if index < len(valueSpec.Values) {
					aliases[ident.Name] = valueSpec.Values[index]
				} else if len(valueSpec.Values) == 1 {
					aliases[ident.Name] = valueSpec.Values[0]
				}
			}
		}
	}
}

func p5UserbotCommandsFromFunction(fn *ast.FuncDecl, aliases map[string]ast.Expr) []string {
	var commands []string
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		ret, ok := node.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			list, ok := result.(*ast.CompositeLit)
			if !ok || !p5IsCommandSlice(list.Type) {
				continue
			}
			for _, element := range list.Elts {
				command, ok := element.(*ast.CompositeLit)
				if !ok {
					continue
				}
				name, userbot := p5CommandMetadata(command, aliases)
				if userbot && name != "" {
					commands = append(commands, name)
				}
			}
		}
		return true
	})
	return commands
}

func p5IsCommandSlice(expr ast.Expr) bool {
	array, ok := expr.(*ast.ArrayType)
	if !ok {
		return false
	}
	sel, ok := array.Elt.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Command" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "core"
}

func p5CommandMetadata(command *ast.CompositeLit, aliases map[string]ast.Expr) (string, bool) {
	name := ""
	hasSurfaces := false
	userbot := false
	for _, element := range command.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Name":
			if lit, ok := field.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				name, _ = strconv.Unquote(lit.Value)
			}
		case "Surfaces":
			hasSurfaces = true
			userbot = p5SurfaceExprSupportsUserbot(field.Value, aliases, make(map[string]bool))
		}
	}
	if !hasSurfaces {
		userbot = true
	}
	return name, userbot
}

func p5SurfaceExprSupportsUserbot(expr ast.Expr, aliases map[string]ast.Expr, seen map[string]bool) bool {
	switch value := expr.(type) {
	case *ast.SelectorExpr:
		return value.Sel != nil && value.Sel.Name == "SurfaceUserbot"
	case *ast.BinaryExpr:
		return p5SurfaceExprSupportsUserbot(value.X, aliases, seen) || p5SurfaceExprSupportsUserbot(value.Y, aliases, seen)
	case *ast.ParenExpr:
		return p5SurfaceExprSupportsUserbot(value.X, aliases, seen)
	case *ast.Ident:
		if value.Name == "nil" {
			return true
		}
		if seen[value.Name] {
			return true
		}
		aliased, ok := aliases[value.Name]
		if !ok {
			return true
		}
		seen[value.Name] = true
		return p5SurfaceExprSupportsUserbot(aliased, aliases, seen)
	case *ast.BasicLit:
		return value.Kind == token.INT && value.Value == "0"
	default:
		// Unknown surface expressions are treated as userbot-capable so this
		// architecture gate fails closed rather than silently skipping commands.
		return true
	}
}

func p5HasImportPrefix(imports map[string]struct{}, prefix string) bool {
	for path := range imports {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func p5Contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
