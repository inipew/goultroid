package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	assistantdeeplink "github.com/inipew/goultroid/internal/assistant/deeplink"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type startDeepLinkProvider struct {
	scope     tasks.ScopeIdentity
	resources []tasks.ResourceRequirement
	executes  int
}

func (p *startDeepLinkProvider) Prepare(context.Context, string, int64) (assistantdeeplink.PreparedTarget, error) {
	return assistantdeeplink.PreparedTarget{
		Scope:     p.scope,
		Resources: append([]tasks.ResourceRequirement(nil), p.resources...),
		State:     "prepared",
	}, nil
}

func (p *startDeepLinkProvider) Execute(_ context.Context, _ assistantdeeplink.PreparedTarget, delivery assistantdeeplink.Delivery) error {
	p.executes++
	if delivery.SendText != nil {
		return delivery.SendText("deep-link delivered")
	}
	return nil
}

func newStartDeepLinkRouter(t *testing.T, provider *startDeepLinkProvider) *assistantdeeplink.Router {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, assistantdeeplink.MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error=%v", err)
	}
	router := assistantdeeplink.NewRouter(assistantdeeplink.NewSQLiteRepository(db))
	if _, err := router.Register("test", provider); err != nil {
		t.Fatal(err)
	}
	return router
}

type immediateDeepLinkTicket struct {
	id     tasks.TaskID
	result tasks.TaskResult
	done   chan struct{}
}

func (t *immediateDeepLinkTicket) TaskID() tasks.TaskID { return t.id }
func (t *immediateDeepLinkTicket) State() tasks.TaskState {
	if t.result.IsSuccess() {
		return tasks.StateCompleted
	}
	return tasks.StateFailed
}
func (t *immediateDeepLinkTicket) Done() <-chan struct{} { return t.done }
func (t *immediateDeepLinkTicket) Result() (tasks.TaskResult, bool) {
	return t.result, true
}
func (t *immediateDeepLinkTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return t.result, nil
}

type immediateDeepLinkTasks struct {
	spec      tasks.WorkSpec
	submits   int
	rejectErr error
}

func (c *immediateDeepLinkTasks) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.spec = spec
	c.submits++
	if c.rejectErr != nil {
		return nil, c.rejectErr
	}
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted, Cause: tasks.CauseNone}
	if err := spec.Handler(ctx); err != nil {
		result.Outcome = tasks.OutcomeFailed
		result.Failure.Message = err.Error()
	}
	done := make(chan struct{})
	close(done)
	return &immediateDeepLinkTicket{id: spec.ID, result: result, done: done}, nil
}
func (*immediateDeepLinkTasks) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*immediateDeepLinkTasks) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*immediateDeepLinkTasks) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestAssistantStartDeepLinkCarriesScopeAndMediaAdmission(t *testing.T) {
	provider := &startDeepLinkProvider{
		scope:     tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 8},
		resources: []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
	}
	router := newStartDeepLinkRouter(t, provider)
	token, err := router.Issue(context.Background(), assistantdeeplink.IssueRequest{
		Kind: "test", Payload: "opaque", ActorID: 7, SingleUse: true, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	taskClient := &immediateDeepLinkTasks{}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	audience, audienceRepo := newAssistantAudienceRegistry(t)
	client.SetAudienceRegistry(audience)
	client.SetDeepLinkRouter(router)
	client.SetTasks(taskClient)
	interaction := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx:         context.Background(),
		SenderID:    7,
		Peer:        &tg.InputPeerUser{UserID: 7},
		Args:        []string{token.ID},
		Interaction: interaction,
	}); err != nil {
		t.Fatalf("dispatchStart(deep link) error=%v", err)
	}
	if interaction.sent != "deep-link delivered" {
		t.Fatalf("deep-link delivery=%q", interaction.sent)
	}
	if provider.executes != 1 || taskClient.submits != 1 {
		t.Fatalf("executes/submits=%d/%d, want 1/1", provider.executes, taskClient.submits)
	}
	if taskClient.spec.Scope != provider.scope {
		t.Fatalf("task scope=%+v, want %+v", taskClient.spec.Scope, provider.scope)
	}
	if taskClient.spec.Pool != tasks.PoolID("general") ||
		taskClient.spec.Class != tasks.PriorityInteractive ||
		taskClient.spec.ExecutionTimeout != assistantDeepLinkExecutionTimeout {
		t.Fatalf("unexpected task admission: pool=%q class=%q timeout=%s",
			taskClient.spec.Pool, taskClient.spec.Class, taskClient.spec.ExecutionTimeout)
	}
	if len(taskClient.spec.Resources) != 1 ||
		taskClient.spec.Resources[0].Name != "media" ||
		taskClient.spec.Resources[0].Amount != 1 {
		t.Fatalf("task resources=%+v, want media:1", taskClient.spec.Resources)
	}
	member, err := audienceRepo.GetAudience(context.Background(), 7)
	if err != nil {
		t.Fatalf("deep-link audience missing: %v", err)
	}
	if member.Sources != pmrelay.AudienceSourceDeepLink {
		t.Fatalf("deep-link audience sources=%d, want deep_link", member.Sources)
	}
}

