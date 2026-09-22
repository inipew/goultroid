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
		"handleAssistantGroupService(ctx, service, e, deps)",
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
	end := strings.Index(source[start+1:], "\nfunc RegisterUpdateHandlers(")
	if start < 0 || end < 0 {
		t.Fatal("P7-H group-service ingress helper is missing")
	}
	block := source[start : start+1+end]
	interest := strings.Index(block, "deps.GroupEvents.Interested(chat.id, kind)")
	peer := strings.Index(block, "resolveAssistantGroupServicePeer(")
	dedupe := strings.Index(block, "make(map[int64]struct{}")
	users := strings.Index(block, "make([]core.GroupServiceUser")
	allocation := strings.Index(block, "&core.GroupServiceEvent{")
	publish := strings.Index(block, "deps.GroupEvents.Publish(")
	if interest < 0 || peer < 0 || dedupe < 0 || users < 0 || allocation < 0 || publish < 0 {
		t.Fatalf("P7-H ingress markers missing: interest=%d peer=%d dedupe=%d users=%d allocation=%d publish=%d",
			interest, peer, dedupe, users, allocation, publish)
	}
	for name, position := range map[string]int{
		"peer resolution": peer,
		"dedupe":          dedupe,
		"users":           users,
		"event":           allocation,
		"publish":         publish,
	} {
		if interest > position {
			t.Fatalf("P7-H interest gate occurs after %s work: interest=%d position=%d",
				name, interest, position)
		}
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

func TestP7HServiceMessagesBypassGlobalEntityCacheWhenInactive(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "updates.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	start := strings.Index(source, "handleNewMessage := func(")
	end := strings.Index(source[start+1:], "\n\tdispatcher.OnNewMessage(")
	if start < 0 || end < 0 {
		t.Fatal("shared Assistant message handler is missing")
	}
	block := source[start : start+1+end]
	serviceBranch := strings.Index(block, "if service, ok := message.(*tg.MessageService); ok")
	cache := strings.Index(block, "deps.CacheEntities(e)")
	if serviceBranch < 0 || cache < 0 || serviceBranch > cache {
		t.Fatalf("service-message branch must precede global entity cache: service=%d cache=%d",
			serviceBranch, cache)
	}
}

func TestP7HDoesNotOpenGroupFreeFormInteractionBeforeP7J(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "updates.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source,
		"if privateChat && deps.Resolver != nil && deps.Interaction != nil") {
		t.Fatal("ordinary group text can reach generic Assistant free-form interaction before P7-J")
	}
}

func TestP7HDeliveryReusesEventBusTaskEngine(t *testing.T) {
	root := repositoryRoot(t)

	appPath := filepath.Join(root, "internal", "app", "app.go")
	appRaw, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(appRaw), "coreDeps.eventBus.SetTasks(coreDeps.taskEngine)") {
		t.Fatal("P7-H EventBus is not bound to the shared TaskEngine")
	}

	servicePath := filepath.Join(root, "internal", "assistant", "groupevents", "service.go")
	serviceRaw, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	serviceSource := string(serviceRaw)
	if !strings.Contains(serviceSource, "s.bus.SubscribeWithOptions(") ||
		!strings.Contains(serviceSource, "s.bus.Publish(event)") {
		t.Fatal("P7-H delivery no longer uses the shared EventBus path")
	}
	for _, forbidden := range []string{
		"taskengine.New",
		"tasks.WorkSpec",
		"tasks.Submit",
	} {
		if strings.Contains(serviceSource, forbidden) {
			t.Errorf("P7-H feature owns execution primitive %q instead of EventBus/TaskEngine", forbidden)
		}
	}
}

func TestP7HCloseIsTerminalForInterestAndSubscription(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "groupevents", "service.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"closed atomic.Bool",
		"if !s.closed.CompareAndSwap(false, true)",
		"s.ready.Store(false)",
		"active := !s.closed.Load() && s.loaded && s.hasEnabledLocked()",
		"s.closed.Load() || !s.ready.Load()",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("P7-H terminal-close invariant missing %q", required)
		}
	}
}
