package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guard the new foundations while legacy jobs/scheduler adapters are migrated.
func TestExecutionRedesignFoundations(t *testing.T) {
	imports := collectImports(t, repositoryRoot(t))
	forbidden := map[string][]string{
		"tasks":      {"internal/telegram", "internal/jobs", "internal/workers", "internal/taskengine", "internal/app", "plugins"},
		"admission":  {"internal/telegram", "internal/jobs", "internal/workers", "internal/taskengine", "internal/app", "plugins"},
		"workers":    {"internal/telegram", "internal/jobs", "internal/scheduler", "internal/admission", "internal/taskengine", "internal/app", "plugins"},
		"taskengine": {"internal/telegram", "internal/jobs", "internal/app", "plugins"},
		"scheduler":  {"internal/telegram", "internal/app", "plugins"},
	}
	for pkg, paths := range forbidden {
		for dep := range imports[modulePath+"/internal/"+pkg] {
			if (pkg != "scheduler" && dep == "database/sql") || strings.HasPrefix(dep, "github.com/gotd/") || strings.HasPrefix(dep, "modernc.org/sqlite") {
				t.Errorf("%s imports infrastructure %s", pkg, dep)
			}
			for _, path := range paths {
				prefix := modulePath + "/" + path
				if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
					t.Errorf("%s imports forbidden dependency %s", pkg, dep)
				}
			}
		}
	}
}

func TestLegacyExecutionPackagesAreRemoved(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"internal/tasks/manager.go",
		"internal/tasks/task.go",
		"internal/jobs/job.go",
		"internal/jobs/repository.go",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			t.Errorf("legacy execution implementation remains: %s", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "internal/workers"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read legacy workers directory: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Errorf("legacy execution implementation remains: internal/workers/%s", entry.Name())
		}
	}
}

func TestArchitecture_ResourceCommandsGateInvariants(t *testing.T) {
	root := repositoryRoot(t)

	// 1. Check telegram dispatcher uses submitInteractiveCommand for all interactive commands
	dispDispatchPath := filepath.Join(root, "internal", "telegram", "dispatcher_dispatch.go")
	dispContent, err := os.ReadFile(dispDispatchPath)
	if err != nil {
		t.Fatalf("read %s: %v", dispDispatchPath, err)
	}
	if !strings.Contains(string(dispContent), "d.submitInteractiveCommand") {
		t.Errorf("%s must invoke submitInteractiveCommand to route commands via TaskEngine", dispDispatchPath)
	}

	// 2. Check assistant router guards resources with ErrTasksNotConfigured
	asstRouterPath := filepath.Join(root, "internal", "assistant", "command", "router.go")
	asstContent, err := os.ReadFile(asstRouterPath)
	if err != nil {
		t.Fatalf("read %s: %v", asstRouterPath, err)
	}
	if !strings.Contains(string(asstContent), "if len(cmd.Resources) > 0") || !strings.Contains(string(asstContent), "ErrTasksNotConfigured") {
		t.Errorf("%s must guard resource-bearing commands with ErrTasksNotConfigured when task engine is unconfigured", asstRouterPath)
	}

	// 3. Check scheduled_action.go guards command.Resources with tasks client requirement
	schedActionPath := filepath.Join(root, "internal", "app", "scheduled_action.go")
	schedContent, err := os.ReadFile(schedActionPath)
	if err != nil {
		t.Fatalf("read %s: %v", schedActionPath, err)
	}
	if !strings.Contains(string(schedContent), "if len(command.Resources) == 0") || !strings.Contains(string(schedContent), "h.tasks == nil") {
		t.Errorf("%s must enforce TaskEngine client for resource-bearing scheduled commands", schedActionPath)
	}
}

func TestDelayedTelegramActionsAreRuntimeOwned(t *testing.T) {
	root := repositoryRoot(t)

	messagesPath := filepath.Join(root, "internal", "core", "context_messages.go")
	messagesData, err := os.ReadFile(messagesPath)
	if err != nil {
		t.Fatalf("read %s: %v", messagesPath, err)
	}
	for _, forbidden := range []string{"time.AfterFunc(", "context.Background()"} {
		if strings.Contains(string(messagesData), forbidden) {
			t.Errorf("%s must not own detached delayed Telegram work via %q", messagesPath, forbidden)
		}
	}

	schedulerPath := filepath.Join(root, "internal", "app", "delayed_action.go")
	schedulerData, err := os.ReadFile(schedulerPath)
	if err != nil {
		t.Fatalf("read %s: %v", schedulerPath, err)
	}
	for _, required := range []string{
		`Dependencies() []string { return []string{"taskengine"} }`,
		"s.tasks.Submit",
		"tasks.PriorityMaintenance",
		"case <-ctx.Done():",
	} {
		if !strings.Contains(string(schedulerData), required) {
			t.Errorf("%s is missing runtime-owned delayed action invariant %q", schedulerPath, required)
		}
	}
}

func TestTelegramCallbackAndOriginHardeningGuards(t *testing.T) {
	root := repositoryRoot(t)

	callbackTypes := filepath.Join(root, "internal", "services", "callback", "types.go")
	data, err := os.ReadFile(callbackTypes)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"RequiresState bool", "RequiresCallbackState(", "truncateUTF8Bytes("} {
		if !strings.Contains(string(data), required) {
			t.Errorf("%s is missing hardening invariant %q", callbackTypes, required)
		}
	}
	for _, forbidden := range []string{"text[:200]", "text[:4096]"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("%s still contains UTF-8-unsafe truncation %q", callbackTypes, forbidden)
		}
	}

	servicePath := filepath.Join(root, "internal", "telegram", "service.go")
	serviceData, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serviceData), "IsBotSentForPeer(") {
		t.Errorf("%s must expose peer-scoped bot-origin tracking", servicePath)
	}

	dispatchPath := filepath.Join(root, "internal", "telegram", "dispatcher_dispatch.go")
	dispatchData, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dispatchData), "IsBotSentForPeer(") {
		t.Errorf("%s must classify automation origin with peer identity", dispatchPath)
	}
}

func TestIdleCachesAvoidPeriodicWakeupsAndRemainBounded(t *testing.T) {
	root := repositoryRoot(t)

	callbackLifecycle := filepath.Join(root, "internal", "services", "callback", "lifecycle.go")
	lifecycleData, err := os.ReadFile(callbackLifecycle)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"time.NewTicker(", "go func()"} {
		if strings.Contains(string(lifecycleData), forbidden) {
			t.Errorf("%s must remain passive during idle; found %q", callbackLifecycle, forbidden)
		}
	}

	peerStorage := filepath.Join(root, "internal", "telegram", "peer_storage.go")
	peerData, err := os.ReadFile(peerStorage)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"maxPeerStorageCacheEntries", "cachePeerLocked(", "cacheEntityLocked("} {
		if !strings.Contains(string(peerData), required) {
			t.Errorf("%s is missing bounded cache invariant %q", peerStorage, required)
		}
	}
}
