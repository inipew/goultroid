package userlog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

type a6C2UnavailableSettings struct {
	userlogSvc.Repository
}

func (*a6C2UnavailableSettings) GetUserLogSetting(context.Context, string) (string, error) {
	return "", errors.New("configuration database unavailable")
}

func TestA6C2UserLogRestartPrimesDestinationBeforeMessageAdmission(t *testing.T) {
	db := setupTestDB(t)
	repo := userlogSvc.NewSQLiteRepository(db)
	ctx := context.Background()
	config := userlogSvc.NewService(repo, &mockTelegram{}, zap.NewNop())
	if err := config.SetDestination(ctx, userlogSvc.LogDestination{
		Type: userlogSvc.LogDestinationChat, ID: 777,
	}); err != nil {
		t.Fatal(err)
	}
	msg := &core.MessageEnvelope{
		ChatID:     777,
		Chat:       core.Chat{ID: 777, Type: "group"},
		Peer:       core.PeerRef{Kind: core.PeerKindChat, ID: 777},
		SenderPeer: core.PeerRef{Kind: core.PeerKindUser, ID: 500},
		Sender:     core.User{ID: 500, FirstName: "Alice"},
		Text:       "@owner message in logging destination",
		Mentioned:  true,
	}
	if !msg.IsGroup() {
		t.Fatal("test must exercise an actual group mention, not an ignored chat kind")
	}
	for generation := 0; generation < 2; generation++ {
		transport := &mockTelegram{}
		service := userlogSvc.NewService(repo, transport, zap.NewNop())
		if service.IsLogDestinationRef(msg.Peer) {
			t.Fatal("test requires an initially cold destination cache")
		}
		p := userlog.New(service, 12345)
		scope := plugin.NewScope(ctx, "plugin:userlog")
		if err := p.InitScope(scope.Context(), scope); err != nil {
			t.Fatal(err)
		}
		if !service.IsLogDestinationRef(msg.Peer) {
			t.Fatal("destination not primed from SQLite before hook activation")
		}
		if err := p.HandleMessageEvent(ctx, msg); err != nil {
			t.Fatal(err)
		}
		if got := scope.ActiveGoroutines(); got != 0 {
			t.Fatalf("destination self-mention started %d worker(s)", got)
		}
		if got := transport.getSent(); got != "" {
			t.Fatalf("message sent into its own destination: %q", got)
		}
		stopCtx, cancel := context.WithTimeout(ctx, time.Second)
		if err := p.ShutdownContext(stopCtx); err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := scope.Close(stopCtx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
}

func TestA6C2UserLogFailsActivationWhenDestinationCannotBeLoaded(t *testing.T) {
	db := setupTestDB(t)
	repo := &a6C2UnavailableSettings{Repository: userlogSvc.NewSQLiteRepository(db)}
	p := userlog.New(userlogSvc.NewService(repo, &mockTelegram{}, zap.NewNop()), 12345)
	scope := plugin.NewScope(context.Background(), "plugin:userlog")
	if err := p.InitScope(scope.Context(), scope); err == nil {
		t.Fatal("plugin activated without determining the configured destination")
	}
	if got := scope.ActiveGoroutines(); got != 0 {
		t.Fatalf("unverified destination started %d workers", got)
	}
	if err := scope.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
