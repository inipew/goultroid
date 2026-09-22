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
	if err := service.RevalidatePrepared(context.Background(), prepared); err != nil {
		t.Fatalf("RevalidatePrepared(visitor) error = %v", err)
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
	if err := service.RevalidatePrepared(context.Background(), prepared); !errors.Is(err, ErrDisabled) {
		t.Fatalf("RevalidatePrepared(disabled) error=%v, want %v", err, ErrDisabled)
	}

	service.SetEnabled(true)
	if err := service.RevalidatePrepared(context.Background(), prepared); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("RevalidatePrepared(re-enabled old prepare) error=%v, want %v", err, ErrPreparedStale)
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
	if err := service.RevalidatePrepared(ctx, prepared); err != nil {
		t.Fatalf("RevalidatePrepared(owner reply) error=%v", err)
	}

	if _, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 102, ReplyToMessageID: 999,
	}); err != nil || handled {
		t.Fatalf("unmapped owner reply handled=%v err=%v", handled, err)
	}

	if pruned, err := repo.PruneExpiredMappings(ctx, base.Add(2*time.Hour), 64); err != nil || pruned != 1 {
		t.Fatalf("PruneExpiredMappings()=%d err=%v", pruned, err)
	}
	if err := service.RevalidatePrepared(ctx, prepared); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("RevalidatePrepared(after mapping prune) error=%v, want %v", err, ErrPreparedStale)
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
	if err := service.RevalidatePrepared(ctx, prepared); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("RevalidatePrepared(after mapping TTL) error=%v, want %v", err, ErrPreparedStale)
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


type visitorTransportStub struct {
	calls    int
	requests []VisitorForward
	message  int
	err      error
}

func (t *visitorTransportStub) ForwardVisitor(_ context.Context, request VisitorForward) (int, error) {
	t.calls++
	t.requests = append(t.requests, request)
	if t.err != nil {
		return 0, t.err
	}
	return t.message, nil
}

type commitFailRepository struct {
	Repository
	failNext bool
}

func (r *commitFailRepository) CommitDelivery(
	ctx context.Context,
	key DeliveryKey,
	claimID string,
	targetMessageID int,
	deliveredAt time.Time,
) (DeliveryIntent, error) {
	if r.failNext {
		r.failNext = false
		return DeliveryIntent{}, errors.New("simulated commit failure")
	}
	return r.Repository.CommitDelivery(ctx, key, claimID, targetMessageID, deliveredAt)
}

type claimHookRepository struct {
	Repository
	afterClaim func()
}

func (r *claimHookRepository) ClaimDelivery(
	ctx context.Context,
	key DeliveryKey,
	now time.Time,
	claimID string,
	claimExpiresAt time.Time,
) (DeliveryIntent, error) {
	delivery, err := r.Repository.ClaimDelivery(ctx, key, now, claimID, claimExpiresAt)
	if err == nil && r.afterClaim != nil {
		r.afterClaim()
	}
	return delivery, err
}

