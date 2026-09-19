package taskengine

import (
	"context"
	"testing"
)

func TestReleaseQueuedRequestsClearsPoolEnvelopes(t *testing.T) {
	e := NewEngine(Config{})
	inbox := make(chan *engineRequest, 2)
	first := e.acquireRequest(engineRequest{resourceName: "first", ctx: context.Background()})
	second := e.acquireRequest(engineRequest{resourceName: "second", reply: make(chan engineReply, 1)})
	inbox <- first
	inbox <- second

	e.releaseQueuedRequests(inbox)
	if got := len(inbox); got != 0 {
		t.Fatalf("queued requests remaining=%d", got)
	}

	// sync.Pool does not guarantee which object is returned, but every object
	// returned by releaseQueuedRequests must have been zeroed before pooling.
	for i := 0; i < 2; i++ {
		req := e.acquireRequest(engineRequest{})
		if req.resourceName != "" || req.ctx != nil || req.reply != nil {
			t.Fatalf("pooled request retained shutdown references: %+v", req)
		}
		e.releaseRequest(req)
	}
}
