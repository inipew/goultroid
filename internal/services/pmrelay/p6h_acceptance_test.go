package pmrelay

import (
	"context"
	"errors"
	"testing"
	"time"
)

type p6hMappingFailRepository struct {
	Repository
	failNext bool
}

func (r *p6hMappingFailRepository) EnsureMapping(
	ctx context.Context,
	mapping Mapping,
) (Mapping, error) {
	if r.failNext {
		r.failNext = false
		return Mapping{}, errors.New("simulated mapping finalize failure")
	}
	return r.Repository.EnsureMapping(ctx, mapping)
}

type p6hAudienceFailRepository struct {
	Repository
	failNext bool
}

func (r *p6hAudienceFailRepository) TouchAudience(
	ctx context.Context,
	touch AudienceTouch,
) (AudienceMember, error) {
	if r.failNext {
		r.failNext = false
		return AudienceMember{}, errors.New("simulated audience finalize failure")
	}
	return r.Repository.TouchAudience(ctx, touch)
}

func TestP6HVisitorAmbiguousSendRecoversAcrossServiceRestart(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{
		Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8,
	})
	firstRepo := &commitFailRepository{Repository: sqliteRepo, failNext: true}
	base := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	now := base

	first := NewService(firstRepo, 7)
	first.now = func() time.Time { return now }
	first.randomID = func() (int64, error) { return 777, nil }
	first.claimID = func() (string, error) { return "first-claim", nil }
	first.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, first)
	transport := &visitorTransportStub{message: 501}

	if err := first.ExecuteVisitor(ctx, prepared, transport); err == nil {
		t.Fatal("first ExecuteVisitor unexpectedly succeeded after simulated post-send commit crash")
	}
	pending, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending.RandomID != 777 || pending.Completed() || pending.ClaimID != "first-claim" {
		t.Fatalf("pending delivery after crash=%+v", pending)
	}

	// Reconstruct all process-local service state. The only recovery authority is
	// durable SQLite state plus the source occurrence.
	now = base.Add(DeliveryClaimTTL + time.Second)
	second := NewService(sqliteRepo, 7)
	second.now = func() time.Time { return now }
	second.randomID = func() (int64, error) { return 999, nil }
	second.claimID = func() (string, error) { return "restart-claim", nil }
	second.SetEnabled(true)
	reprepared := prepareVisitorForDelivery(t, second)

	if err := second.ExecuteVisitor(ctx, reprepared, transport); err != nil {
		t.Fatalf("ExecuteVisitor after restart error=%v", err)
	}
	if transport.calls != 2 ||
		transport.requests[0].RandomID != 777 ||
		transport.requests[1].RandomID != 777 {
		t.Fatalf("restart changed logical Telegram identity: %+v", transport.requests)
	}
	recovered, err := sqliteRepo.GetDelivery(ctx, pending.DeliveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Completed() || recovered.TargetMessageID != 501 ||
		recovered.ClaimID != "" || recovered.Attempts != 2 {
		t.Fatalf("recovered delivery=%+v", recovered)
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); err != nil {
		t.Fatalf("mapping not healed after restart: %v", err)
	}
	if member, err := sqliteRepo.GetAudience(ctx, 42); err != nil ||
		member.Sources&AudienceSourceRelay == 0 {
		t.Fatalf("audience not healed after restart: member=%+v err=%v", member, err)
	}
}