func prepareVisitorForDelivery(t *testing.T, service *Service) PreparedIngress {
	t.Helper()
	prepared, handled, err := service.PrepareVisitor(context.Background(), IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if err != nil || !handled {
		t.Fatalf("PrepareVisitor() handled=%v err=%v", handled, err)
	}
	return prepared
}

func TestExecuteVisitorPersistsDeliveryMappingAndAudience(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	now := base
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	service.randomID = func() (int64, error) { return 777, nil }
	service.claimID = func() (string, error) { return "claim-a", nil }
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)

	transport := &visitorTransportStub{message: 501}
	if err := service.ExecuteVisitor(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteVisitor() error = %v", err)
	}
	if transport.calls != 1 || len(transport.requests) != 1 {
		t.Fatalf("transport calls=%d requests=%+v", transport.calls, transport.requests)
	}
	request := transport.requests[0]
	if request.SourceChatID != 42 || request.SourceMessageID != 11 ||
		request.TargetChatID != 7 || request.RandomID != 777 {
		t.Fatalf("forward request=%+v", request)
	}

	delivery, err := repo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetMessageID != 501 || delivery.RandomID != 777 || delivery.Attempts != 1 {
		t.Fatalf("delivery=%+v", delivery)
	}
	mapping, err := repo.GetMapping(ctx, 7, 501)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.VisitorUserID != 42 || mapping.VisitorMessageID != 11 {
		t.Fatalf("mapping=%+v", mapping)
	}
	audience, err := repo.GetAudience(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if audience.Sources&AudienceSourceRelay == 0 {
		t.Fatalf("audience sources=%d, missing relay", audience.Sources)
	}

	// A duplicate admitted occurrence heals/finalizes durable state but must not
	// perform a second Telegram forward once delivery is committed.
	now = base.Add(24 * time.Hour)
	service.randomID = func() (int64, error) { return 999, nil }
	service.claimID = func() (string, error) { return "claim-b", nil }
	if err := service.ExecuteVisitor(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteVisitor(duplicate) error = %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("duplicate completed delivery forwarded again: calls=%d", transport.calls)
	}
	audience, err = repo.GetAudience(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !audience.LastSeenAt.Equal(base) {
		t.Fatalf("duplicate recovery extended audience activity: last_seen=%v want %v", audience.LastSeenAt, base)
	}
}

func TestExecuteVisitorExpiredCompletedDeliveryDoesNotResurrectMapping(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 12, 15, 0, 0, time.UTC)
	now := base
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	service.randomID = func() (int64, error) { return 777, nil }
	service.claimID = func() (string, error) { return "claim-a", nil }
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)
	transport := &visitorTransportStub{message: 501}

	if err := service.ExecuteVisitor(ctx, prepared, transport); err != nil {
		t.Fatal(err)
	}
	if pruned, err := repo.PruneExpiredMappings(ctx, base.Add(DefaultMappingRetention+time.Second), 64); err != nil || pruned != 1 {
		t.Fatalf("PruneExpiredMappings()=%d err=%v", pruned, err)
	}
	now = base.Add(DefaultDeliveryRetention + time.Second)
	if err := service.ExecuteVisitor(ctx, prepared, transport); !errors.Is(err, ErrDeliveryExpired) {
		t.Fatalf("ExecuteVisitor(expired completed) error=%v, want %v", err, ErrDeliveryExpired)
	}
	if transport.calls != 1 {
		t.Fatalf("expired completed delivery forwarded again: calls=%d", transport.calls)
	}
	if _, err := repo.GetMapping(ctx, 7, 501); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("expired delivery resurrected mapping: %v", err)
	}
}

func TestExecuteVisitorTransportFailureReleasesClaimAndReusesRandomID(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 12, 30, 0, 0, time.UTC)
	now := base
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	nextRandom := int64(777)
	service.randomID = func() (int64, error) {
		value := nextRandom
		nextRandom = 999
		return value, nil
	}
	nextClaim := "claim-a"
	service.claimID = func() (string, error) {
		value := nextClaim
		nextClaim = "claim-b"
		return value, nil
	}
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)

	transportErr := errors.New("telegram unavailable")
	transport := &visitorTransportStub{message: 501, err: transportErr}
	if err := service.ExecuteVisitor(ctx, prepared, transport); !errors.Is(err, transportErr) {
		t.Fatalf("ExecuteVisitor(failure) error=%v, want %v", err, transportErr)
	}
	delivery, err := repo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.RandomID != 777 || delivery.ClaimID != "" || delivery.Completed() || delivery.Attempts != 1 {
		t.Fatalf("released delivery=%+v", delivery)
	}
	if _, err := repo.GetMapping(ctx, 7, 501); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("mapping after failed transport error=%v", err)
	}
	if _, err := repo.GetAudience(ctx, 42); !errors.Is(err, ErrAudienceNotFound) {
		t.Fatalf("audience after failed transport error=%v", err)
	}

	now = base.Add(time.Second)
	transport.err = nil
	if err := service.ExecuteVisitor(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteVisitor(retry) error=%v", err)
	}
	if transport.calls != 2 || transport.requests[0].RandomID != 777 || transport.requests[1].RandomID != 777 {
		t.Fatalf("retry random ids=%+v", transport.requests)
	}
}

