package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/tasks"
)

type countingRelayIngressService struct {
	base     *pmrelay.Service
	executes int
}

func (s *countingRelayIngressService) PrepareVisitor(ctx context.Context, message pmrelay.IngressMessage) (pmrelay.PreparedIngress, bool, error) {
	return s.base.PrepareVisitor(ctx, message)
}

func (s *countingRelayIngressService) PrepareOwnerReply(ctx context.Context, message pmrelay.IngressMessage) (pmrelay.PreparedIngress, bool, error) {
	return s.base.PrepareOwnerReply(ctx, message)
}

func (s *countingRelayIngressService) RevalidatePrepared(ctx context.Context, prepared pmrelay.PreparedIngress) error {
	s.executes++
	return s.base.RevalidatePrepared(ctx, prepared)
}

type relayAdmissionTaskClient struct {
	spec      tasks.WorkSpec
	calls     int
	submitErr error
	run       bool
}

func (c *relayAdmissionTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.calls++
	c.spec = spec
	if c.submitErr != nil {
		return nil, c.submitErr
	}
	if c.run && spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (*relayAdmissionTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*relayAdmissionTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*relayAdmissionTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func newRelayIngressService(t *testing.T) *pmrelay.Service {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	service := pmrelay.NewService(pmrelay.NewSQLiteRepository(db), 7)
	service.SetEnabled(true)
	return service
}

func TestRelayIngressAdmissionRejectionNeverExecutesPreparedWork(t *testing.T) {
	service := &countingRelayIngressService{base: newRelayIngressService(t)}
	admissionErr := errors.New("interactive queue saturated")
	taskClient := &relayAdmissionTaskClient{submitErr: admissionErr}
	ingress := NewRelayIngress(service, taskClient)

	handled, err := ingress.tryVisitor(context.Background(), pmrelay.IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if !handled || err == nil || !strings.Contains(err.Error(), admissionErr.Error()) {
		t.Fatalf("tryVisitor() handled=%v err=%v", handled, err)
	}
	if taskClient.calls != 1 {
		t.Fatalf("TaskEngine submissions=%d, want 1", taskClient.calls)
	}
	if service.executes != 0 {
		t.Fatalf("RevalidatePrepared calls=%d after admission rejection, want 0", service.executes)
	}
}

func TestRelayIngressCarriesPerVisitorAdmissionAndThreadOrdering(t *testing.T) {
	service := &countingRelayIngressService{base: newRelayIngressService(t)}
	taskClient := &relayAdmissionTaskClient{run: true}
	ingress := NewRelayIngress(service, taskClient)

	handled, err := ingress.tryVisitor(context.Background(), pmrelay.IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if err != nil || !handled {
		t.Fatalf("tryVisitor() handled=%v err=%v", handled, err)
	}
	if service.executes != 1 {
		t.Fatalf("RevalidatePrepared calls=%d, want 1", service.executes)
	}
	spec := taskClient.spec
	if spec.Scope != relayScope {
		t.Fatalf("relay scope=%+v, want %+v", spec.Scope, relayScope)
	}
	if spec.QuotaOwner != tasks.OwnerID("pmrelay:visitor:42") {
		t.Fatalf("quota owner=%q", spec.QuotaOwner)
	}
	if spec.OrderingKey != "pmrelay:thread:42" {
		t.Fatalf("ordering key=%q", spec.OrderingKey)
	}
	if spec.Pool != tasks.PoolID("interactive") || spec.Class != tasks.PriorityInteractive {
		t.Fatalf("pool=%q class=%q", spec.Pool, spec.Class)
	}
	if spec.ExecutionTimeout != relayExecutionTimeout {
		t.Fatalf("execution timeout=%s, want %s", spec.ExecutionTimeout, relayExecutionTimeout)
	}
	if len(spec.Resources) != 0 {
		t.Fatalf("P6-B relay unexpectedly reserves resources: %+v", spec.Resources)
	}
	if got := string(spec.ID); got != "asst:relay:visitor_to_owner:42:11:1" {
		t.Fatalf("task id=%q", got)
	}
}

func TestRelayIngressOwnerAndVisitorDirectionsShareThreadOrdering(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	now := time.Now().UTC()
	if _, err := repo.EnsureMapping(ctx, pmrelay.Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	serviceBase := pmrelay.NewService(repo, 7)
	serviceBase.SetEnabled(true)
	service := &countingRelayIngressService{base: serviceBase}
	taskClient := &relayAdmissionTaskClient{run: true}
	ingress := NewRelayIngress(service, taskClient)

	handled, err := ingress.tryOwnerReply(ctx, pmrelay.IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if err != nil || !handled {
		t.Fatalf("tryOwnerReply() handled=%v err=%v", handled, err)
	}
	if taskClient.spec.QuotaOwner != tasks.OwnerID("pmrelay:owner-reply:42") ||
		taskClient.spec.OrderingKey != "pmrelay:thread:42" {
		t.Fatalf("owner reply admission quota=%q ordering=%q", taskClient.spec.QuotaOwner, taskClient.spec.OrderingKey)
	}
	if got := string(taskClient.spec.ID); got != "asst:relay:owner_to_visitor:7:101:1" {
		t.Fatalf("owner reply task id=%q", got)
	}
}

func TestRelayIngressTaskIdentityDoesNotOwnDeliveryIdempotency(t *testing.T) {
	service := &countingRelayIngressService{base: newRelayIngressService(t)}
	taskClient := &relayAdmissionTaskClient{}
	ingress := NewRelayIngress(service, taskClient)
	message := pmrelay.IngressMessage{SenderID: 42, ChatID: 42, MessageID: 11}

	if handled, err := ingress.tryVisitor(context.Background(), message); err != nil || !handled {
		t.Fatalf("first tryVisitor() handled=%v err=%v", handled, err)
	}
	firstID := taskClient.spec.ID
	if handled, err := ingress.tryVisitor(context.Background(), message); err != nil || !handled {
		t.Fatalf("second tryVisitor() handled=%v err=%v", handled, err)
	}
	secondID := taskClient.spec.ID
	if firstID == secondID {
		t.Fatalf("duplicate source reused TaskEngine id %q; durable delivery must own idempotency", firstID)
	}
	if taskClient.spec.OrderingKey != "pmrelay:thread:42" {
		t.Fatalf("duplicate source changed thread ordering key=%q", taskClient.spec.OrderingKey)
	}
}


type relayVisitorTransportStub struct {
	calls   int
	request pmrelay.VisitorForward
	message int
	err     error
}

func (t *relayVisitorTransportStub) ForwardVisitor(_ context.Context, request pmrelay.VisitorForward) (int, error) {
	t.calls++
	t.request = request
	if t.err != nil {
		return 0, t.err
	}
	return t.message, nil
}

func TestRelayIngressExecutesVisitorDeliveryOnlyInsideAdmittedHandler(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	taskClient := &relayAdmissionTaskClient{run: true}
	transport := &relayVisitorTransportStub{message: 501}
	ingress := NewRelayIngress(service, taskClient, transport)

	handled, err := ingress.tryVisitor(ctx, pmrelay.IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if err != nil || !handled {
		t.Fatalf("tryVisitor() handled=%v err=%v", handled, err)
	}
	if transport.calls != 1 {
		t.Fatalf("visitor transport calls=%d, want 1", transport.calls)
	}
	if transport.request.SourceChatID != 42 ||
		transport.request.SourceMessageID != 11 ||
		transport.request.TargetChatID != 7 ||
		transport.request.RandomID == 0 {
		t.Fatalf("visitor transport request=%+v", transport.request)
	}
	if _, err := repo.GetMapping(ctx, 7, 501); err != nil {
		t.Fatalf("durable relay mapping missing: %v", err)
	}
	member, err := repo.GetAudience(ctx, 42)
	if err != nil {
		t.Fatalf("relay audience missing: %v", err)
	}
	if member.Sources&pmrelay.AudienceSourceRelay == 0 {
		t.Fatalf("relay audience sources=%d", member.Sources)
	}
}

func TestRelayIngressAdmissionRejectionCannotTouchVisitorDeliveryState(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	taskClient := &relayAdmissionTaskClient{submitErr: errors.New("queue full")}
	transport := &relayVisitorTransportStub{message: 501}
	ingress := NewRelayIngress(service, taskClient, transport)

	handled, err := ingress.tryVisitor(ctx, pmrelay.IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if !handled || err == nil {
		t.Fatalf("tryVisitor() handled=%v err=%v", handled, err)
	}
	if transport.calls != 0 {
		t.Fatalf("transport ran before admission: calls=%d", transport.calls)
	}
	if count, err := repo.CountDeliveries(ctx); err != nil || count != 0 {
		t.Fatalf("deliveries after rejected admission=%d err=%v", count, err)
	}
	if count, err := repo.CountMappings(ctx); err != nil || count != 0 {
		t.Fatalf("mappings after rejected admission=%d err=%v", count, err)
	}
	if count, err := repo.CountAudience(ctx); err != nil || count != 0 {
		t.Fatalf("audience after rejected admission=%d err=%v", count, err)
	}
}


func TestRelayIngressP6CClaimsMappedOwnerReplyButFailsClosedBeforeP6D(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	now := time.Now().UTC()
	if _, err := repo.EnsureMapping(ctx, pmrelay.Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	taskClient := &relayAdmissionTaskClient{run: true}
	transport := &relayVisitorTransportStub{message: 501}
	ingress := NewRelayIngress(service, taskClient, transport)

	handled, err := ingress.tryOwnerReply(ctx, pmrelay.IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if !handled || !errors.Is(err, pmrelay.ErrUnsupportedDelivery) {
		t.Fatalf("tryOwnerReply() handled=%v err=%v, want fail-closed unsupported delivery", handled, err)
	}
	if transport.calls != 0 {
		t.Fatalf("visitor transport used for owner reply: calls=%d", transport.calls)
	}
}


type relayBidirectionalTransportStub struct {
	visitorCalls int
	visitorReq   pmrelay.VisitorForward
	visitorMsg   int
	visitorErr   error

	ownerCalls int
	ownerReq   pmrelay.OwnerSend
	ownerMsg   int
	ownerErr   error
}

func (t *relayBidirectionalTransportStub) ForwardVisitor(_ context.Context, request pmrelay.VisitorForward) (int, error) {
	t.visitorCalls++
	t.visitorReq = request
	if t.visitorErr != nil {
		return 0, t.visitorErr
	}
	return t.visitorMsg, nil
}

func (t *relayBidirectionalTransportStub) SendOwnerReply(_ context.Context, request pmrelay.OwnerSend) (int, error) {
	t.ownerCalls++
	t.ownerReq = request
	if t.ownerErr != nil {
		return 0, t.ownerErr
	}
	return t.ownerMsg, nil
}

func TestRelayIngressExecutesMappedOwnerReplyThroughP6DTransport(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	now := time.Now().UTC()
	if _, err := repo.EnsureMapping(ctx, pmrelay.Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	taskClient := &relayAdmissionTaskClient{run: true}
	transport := &relayBidirectionalTransportStub{visitorMsg: 501, ownerMsg: 601}
	ingress := NewRelayIngress(service, taskClient, transport)

	handled, err := ingress.tryOwnerReply(ctx, pmrelay.IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if err != nil || !handled {
		t.Fatalf("tryOwnerReply() handled=%v err=%v", handled, err)
	}
	if transport.ownerCalls != 1 {
		t.Fatalf("owner transport calls=%d, want 1", transport.ownerCalls)
	}
	if transport.visitorCalls != 0 {
		t.Fatalf("owner reply incorrectly used visitor forward transport: calls=%d", transport.visitorCalls)
	}
	if transport.ownerReq.SourceChatID != 7 ||
		transport.ownerReq.SourceMessageID != 101 ||
		transport.ownerReq.TargetChatID != 42 ||
		transport.ownerReq.RandomID == 0 {
		t.Fatalf("owner transport request=%+v", transport.ownerReq)
	}

	delivery, err := repo.GetDelivery(ctx, pmrelay.DeliveryKey{
		Direction:       pmrelay.DeliveryOwnerToVisitor,
		SourceChatID:    7,
		SourceMessageID: 101,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Completed() || delivery.TargetMessageID != 601 {
		t.Fatalf("owner delivery=%+v", delivery)
	}
}

func TestRelayIngressOwnerAdmissionRejectionHasNoDeliverySideEffect(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	now := time.Now().UTC()
	if _, err := repo.EnsureMapping(ctx, pmrelay.Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	taskClient := &relayAdmissionTaskClient{submitErr: errors.New("queue full")}
	transport := &relayBidirectionalTransportStub{visitorMsg: 501, ownerMsg: 601}
	ingress := NewRelayIngress(service, taskClient, transport)

	handled, err := ingress.tryOwnerReply(ctx, pmrelay.IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if !handled || err == nil {
		t.Fatalf("tryOwnerReply() handled=%v err=%v", handled, err)
	}
	if transport.ownerCalls != 0 || transport.visitorCalls != 0 {
		t.Fatalf("transport ran before owner admission: owner=%d visitor=%d", transport.ownerCalls, transport.visitorCalls)
	}
	if count, err := repo.CountDeliveries(ctx); err != nil || count != 0 {
		t.Fatalf("deliveries after rejected owner admission=%d err=%v", count, err)
	}
}


func TestRelayIngressBlockedVisitorNeverReachesTaskEngine(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	if _, err := service.BlockVisitor(ctx, 42, "spam"); err != nil {
		t.Fatal(err)
	}
	taskClient := &relayAdmissionTaskClient{}
	ingress := NewRelayIngress(service, taskClient)

	handled, err := ingress.tryVisitor(ctx, pmrelay.IngressMessage{
		SenderID: 42, ChatID: 42, MessageID: 11,
	})
	if err != nil || handled {
		t.Fatalf("tryVisitor(blocked) handled=%v err=%v, want false nil", handled, err)
	}
	if taskClient.calls != 0 {
		t.Fatalf("blocked visitor reached TaskEngine %d time(s)", taskClient.calls)
	}
	if count, err := repo.CountDeliveries(ctx); err != nil || count != 0 {
		t.Fatalf("blocked visitor created deliveries=%d err=%v", count, err)
	}
}

func TestRelayIngressBlockedMappedOwnerReplyFailsBeforeTaskEngine(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepository(db)
	now := time.Now().UTC()
	if _, err := repo.EnsureMapping(ctx, pmrelay.Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	if _, err := service.BlockVisitor(ctx, 42, "abuse"); err != nil {
		t.Fatal(err)
	}
	taskClient := &relayAdmissionTaskClient{}
	ingress := NewRelayIngress(service, taskClient)

	handled, err := ingress.tryOwnerReply(ctx, pmrelay.IngressMessage{
		SenderID: 7, ChatID: 7, MessageID: 101, ReplyToMessageID: 100,
	})
	if !handled || !errors.Is(err, pmrelay.ErrVisitorBlocked) {
		t.Fatalf("tryOwnerReply(blocked) handled=%v err=%v, want handled ErrVisitorBlocked", handled, err)
	}
	if taskClient.calls != 0 {
		t.Fatalf("blocked owner reply reached TaskEngine %d time(s)", taskClient.calls)
	}
	if count, err := repo.CountDeliveries(ctx); err != nil || count != 0 {
		t.Fatalf("blocked owner reply created deliveries=%d err=%v", count, err)
	}
}
