package telegram

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type pipelineTrackingIdempotencyRepo struct {
	countingIdempotencyRepository
	begins   atomic.Int32
	accepts  atomic.Int32
	releases atomic.Int32
	failWith error
}

func (r *pipelineTrackingIdempotencyRepo) BeginClaim(ctx context.Context, key, owner string, expiresAt, now time.Time) (bool, error) {
	r.begins.Add(1)
	if r.failWith != nil {
		return false, r.failWith
	}
	return r.countingIdempotencyRepository.BeginClaim(ctx, key, owner, expiresAt, now)
}

func (r *pipelineTrackingIdempotencyRepo) AcceptClaim(ctx context.Context, key, owner string, expiresAt time.Time) (bool, error) {
	r.accepts.Add(1)
	return r.countingIdempotencyRepository.AcceptClaim(ctx, key, owner, expiresAt)
}

func (r *pipelineTrackingIdempotencyRepo) ReleaseClaim(ctx context.Context, key, owner string) (bool, error) {
	r.releases.Add(1)
	return r.countingIdempotencyRepository.ReleaseClaim(ctx, key, owner)
}

func TestPipeline_InvocationAuthorizationBeforeDurableClaim(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "adminonly",
		Permission: core.PermissionOwner,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationSelfOnly},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	// Sender 300 is not owner (100)
	msg := &tg.Message{
		ID:      10,
		PeerID:  &tg.PeerChat{ChatID: 7},
		FromID:  &tg.PeerUser{UserID: 300},
		Message: ".adminonly",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if executed.Load() {
		t.Fatal("invocation-denied command was executed")
	}
	if got := repo.begins.Load(); got != 0 {
		t.Fatalf("durable idempotency BeginClaim called %d times for denied command, want 0", got)
	}
}

func TestPipeline_DecisionSuppressionPreventsDurableClaimAndCommand(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "allowed",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	configureDispatcherTasks(t, d)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	var decisionCalls atomic.Int32
	d.AddScopedCanonicalMessageHandlerWithRoutingAndState(
		PrioritySecurity,
		tasks.ScopeIdentity{Owner: "plugin:decision-suppress", Generation: 1},
		core.MessageHookRouting{Lane: core.MessageHookDecision},
		func(int64) bool { return true },
		func(_ context.Context, _ *core.MessageEnvelope) error {
			decisionCalls.Add(1)
			return core.ErrInterceptHandled
		},
	)

	msg := &tg.Message{
		ID:      11,
		PeerID:  &tg.PeerChat{ChatID: 7},
		FromID:  &tg.PeerUser{UserID: 100},
		Message: ".allowed",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if got := decisionCalls.Load(); got != 1 {
		t.Fatalf("decision hook calls=%d, want 1", got)
	}
	if executed.Load() {
		t.Fatal("suppressed command was executed")
	}
	if got := repo.begins.Load(); got != 0 {
		t.Fatalf("durable idempotency BeginClaim called %d times for suppressed command, want 0", got)
	}
}

func TestPipeline_SenderConsistency_MismatchedUserDropped(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	// Inconsistent sender in private dialog: PeerUser is 200, but FromID is 300
	msg := &tg.Message{
		ID:      12,
		PeerID:  &tg.PeerUser{UserID: 200},
		FromID:  &tg.PeerUser{UserID: 300},
		Message: ".ping",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if executed.Load() {
		t.Fatal("mismatched private sender command was executed")
	}
	if got := repo.begins.Load(); got != 0 {
		t.Fatalf("durable idempotency BeginClaim called %d times for mismatched private sender, want 0", got)
	}
}

func TestPipeline_SenderConsistency_OmittedPrivateFromIDAllowed(t *testing.T) {
	router := core.NewRouter(".")
	executed := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	configureDispatcherTasks(t, d)

	// Omitted FromID in private dialog: PeerUser is 200, FromID is nil
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			200: {ID: 200, AccessHash: 12345},
		},
	}
	msg := &tg.Message{
		ID:      13,
		PeerID:  &tg.PeerUser{UserID: 200},
		FromID:  nil,
		Message: ".ping",
	}

	if err := d.dispatch(context.Background(), entities, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("omitted private fromID was not executed")
	}
}