func TestExecuteVisitorRecoversCrashAfterSendWithSameRandomID(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	repo := &commitFailRepository{Repository: sqliteRepo, failNext: true}
	base := time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC)
	now := base
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	nextRandom := int64(777)
	service.randomID = func() (int64, error) {
		value := nextRandom
		nextRandom = 999
		return value, nil
	}
	nextClaim := "claim-a"
	service.claimID = func() (string, error) {
		value := nextClaim
		nextClaim = "claim-b"
		return value, nil
	}
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)
	transport := &visitorTransportStub{message: 501}

	if err := service.ExecuteVisitor(ctx, prepared, transport); err == nil {
		t.Fatal("ExecuteVisitor() unexpectedly succeeded with failed durable commit")
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.RandomID != 777 || delivery.ClaimID != "claim-a" || delivery.Completed() {
		t.Fatalf("ambiguous post-send delivery=%+v", delivery)
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("mapping existed before delivery commit: %v", err)
	}

	// Simulate restart/recovery after the old execution lease expires. Telegram
	// sees the same random_id, so the second transport attempt is the same
	// logical forward rather than a new message.
	now = base.Add(DeliveryClaimTTL + time.Second)
	if err := service.ExecuteVisitor(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteVisitor(recovery) error=%v", err)
	}
	if transport.calls != 2 ||
		transport.requests[0].RandomID != 777 ||
		transport.requests[1].RandomID != 777 {
		t.Fatalf("recovery requests=%+v", transport.requests)
	}
	delivery, err = sqliteRepo.GetDelivery(ctx, delivery.DeliveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetMessageID != 501 || delivery.Attempts != 2 {
		t.Fatalf("recovered delivery=%+v", delivery)
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); err != nil {
		t.Fatalf("mapping after recovery: %v", err)
	}
	if _, err := sqliteRepo.GetAudience(ctx, 42); err != nil {
		t.Fatalf("audience after recovery: %v", err)
	}
}

func TestExecuteVisitorRevalidatesAgainAfterClaimBeforeTransport(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	repo := &claimHookRepository{Repository: sqliteRepo}
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base }
	service.randomID = func() (int64, error) { return 777, nil }
	service.claimID = func() (string, error) { return "claim-a", nil }
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)
	repo.afterClaim = func() { service.SetEnabled(false) }
	transport := &visitorTransportStub{message: 501}

	if err := service.ExecuteVisitor(ctx, prepared, transport); !errors.Is(err, ErrDisabled) {
		t.Fatalf("ExecuteVisitor(disabled after claim) error=%v, want %v", err, ErrDisabled)
	}
	if transport.calls != 0 {
		t.Fatalf("transport ran after post-claim revalidation failed: calls=%d", transport.calls)
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.ClaimID != "" || delivery.Completed() {
		t.Fatalf("failed post-claim revalidation left active delivery=%+v", delivery)
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("mapping created after denied transport: %v", err)
	}
	if _, err := sqliteRepo.GetAudience(ctx, 42); !errors.Is(err, ErrAudienceNotFound) {
		t.Fatalf("audience created after denied transport: %v", err)
	}
}

func TestExecuteVisitorLazilyReclaimsExpiredCapacity(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 1, Deliveries: 1, Audience: 1})
	now := time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC)
	old := now.Add(-200 * 24 * time.Hour)

	if _, err := repo.EnsureMapping(ctx, Mapping{
		OwnerChatID: 7, OwnerMessageID: 400,
		VisitorUserID: 99, VisitorMessageID: 1,
		CreatedAt: old, ExpiresAt: old.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureDelivery(ctx, DeliveryIntent{
		DeliveryKey: DeliveryKey{
			Direction: DeliveryVisitorToOwner, SourceChatID: 99, SourceMessageID: 1,
		},
		TargetChatID: 7,
		RandomID:     123,
		CreatedAt:    old,
		UpdatedAt:    old,
		ExpiresAt:    old.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TouchAudience(ctx, AudienceTouch{
		UserID: 99, Source: AudienceSourceStart, SeenAt: old,
	}); err != nil {
		t.Fatal(err)
	}

	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	service.randomID = func() (int64, error) { return 777, nil }
	service.claimID = func() (string, error) { return "claim-a", nil }
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)
	transport := &visitorTransportStub{message: 501}

	if err := service.ExecuteVisitor(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteVisitor() error = %v", err)
	}
	if _, err := repo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	}); err != nil {
		t.Fatalf("new delivery missing after lazy reclamation: %v", err)
	}
	if _, err := repo.GetMapping(ctx, 7, 501); err != nil {
		t.Fatalf("new mapping missing after lazy reclamation: %v", err)
	}
	if _, err := repo.GetAudience(ctx, 42); err != nil {
		t.Fatalf("new audience member missing after lazy reclamation: %v", err)
	}
	if _, err := repo.GetMapping(ctx, 7, 400); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("expired mapping survived capacity reclamation: %v", err)
	}
	if _, err := repo.GetAudience(ctx, 99); !errors.Is(err, ErrAudienceNotFound) {
		t.Fatalf("stale audience survived capacity reclamation: %v", err)
	}
}


