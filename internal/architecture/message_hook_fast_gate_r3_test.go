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

func TestR3MessageHookFastGateFactsStayTransportNeutral(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "core", "message_hook.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var facts *ast.StructType
	var gate *ast.FuncType
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			switch typeSpec.Name.Name {
			case "MessageHookFacts":
				facts, _ = typeSpec.Type.(*ast.StructType)
			case "MessageHookFastGate":
				gate, _ = typeSpec.Type.(*ast.FuncType)
			}
		}
	}
	if facts == nil {
		t.Fatal("core.MessageHookFacts missing")
	}
	expected := map[string]string{
		"ChatID":      "int64",
		"Outgoing":    "bool",
		"IsCommand":   "bool",
		"CommandName": "string",
		"Origin":      "ExecutionSource",
	}
	seen := map[string]string{}
	for _, field := range facts.Fields.List {
		ident, ok := field.Type.(*ast.Ident)
		if !ok {
			t.Fatalf("MessageHookFacts field uses non-scalar/non-core-ident type: %#v", field.Type)
		}
		for _, name := range field.Names {
			seen[name.Name] = ident.Name
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("MessageHookFacts fields=%v, want %v", seen, expected)
	}
	for name, typ := range expected {
		if seen[name] != typ {
			t.Errorf("MessageHookFacts.%s type=%q, want %q", name, seen[name], typ)
		}
	}

	if gate == nil || gate.Params == nil || len(gate.Params.List) != 1 ||
		gate.Results == nil || len(gate.Results.List) != 1 {
		t.Fatalf("MessageHookFastGate must be func(MessageHookFacts) bool: %#v", gate)
	}
	param, ok := gate.Params.List[0].Type.(*ast.Ident)
	if !ok || param.Name != "MessageHookFacts" {
		t.Fatalf("MessageHookFastGate parameter=%#v, want MessageHookFacts", gate.Params.List[0].Type)
	}
	result, ok := gate.Results.List[0].Type.(*ast.Ident)
	if !ok || result.Name != "bool" {
		t.Fatalf("MessageHookFastGate result=%#v, want bool", gate.Results.List[0].Type)
	}
}

func TestR3AFKFastGatesStayPureAndBounded(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "afk", "afk.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	for _, name := range []string{"outgoingFastGate", "incomingFastGate"} {
		start := strings.Index(source, "func (p *Plugin) "+name)
		if start < 0 {
			t.Fatalf("%s missing", name)
		}
		next := strings.Index(source[start+1:], "\nfunc ")
		body := source[start:]
		if next >= 0 {
			body = source[start : start+1+next]
		}
		for _, banned := range []string{
			"p.db",
			"p.svcFunc",
			"GetMessage(",
			"SendMessage(",
			"resolveEnvelopePeer(",
			"transitionMu",
			"stateMu",
			"cooldownMu",
			"context.",
		} {
			if strings.Contains(body, banned) {
				t.Errorf("%s contains non-fast-path dependency %q", name, banned)
			}
		}
	}
}

func TestR3DispatcherRunsFastGateBeforeTaskAdmission(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "telegram", "dispatcher_dispatch.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	check := func(name, endMarker string) {
		t.Helper()
		start := strings.Index(source, "func (d *Dispatcher) "+name)
		if start < 0 {
			t.Fatalf("%s missing", name)
		}
		end := strings.Index(source[start:], endMarker)
		if end < 0 {
			t.Fatalf("%s terminator %q missing", name, endMarker)
		}
		body := source[start : start+end]
		gate := strings.Index(body, "messageHookFastInterested")
		submit := strings.Index(body, "client.Submit")
		if gate < 0 || submit < 0 || gate > submit {
			t.Fatalf("%s must evaluate fast gate before TaskEngine admission", name)
		}
	}

	check("executeDecisionHandlersEnvelope", "func (d *Dispatcher) messageHookStateInterested")
	check("dispatchEventHandlersEnvelope", "func (d *Dispatcher) resolveDispatchChat")
}
