package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/core"
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