type ownerTransportStub struct {
	calls    int
	requests []OwnerSend
	message  int
	err      error
}

func (t *ownerTransportStub) SendOwnerReply(_ context.Context, request OwnerSend) (int, error) {
	t.calls++
	t.requests = append(t.requests, request)
	if t.err != nil {
		return 0, t.err
	}
	return t.message, nil
}

func prepareOwnerForDelivery(
	t *testing.T,
	ctx context.Context,
	repo Repository,
	service *Service,
	base time.Time,
) PreparedIngress {
	t.Helper()
	if _, err := repo.EnsureMapping(ctx, Mapping{
		OwnerChatID: 7, OwnerMessageID: 500,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	prepared, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 501, ReplyToMessageID: 500,
	})
	if err != nil || !handled {
		t.Fatalf("PrepareOwnerReply() handled=%v err=%v", handled, err)
	}
	return prepared
}

func TestExecuteOwnerPersistsDurableBotDeliveryWithoutNewMappingOrAudience(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 16, 0, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(5 * time.Minute) }
	service.randomID = func() (int64, error) { return 888, nil }
	service.claimID = func() (string, error) { return "owner-claim-a", nil }
	service.SetEnabled(true)
	prepared := prepareOwnerForDelivery(t, ctx, repo, service, base)

	transport := &ownerTransportStub{message: 601}
	if err := service.ExecuteOwner(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteOwner() error=%v", err)
	}
	if transport.calls != 1 || len(transport.requests) != 1 {
		t.Fatalf("owner transport calls=%d requests=%+v", transport.calls, transport.requests)
	}
	request := transport.requests[0]
	if request.SourceChatID != 7 || request.SourceMessageID != 501 ||
		request.TargetChatID != 42 || request.RandomID != 888 {
		t.Fatalf("owner send request=%+v", request)
	}

	delivery, err := repo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 501,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetChatID != 42 ||
		delivery.TargetMessageID != 601 || delivery.RandomID != 888 || delivery.Attempts != 1 {
		t.Fatalf("owner delivery=%+v", delivery)
	}
	if count, err := repo.CountMappings(ctx); err != nil || count != 1 {
		t.Fatalf("mapping count=%d err=%v, want original mapping only", count, err)
	}
	if count, err := repo.CountAudience(ctx); err != nil || count != 0 {
		t.Fatalf("audience count=%d err=%v, owner reply must not imply visitor activity", count, err)
	}

	service.randomID = func() (int64, error) { return 999, nil }
	service.claimID = func() (string, error) { return "owner-claim-b", nil }
	if err := service.ExecuteOwner(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteOwner(duplicate) error=%v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("completed owner reply sent twice: calls=%d", transport.calls)
	}
}