func TestP6HOwnerAmbiguousSendRecoversAcrossServiceRestart(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{
		Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8,
	})
	base := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Minute)

	firstRepo := &commitFailRepository{Repository: sqliteRepo, failNext: true}
	first := NewService(firstRepo, 7)
	first.now = func() time.Time { return now }
	first.randomID = func() (int64, error) { return 888, nil }
	first.claimID = func() (string, error) { return "owner-first-claim", nil }
	first.SetEnabled(true)
	prepared := prepareOwnerForDelivery(t, ctx, firstRepo, first, base)
	transport := &ownerTransportStub{message: 601}

	if err := first.ExecuteOwner(ctx, prepared, transport); err == nil {
		t.Fatal("first ExecuteOwner unexpectedly succeeded after simulated post-send commit crash")
	}

	now = now.Add(DeliveryClaimTTL + time.Second)
	second := NewService(sqliteRepo, 7)
	second.now = func() time.Time { return now }
	second.randomID = func() (int64, error) { return 999, nil }
	second.claimID = func() (string, error) { return "owner-restart-claim", nil }
	second.SetEnabled(true)
	reprepared, handled, err := second.PrepareOwnerReply(ctx, IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 501, ReplyToMessageID: 500,
	})
	if err != nil || !handled {
		t.Fatalf("PrepareOwnerReply after restart handled=%v err=%v", handled, err)
	}

	if err := second.ExecuteOwner(ctx, reprepared, transport); err != nil {
		t.Fatalf("ExecuteOwner after restart error=%v", err)
	}
	if transport.calls != 2 ||
		transport.requests[0].RandomID != 888 ||
		transport.requests[1].RandomID != 888 {
		t.Fatalf("owner restart changed logical Telegram identity: %+v", transport.requests)
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryOwnerToVisitor, SourceChatID: 7, SourceMessageID: 501,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetMessageID != 601 ||
		delivery.ClaimID != "" || delivery.Attempts != 2 {
		t.Fatalf("recovered owner delivery=%+v", delivery)
	}
}

