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
		"defaultDelayedActionRetainedBytes",
		"pendingBytes",
		"retainedBytes int64",
		"s.requests = nil",
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



	clientPath := filepath.Join(root, "internal", "telegram", "client.go")
	clientData, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"DefaultResolverCacheConfig", "DefaultExecutorPolicy"} {
		if strings.Contains(string(clientData), forbidden) {
			t.Errorf("%s must use immutable runtime defaults, found %q", clientPath, forbidden)
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

func TestTelegramEventConstructionUsesCanonicalNormalizerBoundary(t *testing.T) {
	root := repositoryRoot(t)

	dispatchPath := filepath.Join(root, "internal", "telegram", "dispatcher_dispatch.go")
	dispatchData, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dispatchData), "&core.MessageCreatedEvent{") {
		t.Errorf("%s must use canonicalMessageCreatedEvent instead of constructing MessageCreatedEvent directly", dispatchPath)
	}
	if !strings.Contains(string(dispatchData), "canonicalMessageCreatedEvent(") {
		t.Errorf("%s must publish messages through canonicalMessageCreatedEvent", dispatchPath)
	}

	callbackPath := filepath.Join(root, "internal", "telegram", "dispatcher_callback.go")
	callbackData, err := os.ReadFile(callbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(callbackData), "&core.CallbackQueryEvent{") {
		t.Errorf("%s must not construct CallbackQueryEvent directly", callbackPath)
	}
	for _, required := range []string{"canonicalCallbackQueryEvent(", "canonicalInlineCallbackQueryEvent("} {
		if !strings.Contains(string(callbackData), required) {
			t.Errorf("%s is missing canonical callback constructor %q", callbackPath, required)
		}
	}
}

func TestPerformanceResilienceHotPathsStayBounded(t *testing.T) {
	root := repositoryRoot(t)

	limiterPath := filepath.Join(root, "internal", "telegram", "rpc_limiter.go")
	limiterData, err := os.ReadFile(limiterPath)
	if err != nil {
		t.Fatal(err)
	}
	limiter := string(limiterData)
	if strings.Contains(limiter, "for key, bucket := range l.buckets") ||
		strings.Contains(limiter, "for key, until := range l.penalties") {
		t.Errorf("%s must not sweep all limiter state on the RPC hot path", limiterPath)
	}
	for _, required := range []string{"bucketLRU", "penaltyHeap", "overflowPenaltyUntil"} {
		if !strings.Contains(limiter, required) {
			t.Errorf("%s is missing bounded limiter structure %q", limiterPath, required)
		}
	}

	jobsPath := filepath.Join(root, "internal", "jobs", "manager.go")
	jobsData, err := os.ReadFile(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jobsData), "time.AfterFunc(") {
		t.Errorf("%s must not create one timer per deferred occurrence", jobsPath)
	}
	if !strings.Contains(string(jobsData), "EarliestDeferredOccurrenceDue(") {
		t.Errorf("%s must coordinate durable retry deadlines through one timer", jobsPath)
	}

	broadcastPath := filepath.Join(root, "internal", "services", "broadcast", "service.go")
	broadcastData, err := os.ReadFile(broadcastPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"time.After(", "time.Sleep("} {
		if strings.Contains(string(broadcastData), forbidden) {
			t.Errorf("%s must not hold a broadcast worker with %q", broadcastPath, forbidden)
		}
	}
	if !strings.Contains(string(broadcastData), "client.Submit(") {
		t.Errorf("%s must delegate physical target sends to TaskEngine", broadcastPath)
	}

	wiringPath := filepath.Join(root, "internal", "app", "wiring_services.go")
	wiringData, err := os.ReadFile(wiringPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wiringData), "broadcastService.SetTasks(core.taskEngine)") {
		t.Errorf("%s must wire broadcast execution to the shared TaskEngine", wiringPath)
	}
}


func TestIdleAndResourceRegressionGuards(t *testing.T) {
	root := repositoryRoot(t)

	checks := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path:      filepath.Join(root, "internal", "services", "inline", "cache.go"),
			required:  []string{"nextExpiry(", "pruneLoop("},
			forbidden: []string{"time.NewTicker("},
		},
		{
			path:      filepath.Join(root, "internal", "idempotency", "manager.go"),
			required:  []string{"EarliestExpiry(", "cleanupWake"},
			forbidden: []string{"time.NewTicker("},
		},
		{
			path:      filepath.Join(root, "internal", "scheduler", "engine.go"),
			required:  []string{"SetScheduleWake(e.notifyWake)", "case <-e.wakeChan:"},
			forbidden: []string{"idleHeartbeat"},
		},
		{
			path:     filepath.Join(root, "internal", "database", "db.go"),
			required: []string{"sqlitePoolLimits(", "min(max(4, cpuCount), 8)"},
		},
		{
			path:     filepath.Join(root, "internal", "resource", "manager.go"),
			required: []string{"DefaultMaxTrackedResources", "resource tracking capacity reached"},
		},
		{
			path:      filepath.Join(root, "internal", "services", "userlog", "service.go"),
			forbidden: []string{"time.After("},
		},
	}

	for _, check := range checks {
		data, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatalf("read %s: %v", check.path, err)
		}
		text := string(data)
		for _, required := range check.required {
			if !strings.Contains(text, required) {
				t.Errorf("%s is missing regression invariant %q", check.path, required)
			}
		}
		for _, forbidden := range check.forbidden {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s reintroduced idle/resource regression %q", check.path, forbidden)
			}
		}
	}
}

func TestJobsRetryDelaysRemainCoordinatorOwned(t *testing.T) {
	root := repositoryRoot(t)
	managerPath := filepath.Join(root, "internal", "jobs", "manager.go")
	data, err := os.ReadFile(managerPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"if delay > 0 {",
		"m.store.DeferOccurrence(",
		"m.signalRecovery()",
		"Every positive backoff is durable timing state",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("%s is missing durable retry invariant %q", managerPath, required)
		}
	}
	if strings.Contains(text, "timer := time.NewTimer(delay)") {
		t.Errorf("%s reintroduced a timer held by a retry worker", managerPath)
	}
}

func TestDownloaderUsesEphemeralTaskContinuations(t *testing.T) {
	root := repositoryRoot(t)

	downloaderPath := filepath.Join(root, "plugins", "downloader", "downloader.go")
	data, err := os.ReadFile(downloaderPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"p.tasks.Submit(",
		`Pool:             tasks.PoolID("download")`,
		"urlResources(",
		"markHeldResources(",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("%s is missing ephemeral continuation invariant %q", downloaderPath, required)
		}
	}
	for _, forbidden := range []string{
		"jobs.Register(",
		"JobDefinition",
		"jobUI",
		"dl-url-",
		"dl-media-",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s reintroduced per-request durable job state %q", downloaderPath, forbidden)
		}
	}

	modulePath := filepath.Join(root, "plugins", "downloader", "module.go")
	moduleData, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(moduleData), "plugin.CapTasks") {
		t.Errorf("%s must request TaskEngine capability", modulePath)
	}
	if strings.Contains(string(moduleData), "plugin.CapJobs") {
		t.Errorf("%s must not request durable jobs capability for ephemeral downloads", modulePath)
	}
}