func TestExecuteOwnerTransportFailureReleasesClaimAndReusesRandomID(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	base := time.Date(2026, 9, 22, 16, 30, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	nextRandom := int64(888)
	service.randomID = func() (int64, error) {
		value := nextRandom
		nextRandom = 999
		return value, nil
	}
	nextClaim := "owner-claim-a"
	service.claimID = func() (string, error) {
		value := nextClaim
		nextClaim = "owner-claim-b"
		return value, nil
	}
	service.SetEnabled(true)
	prepared := prepareOwnerForDelivery(t, ctx, repo, service, base)

	transportErr := errors.New("telegram unavailable")
	transport := &ownerTransportStub{message: 601, err: transportErr}
	if err := service.ExecuteOwner(ctx, prepared, transport); !errors.Is(err, transportErr) {
		t.Fatalf("ExecuteOwner(failure) error=%v, want %v", err, transportErr)
	}
	delivery, err := repo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 501,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.RandomID != 888 || delivery.ClaimID != "" || delivery.Completed() || delivery.Attempts != 1 {
		t.Fatalf("released owner delivery=%+v", delivery)
	}

	now = now.Add(time.Second)
	transport.err = nil
	if err := service.ExecuteOwner(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteOwner(retry) error=%v", err)
	}
	if transport.calls != 2 ||
		transport.requests[0].RandomID != 888 ||
		transport.requests[1].RandomID != 888 {
		t.Fatalf("owner retry requests=%+v", transport.requests)
	}
}

func TestExecuteOwnerRecoversCommitFailureWithSameRandomID(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	repo := &commitFailRepository{Repository: sqliteRepo, failNext: true}
	base := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	nextRandom := int64(888)
	service.randomID = func() (int64, error) {
		value := nextRandom
		nextRandom = 999
		return value, nil
	}
	nextClaim := "owner-claim-a"
	service.claimID = func() (string, error) {
		value := nextClaim
		nextClaim = "owner-claim-b"
		return value, nil
	}
	service.SetEnabled(true)
	prepared := prepareOwnerForDelivery(t, ctx, repo, service, base)
	transport := &ownerTransportStub{message: 601}

	if err := service.ExecuteOwner(ctx, prepared, transport); err == nil {
		t.Fatal("ExecuteOwner() unexpectedly succeeded with failed durable commit")
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 501,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.RandomID != 888 || delivery.ClaimID != "owner-claim-a" || delivery.Completed() {
		t.Fatalf("ambiguous owner delivery=%+v", delivery)
	}

	now = now.Add(DeliveryClaimTTL + time.Second)
	if err := service.ExecuteOwner(ctx, prepared, transport); err != nil {
		t.Fatalf("ExecuteOwner(recovery) error=%v", err)
	}
	if transport.calls != 2 ||
		transport.requests[0].RandomID != 888 ||
		transport.requests[1].RandomID != 888 {
		t.Fatalf("owner recovery requests=%+v", transport.requests)
	}
	delivery, err = sqliteRepo.GetDelivery(ctx, delivery.DeliveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetMessageID != 601 || delivery.Attempts != 2 {
		t.Fatalf("recovered owner delivery=%+v", delivery)
	}
}

func TestExecuteOwnerRevalidatesMappingAgainAfterClaim(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8})
	repo := &claimHookRepository{Repository: sqliteRepo}
	base := time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)
	service := NewService(repo, 7)
	service.now = func() time.Time { return now }
	service.randomID = func() (int64, error) { return 888, nil }
	service.claimID = func() (string, error) { return "owner-claim-a", nil }
	service.SetEnabled(true)
	prepared := prepareOwnerForDelivery(t, ctx, repo, service, base)
	repo.afterClaim = func() {
		now = base.Add(2 * time.Hour)
	}
	transport := &ownerTransportStub{message: 601}

	if err := service.ExecuteOwner(ctx, prepared, transport); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("ExecuteOwner(expired mapping after claim) error=%v, want %v", err, ErrPreparedStale)
	}
	if transport.calls != 0 {
		t.Fatalf("owner transport ran after mapping revalidation failed: calls=%d", transport.calls)
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 501,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.ClaimID != "" || delivery.Completed() {
		t.Fatalf("mapping revalidation failure left active delivery=%+v", delivery)
	}
}


func TestPrepareVisitorSuppressesDurablyBlockedVisitorBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8})
	base := time.Date(2026, 9, 22, 20, 30, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base }
	service.SetEnabled(true)
	if _, err := service.BlockVisitor(ctx, 42, "spam"); err != nil {
		t.Fatal(err)
	}

	_, handled, err := service.PrepareVisitor(ctx, IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if err != nil || handled {
		t.Fatalf("PrepareVisitor(blocked) handled=%v err=%v, want false nil", handled, err)
	}
}

