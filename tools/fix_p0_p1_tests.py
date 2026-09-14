from pathlib import Path


def replace(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise RuntimeError(f"anchor not found in {path}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1))


replace(
    "internal/workers/workers_test.go",
    'if got := len(mgr.AllStats()); got != 4 {\n\t\tt.Fatalf("expected 4 workload pools, got %d", got)\n\t}',
    'if got := len(mgr.AllStats()); got != 5 {\n\t\tt.Fatalf("expected 5 workload pools, got %d", got)\n\t}\n\tif _, exists := mgr.Get(PoolInteractive); !exists {\n\t\tt.Fatal("interactive pool must be provisioned")\n\t}',
)

replace(
    "internal/scheduler/engine_test.go",
    '''\t_, cancel := context.WithCancel(ctx)\n\tdefer cancel()\n\tengine.executeJob(ctx, claimed[0], cancel)\n\n\tsubmitter.mu.Lock()\n\tdefer submitter.mu.Unlock()\n\tif len(submitter.tasks) != 1 {\n\t\tt.Fatalf("expected 1 task submitted, got %d", len(submitter.tasks))\n\t}\n\tif submitter.tasks[0].Name != "job:sync-cache" {\n\t\tt.Errorf("expected task name 'job:sync-cache', got %q", submitter.tasks[0].Name)\n\t}\n\n\t// Run the submitted task\n\tif err := submitter.tasks[0].Run(ctx); err != nil {\n\t\tt.Fatalf("task run failed: %v", err)\n\t}\n\tif !jobRan {\n\t\tt.Fatalf("expected jobRan to be true")\n\t}\n''',
    '''\t_, cancel := context.WithCancel(ctx)\n\tdefer cancel()\n\texecuteDone := make(chan struct{})\n\tgo func() {\n\t\tengine.executeJob(ctx, claimed[0], cancel)\n\t\tclose(executeDone)\n\t}()\n\n\tvar submitted tasks.Task\n\tdeadline := time.Now().Add(time.Second)\n\tfor time.Now().Before(deadline) {\n\t\tsubmitter.mu.Lock()\n\t\tif len(submitter.tasks) == 1 {\n\t\t\tsubmitted = submitter.tasks[0]\n\t\t\tsubmitter.mu.Unlock()\n\t\t\tbreak\n\t\t}\n\t\tsubmitter.mu.Unlock()\n\t\ttime.Sleep(time.Millisecond)\n\t}\n\tif submitted.Run == nil {\n\t\tt.Fatal("expected one task to be submitted")\n\t}\n\tif submitted.Name != "job:sync-cache" {\n\t\tt.Errorf("expected task name 'job:sync-cache', got %q", submitted.Name)\n\t}\n\n\t// TriggerAndWait now waits for the concrete task result, so the fixture\n\t// executes the submitted task while executeJob is waiting for completion.\n\tif err := submitted.Run(ctx); err != nil {\n\t\tt.Fatalf("task run failed: %v", err)\n\t}\n\tselect {\n\tcase <-executeDone:\n\tcase <-time.After(time.Second):\n\t\tt.Fatal("executeJob did not observe managed job completion")\n\t}\n\tif !jobRan {\n\t\tt.Fatalf("expected jobRan to be true")\n\t}\n''',
)
