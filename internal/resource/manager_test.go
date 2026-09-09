package resource

import (
	"testing"
)

func TestResourceManager_RegisterAndRelease(t *testing.T) {
	mgr := NewManager()

	res := Resource{
		ID:    "sub-1",
		Owner: "plugin:afk",
		Type:  TypeSubscription,
	}

	if err := mgr.Register(res); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Duplicate registration must fail
	if err := mgr.Register(res); err == nil {
		t.Fatalf("expected duplicate registration error")
	}

	// Retrieve
	got, found := mgr.Get("sub-1")
	if !found || got.State != StateActive {
		t.Fatalf("expected active resource, got: %+v", got)
	}

	// Release
	if err := mgr.Release("sub-1"); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// Must no longer exist
	_, found = mgr.Get("sub-1")
	if found {
		t.Fatalf("expected resource to be released/removed")
	}
}

func TestResourceManager_ByOwnerAndType(t *testing.T) {
	mgr := NewManager()

	_ = mgr.Register(Resource{ID: "r1", Owner: "plugin:downloader", Type: TypeTempFile})
	_ = mgr.Register(Resource{ID: "r2", Owner: "plugin:downloader", Type: TypeProcess})
	_ = mgr.Register(Resource{ID: "r3", Owner: "plugin:reminder", Type: TypeJob})

	dlRes := mgr.ByOwner("plugin:downloader")
	if len(dlRes) != 2 {
		t.Errorf("expected 2 resources for downloader, got %d", len(dlRes))
	}

	tempFiles := mgr.ByType(TypeTempFile)
	if len(tempFiles) != 1 || tempFiles[0].ID != "r1" {
		t.Errorf("unexpected temp file result: %+v", tempFiles)
	}

	snap := mgr.OwnerSnapshot("plugin:downloader")
	if snap.TotalActive != 2 || snap.CountsByType[TypeTempFile] != 1 || snap.CountsByType[TypeProcess] != 1 {
		t.Errorf("unexpected snapshot: %+v", snap)
	}
}

func TestResourceManager_DetectLeaks(t *testing.T) {
	mgr := NewManager()

	_ = mgr.Register(Resource{ID: "r1", Owner: "plugin:leaky", Type: TypeGoroutine})
	_ = mgr.Register(Resource{ID: "r2", Owner: "plugin:leaky", Type: TypeTempFile})

	leaks := mgr.DetectLeaks("plugin:leaky")
	if len(leaks) != 2 {
		t.Fatalf("expected 2 leaks, got %d", len(leaks))
	}
	if leaks[0].State != StateLeaked {
		t.Errorf("expected state leaked, got %s", leaks[0].State)
	}

	snap := mgr.OwnerSnapshot("plugin:leaky")
	if snap.Leaked != 2 {
		t.Errorf("expected 2 leaked resources in snapshot, got %d", snap.Leaked)
	}
}

func TestResourceManager_LeakPolicyAndForceCleanup(t *testing.T) {
	mgr := NewManager()
	mgr.SetLeakPolicy(LeakPolicyForceCleanup)

	cleaned := false
	cleanupFn := func() error {
		cleaned = true
		return nil
	}

	err := mgr.RegisterWithCleanup(Resource{ID: "c1", Owner: "plugin:orphan", Type: TypeTempFile}, cleanupFn)
	if err != nil {
		t.Fatalf("register with cleanup: %v", err)
	}

	leaks, err := mgr.HandleLeaks("plugin:orphan")
	if err != nil {
		t.Fatalf("handle leaks: %v", err)
	}
	if len(leaks) != 1 {
		t.Fatalf("expected 1 leak detected, got %d", len(leaks))
	}
	if !cleaned {
		t.Fatalf("expected cleanup function to be invoked")
	}

	// Active resources should now be empty after force cleanup
	if len(mgr.ByOwner("plugin:orphan")) != 0 {
		t.Fatalf("expected 0 active resources after force cleanup")
	}
}