func TestPrepareOwnerReplyFailsClosedForBlockedMappedVisitor(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8})
	base := time.Date(2026, 9, 22, 20, 45, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(time.Minute) }
	service.SetEnabled(true)
	if _, err := repo.EnsureMapping(ctx, Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BlockVisitor(ctx, 42, "abuse"); err != nil {
		t.Fatal(err)
	}

	_, handled, err := service.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if !handled || !errors.Is(err, ErrVisitorBlocked) {
		t.Fatalf("PrepareOwnerReply(blocked) handled=%v err=%v, want handled ErrVisitorBlocked", handled, err)
	}
}

func TestExecuteVisitorRevalidatesBlockAfterClaimBeforeTransport(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8})
	repo := &claimHookRepository{Repository: sqliteRepo}
	base := time.Date(2026, 9, 22, 21, 0, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base }
	service.randomID = func() (int64, error) { return 777, nil }
	service.claimID = func() (string, error) { return "claim-block-visitor", nil }
	service.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, service)
	repo.afterClaim = func() {
		if _, err := service.BlockVisitor(context.Background(), 42, "late block"); err != nil {
			t.Errorf("BlockVisitor() after claim error=%v", err)
		}
	}
	transport := &visitorTransportStub{message: 501}

	if err := service.ExecuteVisitor(ctx, prepared, transport); !errors.Is(err, ErrVisitorBlocked) {
		t.Fatalf("ExecuteVisitor(blocked after claim) error=%v, want %v", err, ErrVisitorBlocked)
	}
	if transport.calls != 0 {
		t.Fatalf("visitor transport ran after late block: calls=%d", transport.calls)
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.ClaimID != "" || delivery.Completed() {
		t.Fatalf("late block left active visitor delivery=%+v", delivery)
	}
}

func TestExecuteOwnerRevalidatesBlockAfterClaimBeforeTransport(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8})
	repo := &claimHookRepository{Repository: sqliteRepo}
	base := time.Date(2026, 9, 22, 21, 30, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(time.Minute) }
	service.randomID = func() (int64, error) { return 888, nil }
	service.claimID = func() (string, error) { return "claim-block-owner", nil }
	service.SetEnabled(true)
	prepared := prepareOwnerForDelivery(t, ctx, repo, service, base)
	repo.afterClaim = func() {
		if _, err := service.BlockVisitor(context.Background(), 42, "late block"); err != nil {
			t.Errorf("BlockVisitor() after claim error=%v", err)
		}
	}
	transport := &ownerTransportStub{message: 601}

	if err := service.ExecuteOwner(ctx, prepared, transport); !errors.Is(err, ErrVisitorBlocked) {
		t.Fatalf("ExecuteOwner(blocked after claim) error=%v, want %v", err, ErrVisitorBlocked)
	}
	if transport.calls != 0 {
		t.Fatalf("owner transport ran after late block: calls=%d", transport.calls)
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 501,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.ClaimID != "" || delivery.Completed() {
		t.Fatalf("late block left active owner delivery=%+v", delivery)
	}
}

func TestRelayControlStatusAndVisitorDetails(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepository(t, Limits{Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8})
	base := time.Date(2026, 9, 22, 22, 0, 0, 0, time.UTC)
	service := NewService(repo, 7)
	service.now = func() time.Time { return base.Add(time.Minute) }
	service.SetEnabled(true)
	if _, err := repo.EnsureMapping(ctx, Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TouchAudience(ctx, AudienceTouch{
		UserID: 42, Source: AudienceSourceRelay, SeenAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BlockVisitor(ctx, 42, "spam"); err != nil {
		t.Fatal(err)
	}

	status, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.Mappings != 1 || status.Audience != 1 || status.Blocked != 1 {
		t.Fatalf("Status()=%+v", status)
	}

	details, err := service.VisitorDetails(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if details.Mapping.VisitorUserID != 42 || details.Audience == nil || details.Block == nil ||
		details.Block.Reason != "spam" {
		t.Fatalf("VisitorDetails()=%+v", details)
	}
	if removed, err := service.UnblockVisitor(ctx, 42); err != nil || !removed {
		t.Fatalf("UnblockVisitor() removed=%v err=%v", removed, err)
	}
	details, err = service.VisitorDetails(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if details.Block != nil {
		t.Fatalf("VisitorDetails() block after unblock=%+v", details.Block)
	}
}
