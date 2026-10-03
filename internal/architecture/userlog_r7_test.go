package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR7UserLogHasNoPrivateQueueOrWorker(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "userlog", "userlog.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{
		"queue chan func()",
		"scope.Go(",
		"time.NewTimer(",
		"workerRunning",
		"workerGeneration",
		"workerIdleTimeout",
		"enqueuedCount",
		"droppedCount",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("UserLog retained private scheduling/backpressure primitive %q", forbidden)
		}
	}
	for _, required := range []string{
		"plugin.PluginContextInitializer",
		"pctx.TaskClient()",
		"core.MessageHookEvent",
		"core.MessageHookFailOpen",
		"core.MessageHookOrderingPlugin",
		"userLogExecutionTimeout",
		"tasks.PoolID(\"general\")",
		"tasks.PriorityNormal",
		"userLogOrderingKey",
		"SubscribeWithOptions",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("UserLog R7 execution model missing %q", required)
		}
	}
}

func TestR7UserLogUsesSharedTaskAndRPCAuthorities(t *testing.T) {
	root := repositoryRoot(t)
	moduleRaw, err := os.ReadFile(filepath.Join(root, "plugins", "userlog", "module.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(moduleRaw), "plugin.CapTasks") {
		t.Fatal("UserLog manifest does not declare shared TaskEngine capability")
	}

	serviceRaw, err := os.ReadFile(filepath.Join(root, "internal", "services", "userlog", "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	service := string(serviceRaw)
	for _, forbidden := range []string{"NewRPCExecutor(", "NewHierarchicalRPCLimiter(", "RetryPolicy{", "time.Sleep("} {
		if strings.Contains(service, forbidden) {
			t.Fatalf("UserLog service created a private RPC/retry path via %q", forbidden)
		}
	}
	if !strings.Contains(service, "svc.SendMessage(ctx, dest.InputPeer(), text)") {
		t.Fatal("UserLog service no longer delegates to canonical Telegram message service")
	}

	appRaw, err := os.ReadFile(filepath.Join(root, "internal", "app", "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(appRaw), "coreDeps.eventBus.SetTasks(coreDeps.taskEngine)") {
		t.Fatal("production EventBus is not wired to the shared TaskEngine")
	}
}