func TestPipeline_MissingStableIdentityFailsClosed(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	// Invalid message ID 0 (no stable identity)
	msg := &tg.Message{
		ID:      0,
		PeerID:  &tg.PeerChat{ChatID: 10},
		Message: ".ping",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if executed.Load() {
		t.Fatal("command without stable identity executed")
	}
	if got := repo.begins.Load(); got != 0 {
		t.Fatalf("durable idempotency BeginClaim called %d times for command without stable identity, want 0", got)
	}
}

func TestPipeline_UnresolvablePeerReleasesClaims(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	// Channel without AccessHash and without resolver entry
	msg := &tg.Message{
		ID:      15,
		PeerID:  &tg.PeerChannel{ChannelID: 9999},
		Message: ".ping",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if executed.Load() {
		t.Fatal("command with unresolvable peer executed")
	}
	if got := repo.begins.Load(); got != 1 {
		t.Fatalf("BeginClaim called %d times, want 1", got)
	}
	if got := repo.releases.Load(); got != 1 {
		t.Fatalf("ReleaseClaim called %d times, want 1", got)
	}

	// Verify ingress dedupe claim was also released so retry is allowed
	_, accepted := d.ingressDedupe.Begin(msg, time.Now())
	if !accepted {
		t.Fatal("ingress claim was not released on peer resolution failure; retry was blocked")
	}
}

func TestPipeline_DurableStorageErrorFailsClosedAndReleasesIngressClaim(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{
		failWith: errors.New("sqlite disk I/O error"),
	}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	msg := &tg.Message{
		ID:      16,
		PeerID:  &tg.PeerChat{ChatID: 10},
		Message: ".ping",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if executed.Load() {
		t.Fatal("command executed despite storage failure")
	}

	// Ingress claim should be released on storage error
	_, accepted := d.ingressDedupe.Begin(msg, time.Now())
	if !accepted {
		t.Fatal("ingress claim was not released on storage error; retry was blocked")
	}
}

func TestPipeline_SecurityDecisionFailureFailsClosed(t *testing.T) {
	router := core.NewRouter(".")
	var executed atomic.Bool
	if err := router.Register(core.Command{
		Name:       "ping",
		Permission: core.PermissionEveryone,
		Invocation: core.InvocationPolicy{Userbot: core.InvocationAnyone},
		Handler: func(*core.Context) error {
			executed.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}

	repo := &pipelineTrackingIdempotencyRepo{}
	d := NewDispatcher(router, core.NewPermissions(100, nil), nil, zap.NewNop())
	d.SetSelfID(100)
	d.SetIdempotency(idempotency.NewManager(time.Minute, repo))

	// Register security decision hook that fails closed when tasks client is nil
	d.AddScopedCanonicalMessageHandlerWithRoutingAndState(
		PrioritySecurity,
		tasks.ScopeIdentity{Owner: "plugin:security-closed", Generation: 1},
		core.MessageHookRouting{Lane: core.MessageHookDecision},
		func(int64) bool { return true },
		func(_ context.Context, _ *core.MessageEnvelope) error {
			return nil
		},
	)

	// Since d.taskClient is nil, security decision fails closed
	msg := &tg.Message{
		ID:      17,
		PeerID:  &tg.PeerChat{ChatID: 10},
		Message: ".ping",
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, msg); err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if executed.Load() {
		t.Fatal("command executed even though security decision failed closed")
	}
	if got := repo.begins.Load(); got != 0 {
		t.Fatalf("durable BeginClaim called %d times on security fail-closed, want 0", got)
	}
}
