package pmrelay

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrepareVisitorRequiresEnabledPrivateVisitor(t *testing.T) {
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	service := NewService(repo, 7)

	message := IngressMessage{SenderID: 42, ChatID: 42, MessageID: 11}
	if _, handled, err := service.PrepareVisitor(context.Background(), message); err != nil || handled {
		t.Fatalf("disabled PrepareVisitor() handled=%v err=%v", handled, err)
	}

	service.SetEnabled(true)
	prepared, handled, err := service.PrepareVisitor(context.Background(), message)
	if err != nil || !handled {
		t.Fatalf("enabled PrepareVisitor() handled=%v err=%v", handled, err)
	}
	if prepared.Direction() != DeliveryVisitorToOwner ||
		prepared.VisitorUserID() != 42 ||
		prepared.SourceChatID() != 42 ||
		prepared.SourceMessageID() != 11 ||
		prepared.TargetChatID() != 7 {
		t.Fatalf("prepared visitor ingress = %+v", prepared)
	}
	if err := service.ExecutePrepared(context.Background(), prepared); err != nil {
		t.Fatalf("ExecutePrepared(visitor) error = %v", err)
	}

	if _, handled, err := service.PrepareVisitor(context.Background(), IngressMessage{
		SenderID: 42, ChatID: 99, MessageID: 12,
	}); err != nil || handled {
		t.Fatalf("group-shaped visitor handled=%v err=%v", handled, err)
	}
	if _, handled, err := service.PrepareVisitor(context.Background(), IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 13,
	}); err != nil || handled {
		t.Fatalf("owner visitor fallback handled=%v err=%v", handled, err)
	}
}

func TestPreparedVisitorFailsClosedAcrossPolicyRevision(t *testing.T) {
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	service := NewService(repo, 7)
	service.SetEnabled(true)

	prepared, handled, err := service.PrepareVisitor(context.Background(), IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if err != nil || !handled {
		t.Fatalf("PrepareVisitor() handled=%v err=%v", handled, err)
	}

	service.SetEnabled(false)
	if err := service.ExecutePrepared(context.Background(), prepared); !errors.Is(err, ErrDisabled) {
		t.Fatalf("ExecutePrepared(disabled) error=%v, want %v", err, ErrDisabled)
	}

	service.SetEnabled(true)
	if err := service.ExecutePrepared(context.Background(), prepared); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("ExecutePrepared(re-enabled old prepare) error=%v, want %v", err, ErrPreparedStale)
	}
}

func TestOwnerReplyPrepareAndRevalidationUsesDurableMapping(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	mapping := Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}
	if _, err := repo.EnsureMapping(ctx, mapping); err != nil {
		t.Fatal(err)
	}

	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(5 * time.Minute) }
	service.SetEnabled(true)

	prepared, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if err != nil || !handled {
		t.Fatalf("PrepareOwnerReply() handled=%v err=%v", handled, err)
	}
	if prepared.Direction() != DeliveryOwnerToVisitor ||
		prepared.VisitorUserID() != 42 ||
		prepared.TargetChatID() != 42 ||
		prepared.SourceMessageID() != 101 {
		t.Fatalf("prepared owner reply = %+v", prepared)
	}
	if err := service.ExecutePrepared(ctx, prepared); err != nil {
		t.Fatalf("ExecutePrepared(owner reply) error=%v", err)
	}

	if _, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 102, ReplyToMessageID: 999,
	}); err != nil || handled {
		t.Fatalf("unmapped owner reply handled=%v err=%v", handled, err)
	}

	if pruned, err := repo.PruneExpiredMappings(ctx, base.Add(2*time.Hour), 64); err != nil || pruned != 1 {
		t.Fatalf("PruneExpiredMappings()=%d err=%v", pruned, err)
	}
	if err := service.ExecutePrepared(ctx, prepared); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("ExecutePrepared(after mapping prune) error=%v, want %v", err, ErrPreparedStale)
	}
}

func TestPreparedOwnerReplyExpiresWhileQueued(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 10, 30, 0, 0, time.UTC)
	if _, err := repo.EnsureMapping(ctx, Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(5 * time.Minute) }
	service.SetEnabled(true)

	prepared, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if err != nil || !handled {
		t.Fatalf("PrepareOwnerReply() handled=%v err=%v", handled, err)
	}

	service.now = func() time.Time { return base.Add(2 * time.Hour) }
	if err := service.ExecutePrepared(ctx, prepared); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("ExecutePrepared(after mapping TTL) error=%v, want %v", err, ErrPreparedStale)
	}
}

func TestOwnerReplyExpiredMappingFailsClosedBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	if _, err := repo.EnsureMapping(ctx, Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(2 * time.Minute) }
	service.SetEnabled(true)

	_, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if !handled || !errors.Is(err, ErrMappingExpired) {
		t.Fatalf("expired PrepareOwnerReply() handled=%v err=%v", handled, err)
	}
}
