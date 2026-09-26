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

func TestP2AHookRegistrarUsesSingleRegistrationContract(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "plugin", "manager.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var registrar *ast.InterfaceType
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "HookRegistrar" {
				continue
			}
			registrar, _ = typeSpec.Type.(*ast.InterfaceType)
		}
	}
	if registrar == nil {
		t.Fatal("HookRegistrar interface missing")
	}
	if len(registrar.Methods.List) != 1 || len(registrar.Methods.List[0].Names) != 1 ||
		registrar.Methods.List[0].Names[0].Name != "RegisterMessageHook" {
		t.Fatalf("HookRegistrar must expose exactly RegisterMessageHook, got %#v", registrar.Methods.List)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, retired := range []string{
		"type scopedHookRegistrar interface",
		"type routedHookRegistrar interface",
		"type scopedRoutedHookRegistrar interface",
		"type stateRoutedHookRegistrar interface",
		"type scopedStateRoutedHookRegistrar interface",
		"type canonicalRoutedHookRegistrar interface",
		"type scopedCanonicalRoutedHookRegistrar interface",
		"type canonicalStateRoutedHookRegistrar interface",
		"type scopedCanonicalStateRoutedHookRegistrar interface",
	} {
		if strings.Contains(source, retired) {
			t.Errorf("retired registrar capability interface remains: %s", retired)
		}
	}

	start := strings.Index(source, "func registerMessageHook(")
	if start < 0 {
		t.Fatal("registerMessageHook body not found")
	}
	end := strings.Index(source[start:], "// SchedulerTaskCleaner")
	if end < 0 {
		t.Fatal("registerMessageHook terminator not found")
	}
	body := source[start : start+end]
	if strings.Contains(body, "registrar.(") {
		t.Fatal("registerMessageHook still capability-probes the registrar")
	}
	if strings.Count(body, "registrar.RegisterMessageHook(registration)") != 2 {
		t.Fatal("raw and canonical registration must converge on the single registrar contract")
	}
}

func TestP2AMessageHookRegistrationContractIsShared(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "core", "message_hook.go"): {
			"type MessageHookRegistration struct {",
			"Scope            tasks.ScopeIdentity",
			"Priority         int",
			"Routing          MessageHookRouting",
			"StateGate        func(int64) bool",
			"Handler          CanonicalMessageHookHandler",
			"RawHandler       RawMessageHookHandler",
			"LegacyRouting    bool",
		},
		filepath.Join(root, "internal", "telegram", "dispatcher_handlers.go"): {
			"func (d *Dispatcher) RegisterMessageHook(registration core.MessageHookRegistration) (func(), error)",
			"legacyMessageHookRouting(registration.Priority, registration.Scope)",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, marker := range required {
			if !strings.Contains(source, marker) {
				t.Errorf("P2-A shared hook contract missing %q from %s", marker, path)
			}
		}
	}
}
