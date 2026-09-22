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

func TestP7GMutationServiceHasNoSecondExecutionEngineOrBackgroundLifecycle(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "group_mutation.go")
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
		if importPath == modulePath+"/internal/tasks" {
			t.Fatal("P7-G mutation service must not own or import TaskEngine")
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.GoStmt:
			t.Errorf("P7-G mutation service starts goroutine at %s", fset.Position(n.Pos()))
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
			case "NewTicker", "Tick", "AfterFunc":
				t.Errorf("P7-G mutation service installs timer %s at %s",
					sel.Sel.Name, fset.Position(n.Pos()))
			}
		}
		return true
	})
}

func TestP7GAssistantServicerMutationsDelegateOnlyToTypedPort(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "command", "servicer.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	mutations := map[string]bool{
		"PinMessage":                  false,
		"UnpinMessage":                false,
		"BanUser":                     false,
		"UnbanUser":                   false,
		"KickUser":                    false,
		"MuteUser":                    false,
		"UnmuteUser":                  false,
		"PurgeMessages":               false,
		"PromoteAdmin":                false,
		"DemoteAdmin":                 false,
		"EditChatDefaultBannedRights": false,
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			continue
		}
		if _, tracked := mutations[fn.Name.Name]; !tracked {
			continue
		}

		delegates := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "executeGroupMutation" {
				delegates = true
			}
			return true
		})
		if !delegates {
			t.Errorf("Assistant mutation %s bypasses typed P7-G mutation port", fn.Name.Name)
		}
		mutations[fn.Name.Name] = true
	}

	for name, found := range mutations {
		if !found {
			t.Errorf("P7-G mutation adapter method %s is missing", name)
		}
	}
}

func TestP7GManagedMutationRPCsUseExistingRPCExecutor(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "managed_api.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	for _, method := range []string{
		"ChannelsEditBanned",
		"ChannelsEditAdmin",
		"ChannelsDeleteMessages",
		"MessagesDeleteChatUser",
		"MessagesEditChatAdmin",
		"MessagesUpdatePinnedMessage",
		"MessagesEditChatDefaultBannedRights",
		"MessagesDeleteMessages",
	} {
		start := strings.Index(source, "func (a *managedAPI) "+method+"(")
		if start < 0 {
			t.Errorf("managed P7-G wrapper %s is missing", method)
			continue
		}
		end := strings.Index(source[start+1:], "
func (a *managedAPI) ")
		block := source[start:]
		if end >= 0 {
			block = source[start : start+1+end]
		}
		if !strings.Contains(block, "managedValue(") ||
			!strings.Contains(block, "assistentrpc.IdempotentMutation") {
			t.Errorf("P7-G wrapper %s does not use managed idempotent executor", method)
		}
	}
}

func TestP7GAdminPluginOwnsPolicyNotTelegramTransport(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "admin", "admin.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	for _, forbidden := range []string{
		"ChannelsEditBanned",
		"ChannelsEditAdmin",
		"MessagesDeleteChatUser",
		"MessagesEditChatAdmin",
		"MessagesUpdatePinnedMessage",
		"MessagesDeleteMessages",
		"ChannelsDeleteMessages",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("admin plugin owns raw Telegram mutation %s instead of core facade", forbidden)
		}
	}
}

func TestP7GMutationPortRequiresTaskAdmissionCapability(t *testing.T) {
	root := repositoryRoot(t)

	servicerPath := filepath.Join(root, "internal", "assistant", "command", "servicer.go")
	servicerRaw, err := os.ReadFile(servicerPath)
	if err != nil {
		t.Fatal(err)
	}
	servicerSource := string(servicerRaw)
	for _, required := range []string{
		"mutationAdmitted",
		"if !a.mutationAdmitted",
		"ErrGroupMutationNotAdmitted",
		"func admitGroupMutationExecution",
	} {
		if !strings.Contains(servicerSource, required) {
			t.Errorf("P7-G mutation admission invariant missing %q", required)
		}
	}

	routerPath := filepath.Join(root, "internal", "assistant", "command", "router.go")
	routerRaw, err := os.ReadFile(routerPath)
	if err != nil {
		t.Fatal(err)
	}
	routerSource := string(routerRaw)
	if !strings.Contains(routerSource, "admitGroupMutationExecution(&execCtx)") {
		t.Fatal("P7-G mutation port is not activated inside TaskEngine WorkSpec execution")
	}
	if strings.Contains(routerSource, "admitGroupMutationExecution(coreCtx)") {
		t.Fatal("P7-G mutation admission leaked onto the pre-TaskEngine command context")
	}
}
