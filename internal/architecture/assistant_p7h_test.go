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

func TestP7HGroupEventServiceHasNoPermanentWorkerOrSecondTaskEngine(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "groupevents", "service.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if importPath == modulePath+"/internal/tasks" ||
			importPath == modulePath+"/internal/taskengine" {
			t.Fatalf("P7-H group event service owns execution engine import %q", importPath)
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.GoStmt:
			t.Errorf("P7-H group event service starts goroutine at %s", fset.Position(n.Pos()))
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "time" {
				return true
			}
			switch sel.Sel.Name {
			case "NewTicker", "Tick", "AfterFunc", "NewTimer":
				t.Errorf("P7-H service installs timer %s at %s",
					sel.Sel.Name, fset.Position(n.Pos()))
			}
		}
		return true
	})
}

func TestP7HAssistantIngressCoversBasicAndSupergroupUpdateClasses(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "updates.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	for _, required := range []string{
		"handleNewMessage := func(",
		"dispatcher.OnNewMessage(",
		"dispatcher.OnNewChannelMessage(",
		"handleAssistantGroupService(service, e, deps)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("P7-H Assistant ingress missing %q", required)
		}
	}
}

func TestP7HInterestGatePrecedesEventAllocationAndPublish(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "updates.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	start := strings.Index(source, "func handleAssistantGroupService(")
	end := strings.Index(source[start+1:], "
func RegisterUpdateHandlers(")
	if start < 0 || end < 0 {
		t.Fatal("P7-H group-service ingress helper is missing")
	}
	block := source[start : start+1+end]
	interest := strings.Index(block, "deps.GroupEvents.Interested(chatID, kind)")
	allocation := strings.Index(block, "&core.GroupServiceEvent{")
	publish := strings.Index(block, "deps.GroupEvents.Publish(")
	if interest < 0 || allocation < 0 || publish < 0 {
		t.Fatalf("P7-H ingress markers missing: interest=%d allocation=%d publish=%d",
			interest, allocation, publish)
	}
	if interest > allocation || interest > publish {
		t.Fatalf("P7-H interest gate occurs after event work: interest=%d allocation=%d publish=%d",
			interest, allocation, publish)
	}
}

func TestP7HSubscriptionIsDynamicNotPerChat(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "groupevents", "service.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	if strings.Count(source, "SubscribeWithOptions(") != 1 {
		t.Fatalf("P7-H must own exactly one shared EventBus subscription site, got %d",
			strings.Count(source, "SubscribeWithOptions("))
	}
	for _, required := range []string{
		"activeChats > 0",
		"s.sub == nil",
		"s.sub != nil",
		"sub.Close()",
		"welcomeInterest.Interested(chatID)",
		"goodbyeInterest.Interested(chatID)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("P7-H dynamic subscription/interest invariant missing %q", required)
		}
	}
}

func TestP7HControlStateDoesNotUseGlobalSettings(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "assistant", "groupevents", "service.go"),
		filepath.Join("internal", "assistant", "groupeventsadmin", "feature.go"),
	} {
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if strings.Contains(source, `internal/settings`) ||
			strings.Contains(source, "SettingsService") {
			t.Errorf("P7-H chat-local state bypassed P7-F through global settings in %s", rel)
		}
	}
}
