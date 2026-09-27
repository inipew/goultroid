package telegram

import (
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestIngressDedupeClaimReleaseAllowsRetry(t *testing.T) {
	cache := newIngressMessageDedupe(time.Minute, 8)
	now := time.Unix(1_700_000_000, 0)
	msg := &tg.Message{ID: 9, PeerID: &tg.PeerChat{ChatID: 42}}

	claim, accepted := cache.Begin(msg, now)
	if !accepted {
		t.Fatal("initial ingress claim rejected")
	}
	if _, duplicateAccepted := cache.Begin(msg, now.Add(time.Second)); duplicateAccepted {
		t.Fatal("duplicate was accepted while first generation was active")
	}

	claim.Release()
	if _, retryAccepted := cache.Begin(msg, now.Add(2*time.Second)); !retryAccepted {
		t.Fatal("released ingress generation did not become retryable")
	}
}

func TestIngressDedupeClaimReleaseIsGenerationFenced(t *testing.T) {
	cache := newIngressMessageDedupe(time.Second, 8)
	now := time.Unix(1_700_000_000, 0)
	msg := &tg.Message{ID: 10, PeerID: &tg.PeerChat{ChatID: 42}}

	oldClaim, accepted := cache.Begin(msg, now)
	if !accepted {
		t.Fatal("initial ingress claim rejected")
	}
	if _, accepted := cache.Begin(msg, now.Add(2*time.Second)); !accepted {
		t.Fatal("expired ingress generation was not reclaimable")
	}

	oldClaim.Release()
	if _, accepted := cache.Begin(msg, now.Add(2500*time.Millisecond)); accepted {
		t.Fatal("stale release removed the newer ingress generation")
	}
}
