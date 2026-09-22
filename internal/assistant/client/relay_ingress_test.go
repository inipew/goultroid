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