func TestP6HCompletedVisitorDeliveryHealsMappingAfterRestartWithoutResend(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{
		Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8,
	})
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	now := base
	failing := &p6hMappingFailRepository{Repository: sqliteRepo, failNext: true}

	first := NewService(failing, 7)
	first.now = func() time.Time { return now }
	first.randomID = func() (int64, error) { return 777, nil }
	first.claimID = func() (string, error) { return "claim-a", nil }
	first.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, first)
	transport := &visitorTransportStub{message: 501}

	if err := first.ExecuteVisitor(ctx, prepared, transport); err == nil {
		t.Fatal("first ExecuteVisitor unexpectedly finalized mapping")
	}
	delivery, err := sqliteRepo.GetDelivery(ctx, DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetMessageID != 501 {
		t.Fatalf("delivery was not committed before finalize failure: %+v", delivery)
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("mapping unexpectedly existed after simulated finalize crash: %v", err)
	}

	now = base.Add(time.Minute)
	second := NewService(sqliteRepo, 7)
	second.now = func() time.Time { return now }
	second.SetEnabled(true)
	reprepared := prepareVisitorForDelivery(t, second)
	if err := second.ExecuteVisitor(ctx, reprepared, transport); err != nil {
		t.Fatalf("completed delivery finalize recovery error=%v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("completed delivery was resent during finalize recovery: calls=%d", transport.calls)
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); err != nil {
		t.Fatalf("mapping not healed: %v", err)
	}
	if _, err := sqliteRepo.GetAudience(ctx, 42); err != nil {
		t.Fatalf("audience not healed: %v", err)
	}
}

func TestP6HCompletedVisitorDeliveryHealsAudienceAfterRestartWithoutResend(t *testing.T) {
	ctx := context.Background()
	sqliteRepo, _ := newTestRepository(t, Limits{
		Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8,
	})
	base := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	now := base
	failing := &p6hAudienceFailRepository{Repository: sqliteRepo, failNext: true}

	first := NewService(failing, 7)
	first.now = func() time.Time { return now }
	first.randomID = func() (int64, error) { return 777, nil }
	first.claimID = func() (string, error) { return "claim-a", nil }
	first.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, first)
	transport := &visitorTransportStub{message: 501}

	if err := first.ExecuteVisitor(ctx, prepared, transport); err == nil {
		t.Fatal("first ExecuteVisitor unexpectedly finalized audience")
	}
	if _, err := sqliteRepo.GetMapping(ctx, 7, 501); err != nil {
		t.Fatalf("mapping should precede audience finalize failure: %v", err)
	}
	if _, err := sqliteRepo.GetAudience(ctx, 42); !errors.Is(err, ErrAudienceNotFound) {
		t.Fatalf("audience unexpectedly existed after simulated finalize crash: %v", err)
	}

	now = base.Add(time.Minute)
	second := NewService(sqliteRepo, 7)
	second.now = func() time.Time { return now }
	second.SetEnabled(true)
	reprepared := prepareVisitorForDelivery(t, second)
	if err := second.ExecuteVisitor(ctx, reprepared, transport); err != nil {
		t.Fatalf("audience finalize recovery error=%v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("completed delivery was resent while healing audience: calls=%d", transport.calls)
	}
	member, err := sqliteRepo.GetAudience(ctx, 42)
	if err != nil || member.Sources&AudienceSourceRelay == 0 {
		t.Fatalf("audience not healed: member=%+v err=%v", member, err)
	}
	if !member.LastSeenAt.Equal(base) {
		t.Fatalf("recovery changed activity time: got=%v want=%v", member.LastSeenAt, base)
	}
}

type p6hCancelingVisitorTransport struct {
	cancel   context.CancelFunc
	calls    int
	requests []VisitorForward
}

func (t *p6hCancelingVisitorTransport) ForwardVisitor(
	_ context.Context,
	request VisitorForward,
) (int, error) {
	t.calls++
	t.requests = append(t.requests, request)
	if t.cancel != nil {
		t.cancel()
	}
	return 0, context.Canceled
}

func TestP6HForcedShutdownCanceledClaimRecoversAfterLeaseExpiry(t *testing.T) {
	sqliteRepo, _ := newTestRepository(t, Limits{
		Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8,
	})
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	now := base

	first := NewService(sqliteRepo, 7)
	first.now = func() time.Time { return now }
	first.randomID = func() (int64, error) { return 777, nil }
	first.claimID = func() (string, error) { return "shutdown-claim", nil }
	first.SetEnabled(true)
	prepared := prepareVisitorForDelivery(t, first)

	taskCtx, cancel := context.WithCancel(context.Background())
	transport := &p6hCancelingVisitorTransport{cancel: cancel}
	err := first.ExecuteVisitor(taskCtx, prepared, transport)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteVisitor(canceled transport) error=%v, want context canceled", err)
	}

	pending, getErr := sqliteRepo.GetDelivery(context.Background(), DeliveryKey{
		Direction: DeliveryVisitorToOwner, SourceChatID: 42, SourceMessageID: 11,
	})
	if getErr != nil {
		t.Fatal(getErr)
	}
	if pending.Completed() || pending.ClaimID != "shutdown-claim" ||
		pending.RandomID != 777 {
		t.Fatalf("canceled delivery state=%+v", pending)
	}

	// Forced cancellation can prevent best-effort ReleaseDelivery from using the
	// canceled task context. The durable lease is therefore the fallback fence.
	now = base.Add(DeliveryClaimTTL + time.Second)
	second := NewService(sqliteRepo, 7)
	second.now = func() time.Time { return now }
	second.randomID = func() (int64, error) { return 999, nil }
	second.claimID = func() (string, error) { return "restart-claim", nil }
	second.SetEnabled(true)
	reprepared := prepareVisitorForDelivery(t, second)
	healthy := &visitorTransportStub{message: 501}

	if err := second.ExecuteVisitor(context.Background(), reprepared, healthy); err != nil {
		t.Fatalf("ExecuteVisitor(restart after canceled claim) error=%v", err)
	}
	if healthy.calls != 1 || len(healthy.requests) != 1 ||
		healthy.requests[0].RandomID != 777 {
		t.Fatalf("restart did not reuse durable random id: %+v", healthy.requests)
	}
	recovered, getErr := sqliteRepo.GetDelivery(context.Background(), pending.DeliveryKey)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if !recovered.Completed() || recovered.ClaimID != "" ||
		recovered.TargetMessageID != 501 {
		t.Fatalf("recovered canceled delivery=%+v", recovered)
	}
}
