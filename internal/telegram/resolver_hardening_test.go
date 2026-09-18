package telegram

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestResolverNumericUserFailsClosedWithoutAccessHash(t *testing.T) {
	r := NewResolver(nil, nil)
	peer, id, err := r.ResolveUser(context.Background(), "12345")
	if peer != nil || id != 0 {
		t.Fatalf("expected no peer/id, got peer=%T id=%d", peer, id)
	}
	if !errors.Is(err, core.ErrAccessHashMissing) {
		t.Fatalf("expected ErrAccessHashMissing, got %v", err)
	}
}

func TestResolverNumericChannelFailsClosedWithoutAccessHash(t *testing.T) {
	r := NewResolver(nil, nil)
	peer, err := r.ResolveChat(context.Background(), "-10012345")
	if peer != nil {
		t.Fatalf("expected no peer, got %T", peer)
	}
	if !errors.Is(err, core.ErrAccessHashMissing) {
		t.Fatalf("expected ErrAccessHashMissing, got %v", err)
	}
}

func TestResolverInvalidateRefContext_RemovesHashButKeepsUsernameMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(fmt.Sprintf("file:resolver_invalidate_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)
	key := peers.Key{Prefix: "user", ID: 4242}
	if err := storage.Save(ctx, key, peers.Value{AccessHash: 111}); err != nil {
		t.Fatalf("save access hash: %v", err)
	}
	if err := storage.SaveEntity(ctx, "user", 4242, "target", "", "", "", ""); err != nil {
		t.Fatalf("save entity: %v", err)
	}

	resolver := NewResolver(nil, nil)
	resolver.SetStorage(storage)
	if err := resolver.InvalidateRefContext(ctx, "@target"); err != nil {
		t.Fatalf("invalidate ref: %v", err)
	}

	if _, found, err := storage.Find(ctx, key); err != nil {
		t.Fatalf("find invalidated hash: %v", err)
	} else if found {
		t.Fatal("expected stale access hash to be deleted")
	}
	foundKey, value, found, err := storage.FindByUsername(ctx, "target")
	if err != nil {
		t.Fatalf("find username metadata: %v", err)
	}
	if !found || foundKey != key {
		t.Fatalf("expected username metadata to survive invalidation, found=%v key=%+v", found, foundKey)
	}
	if value.AccessHash != 0 {
		t.Fatalf("expected username metadata to resolve with no cached access hash, got %d", value.AccessHash)
	}
}


func TestResolverLifecycleFollowsParentContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	resolver := NewResolverWithContext(parent, nil, nil, ResolverCacheConfig{MaxEntries: 8})
	t.Cleanup(func() { _ = resolver.Close() })

	cancel()
	select {
	case <-resolver.lifecycleCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("resolver lifecycle did not follow parent cancellation")
	}
}

func TestResolverCloseCancelsLifecycle(t *testing.T) {
	resolver := NewResolverWithContext(context.Background(), nil, nil, ResolverCacheConfig{MaxEntries: 8})
	if err := resolver.Close(); err != nil {
		t.Fatalf("close resolver: %v", err)
	}
	select {
	case <-resolver.lifecycleCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("resolver close did not cancel lifecycle")
	}
}


func TestResolverNetworkAdmissionIsBoundedAndCancelable(t *testing.T) {
	resolver := NewResolverWithContext(context.Background(), nil, nil, ResolverCacheConfig{
		MaxEntries:           8,
		MaxConcurrentNetwork: 2,
	})
	t.Cleanup(func() { _ = resolver.Close() })

	release1, err := resolver.acquireNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release1()
	release2, err := resolver.acquireNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release2()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := resolver.acquireNetwork(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded admission to honor cancellation, got %v", err)
	}
}

func TestResolverNetworkAdmissionReleasesCapacity(t *testing.T) {
	resolver := NewResolverWithContext(context.Background(), nil, nil, ResolverCacheConfig{
		MaxEntries:           8,
		MaxConcurrentNetwork: 1,
	})
	t.Cleanup(func() { _ = resolver.Close() })

	release, err := resolver.acquireNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	releaseAgain, err := resolver.acquireNetwork(ctx)
	if err != nil {
		t.Fatalf("released network capacity was not reusable: %v", err)
	}
	releaseAgain()
}

func TestStandaloneResolverUsesBoundedExecutor(t *testing.T) {
	resolver := NewResolverWithContext(context.Background(), nil, nil, ResolverCacheConfig{MaxEntries: 8})
	t.Cleanup(func() { _ = resolver.Close() })
	if resolver.executor == nil {
		t.Fatal("standalone resolver did not construct an executor")
	}
	if _, ok := resolver.executor.limiter.(*HierarchicalRPCLimiter); !ok {
		t.Fatalf("standalone resolver limiter=%T, want *HierarchicalRPCLimiter", resolver.executor.limiter)
	}
}

func TestResolverConstructorKeepsSharedExecutor(t *testing.T) {
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter:       NewHierarchicalRPCLimiter(DefaultHierarchicalLimiterConfig()),
		DefaultPolicy: defaultExecutorPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolverWithContextAndExecutor(context.Background(), nil, nil, ResolverCacheConfig{MaxEntries: 8}, exec)
	t.Cleanup(func() { _ = resolver.Close() })
	if resolver.executor != exec {
		t.Fatal("resolver did not retain shared executor")
	}
	resolver.SetExecutor(nil)
	if resolver.executor != exec {
		t.Fatal("nil SetExecutor cleared shared executor")
	}
}