func TestAssistantStartDeepLinkUnauthorizedDoesNotBurnSingleUse(t *testing.T) {
	provider := &startDeepLinkProvider{scope: tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}}
	router := newStartDeepLinkRouter(t, provider)
	token, err := router.Issue(context.Background(), assistantdeeplink.IssueRequest{
		Kind: "test", Payload: "actor", ActorID: 7, SingleUse: true, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetDeepLinkRouter(router)
	client.SetTasks(&immediateDeepLinkTasks{})
	unauthorized := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 8,
		Peer: &tg.InputPeerUser{UserID: 8}, Args: []string{token.ID}, Interaction: unauthorized,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unauthorized.sent, "invalid, expired, or no longer available") {
		t.Fatalf("unauthorized response=%q", unauthorized.sent)
	}
	if provider.executes != 0 {
		t.Fatalf("unauthorized token executed provider %d times", provider.executes)
	}

	authorized := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{token.ID}, Interaction: authorized,
	}); err != nil {
		t.Fatal(err)
	}
	if authorized.sent != "deep-link delivered" || provider.executes != 1 {
		t.Fatalf("authorized retry delivery=%q executes=%d", authorized.sent, provider.executes)
	}
}

func TestAssistantStartDeepLinkAdmissionFailureDoesNotBurnSingleUse(t *testing.T) {
	provider := &startDeepLinkProvider{scope: tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}}
	router := newStartDeepLinkRouter(t, provider)
	token, err := router.Issue(context.Background(), assistantdeeplink.IssueRequest{
		Kind: "test", Payload: "admission", ActorID: 7, SingleUse: true, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetDeepLinkRouter(router)
	client.SetTasks(&immediateDeepLinkTasks{rejectErr: errors.New("admission rejected")})
	failed := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{token.ID}, Interaction: failed,
	}); err != nil {
		t.Fatal(err)
	}
	if provider.executes != 0 {
		t.Fatal("provider executed despite TaskEngine rejection")
	}

	client.SetTasks(&immediateDeepLinkTasks{})
	retry := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{token.ID}, Interaction: retry,
	}); err != nil {
		t.Fatal(err)
	}
	if retry.sent != "deep-link delivered" || provider.executes != 1 {
		t.Fatalf("retry delivery=%q executes=%d", retry.sent, provider.executes)
	}
}

func TestAssistantStartMalformedVersionedTokenNeverFallsThroughToHome(t *testing.T) {
	manager, client, _, _ := newShellEngine(t)
	defer manager.Shutdown()
	client.SetTasks(&immediateDeepLinkTasks{})
	interaction := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{"d1_bad"}, Interaction: interaction,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(interaction.sent, "invalid, expired, or no longer available") {
		t.Fatalf("malformed token response=%q", interaction.sent)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 0 {
		t.Fatalf("malformed token fell through to shell sessions=%d", got)
	}
}

func TestAssistantStartNonVersionedPayloadPreservesHomeFlow(t *testing.T) {
	manager, client, port, _ := newShellEngine(t)
	defer manager.Shutdown()
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{"legacy-payload"},
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(port.sent.Text, "GoUltroid Assistant") {
		t.Fatalf("non-versioned payload did not preserve home flow: %q", port.sent.Text)
	}
}

func TestAssistantStartDeepLinkRejectsGroupDelivery(t *testing.T) {
	provider := &startDeepLinkProvider{scope: tasks.ScopeIdentity{Owner: "plugin:test", Generation: 1}}
	router := newStartDeepLinkRouter(t, provider)
	token, err := router.Issue(context.Background(), assistantdeeplink.IssueRequest{
		Kind: "test", Payload: "private-only", ActorID: 7, SingleUse: true, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	taskClient := &immediateDeepLinkTasks{}
	client := NewAssistantClient(1, "hash", "token", zap.NewNop())
	client.SetDeepLinkRouter(router)
	client.SetTasks(taskClient)
	interaction := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerChat{ChatID: 99}, Args: []string{token.ID}, Interaction: interaction,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(interaction.sent, "invalid, expired, or no longer available") {
		t.Fatalf("group deep-link response=%q", interaction.sent)
	}
	if taskClient.submits != 0 || provider.executes != 0 {
		t.Fatalf("group deep-link reached admission/provider: submits=%d executes=%d", taskClient.submits, provider.executes)
	}

	// Rejection happens before durable claim; the same single-use token remains
	// valid in the intended private bot conversation.
	private := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{token.ID}, Interaction: private,
	}); err != nil {
		t.Fatal(err)
	}
	if private.sent != "deep-link delivered" || provider.executes != 1 {
		t.Fatalf("private retry delivery=%q executes=%d", private.sent, provider.executes)
	}
}

func TestAssistantStartUnsupportedDeepLinkVersionFailsClosed(t *testing.T) {
	manager, client, _, _ := newShellEngine(t)
	defer manager.Shutdown()
	client.SetTasks(&immediateDeepLinkTasks{})
	interaction := &publicStartInteraction{}
	if err := client.dispatchStart(&command.Context{
		Ctx: context.Background(), SenderID: 7,
		Peer: &tg.InputPeerUser{UserID: 7}, Args: []string{"d2_AAAAAAAAAAAAAAAAAAAAAA"}, Interaction: interaction,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(interaction.sent, "invalid, expired, or no longer available") {
		t.Fatalf("unsupported token version response=%q", interaction.sent)
	}
	if got := manager.InteractionRuntime().Stats().Sessions; got != 0 {
		t.Fatalf("unsupported token version fell through to shell sessions=%d", got)
	}
}
