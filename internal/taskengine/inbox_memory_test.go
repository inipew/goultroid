package taskengine

import (
	"context"
	"testing"
	"unsafe"
)

func TestControlInboxBackingStoreIsPointerSized(t *testing.T) {
	const capacity = 2048
	e := NewEngine(Config{InboxCapacity: capacity})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())

	if cap(e.inbox) != capacity {
		t.Fatalf("inbox capacity=%d, want %d", cap(e.inbox), capacity)
	}
	pointerBacking := uintptr(cap(e.inbox)) * unsafe.Sizeof((*engineRequest)(nil))
	legacyBacking := uintptr(cap(e.inbox)) * unsafe.Sizeof(engineRequest{})
	if pointerBacking >= legacyBacking {
		t.Fatalf("pointer-backed inbox did not reduce backing store: pointer=%d legacy=%d", pointerBacking, legacyBacking)
	}
	// The union currently carries WorkSpec + TaskResult and is intentionally
	// much larger than a pointer. Keep this guard so a future type regression
	// cannot silently switch the channel back to value-sized storage.
	if legacyBacking < pointerBacking*8 {
		t.Fatalf("engineRequest unexpectedly small for this regression guard: pointer=%d legacy=%d", pointerBacking, legacyBacking)
	}
}

func TestRequestPoolClearsRetainedReferences(t *testing.T) {
	e := NewEngine(Config{})
	payload := make([]byte, 1024)
	req := e.acquireRequest(engineRequest{resourceName: string(payload)})
	if req.resourceName == "" {
		t.Fatal("test request was not populated")
	}
	e.releaseRequest(req)
	reused := e.acquireRequest(engineRequest{})
	if reused.resourceName != "" || reused.ctx != nil || reused.reply != nil {
		t.Fatalf("pooled request retained prior references: %+v", reused)
	}
	e.releaseRequest(reused)
}
