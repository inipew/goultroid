package telegram

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"go.uber.org/zap"
)

type countingIdempotencyRepository struct {
	claims atomic.Int32
}

func (r *countingIdempotencyRepository) InitSchema(context.Context) error { return nil }
func (r *countingIdempotencyRepository) Claim(context.Context, string, time.Time, time.Time) (bool, error) {
	r.claims.Add(1)
	return true, nil
}
func (r *countingIdempotencyRepository) IsProcessed(context.Context, string, time.Time) (bool, error) {
	return false, nil
}
func (r *countingIdempotencyRepository) DeleteExpired(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (r *countingIdempotencyRepository) EarliestExpiry(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (r *countingIdempotencyRepository) Size(context.Context, time.Time) (int, error) {
	return 0, nil
}

func TestDispatcher_PlainMessageSkipsDurableIdempotency(t *testing.T) {
	repo := &countingIdempotencyRepository{}
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	var calls atomic.Int32
	d.AddMessageHandler(func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		calls.Add(1)
		return nil
	})

	msg := &tg.Message{ID: 101, PeerID: &tg.PeerChat{ChatID: 7}, Message: "ordinary traffic"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := repo.claims.Load(); got != 0 {
		t.Fatalf("plain message performed %d durable idempotency claims, want 0", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("plain message hook calls=%d, want 1", got)
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("duplicate dispatch: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("duplicate plain message reached hooks: calls=%d, want 1", got)
	}
	if got := repo.claims.Load(); got != 0 {
		t.Fatalf("duplicate plain message performed %d durable claims, want 0", got)
	}
}

func TestDispatcher_UnknownCommandSkipsDurableIdempotency(t *testing.T) {
	repo := &countingIdempotencyRepository{}
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	msg := &tg.Message{ID: 102, PeerID: &tg.PeerChat{ChatID: 7}, Message: ".notregistered"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := repo.claims.Load(); got != 0 {
		t.Fatalf("unknown command performed %d durable idempotency claims, want 0", got)
	}
}

func TestDispatcher_SuppressedCommandSkipsDurableIdempotency(t *testing.T) {
	repo := &countingIdempotencyRepository{}
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name: "mutate",
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	d := NewDispatcher(router, core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))
	d.AddPrioritizedMessageHandler(PrioritySecurity, func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		return core.ErrInterceptHandled
	})

	msg := &tg.Message{ID: 103, PeerID: &tg.PeerChat{ChatID: 7}, Message: ".mutate"}
	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := repo.claims.Load(); got != 0 {
		t.Fatalf("suppressed command performed %d durable idempotency claims, want 0", got)
	}
	if executed.Load() {
		t.Fatal("suppressed command executed")
	}
}

func TestIngressMessageDedupeSeparatesPeerNamespaces(t *testing.T) {
	cache := newIngressMessageDedupe(time.Minute, 8)
	now := time.Unix(1_700_000_000, 0)

	chat := &tg.Message{ID: 9, PeerID: &tg.PeerChat{ChatID: 42}}
	user := &tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 42}}
	channel := &tg.Message{ID: 9, PeerID: &tg.PeerChannel{ChannelID: 42}}

	for name, msg := range map[string]*tg.Message{"chat": chat, "user": user, "channel": channel} {
		if !cache.Accept(msg, now) {
			t.Fatalf("%s message was falsely treated as duplicate", name)
		}
	}
	for name, msg := range map[string]*tg.Message{"chat": chat, "user": user, "channel": channel} {
		if cache.Accept(msg, now.Add(time.Second)) {
			t.Fatalf("%s duplicate was accepted", name)
		}
	}
}

func TestIngressMessageDedupeExpiresAndStaysBounded(t *testing.T) {
	cache := newIngressMessageDedupe(time.Second, 4)
	now := time.Unix(1_700_000_000, 0)
	first := &tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 10}}

	if !cache.Accept(first, now) {
		t.Fatal("first message rejected")
	}
	if cache.Accept(first, now.Add(500*time.Millisecond)) {
		t.Fatal("duplicate inside TTL accepted")
	}
	if !cache.Accept(first, now.Add(2*time.Second)) {
		t.Fatal("expired message identity was not admitted")
	}

	for i := 2; i <= 100; i++ {
		msg := &tg.Message{ID: i, PeerID: &tg.PeerChat{ChatID: 10}}
		if !cache.Accept(msg, now.Add(3*time.Second)) {
			t.Fatalf("unique message %d rejected", i)
		}
	}
	if got := len(cache.entries); got > cache.maxEntries {
		t.Fatalf("resident dedupe entries=%d exceeds max=%d", got, cache.maxEntries)
	}
	if got := len(cache.ring); got != cache.maxEntries {
		t.Fatalf("ring size=%d, want %d", got, cache.maxEntries)
	}
}

func TestIngressMessageDedupeBypassesUnstableIdentity(t *testing.T) {
	cache := newIngressMessageDedupe(time.Minute, 4)
	now := time.Unix(1_700_000_000, 0)
	msg := &tg.Message{Message: "synthetic"}

	if !cache.Accept(msg, now) || !cache.Accept(msg, now) {
		t.Fatal("message without stable Telegram identity must not be deduplicated")
	}
}

func BenchmarkIngressMessageDedupeUnique(b *testing.B) {
	cache := newIngressMessageDedupe(defaultIngressDedupeTTL, defaultIngressDedupeCapacity)
	msg := &tg.Message{PeerID: &tg.PeerChat{ChatID: 100}}
	now := time.Unix(1_700_000_000, 0)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg.ID = i + 1
		if !cache.Accept(msg, now) {
			b.Fatal("unique message rejected")
		}
	}
}

func BenchmarkIngressMessageDedupeDuplicate(b *testing.B) {
	cache := newIngressMessageDedupe(defaultIngressDedupeTTL, defaultIngressDedupeCapacity)
	msg := &tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 100}}
	now := time.Unix(1_700_000_000, 0)
	if !cache.Accept(msg, now) {
		b.Fatal("initial message rejected")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cache.Accept(msg, now) {
			b.Fatal("duplicate message accepted")
		}
	}
}
