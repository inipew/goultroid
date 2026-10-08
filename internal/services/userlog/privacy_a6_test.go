package userlog_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/services/userlog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestA6UserLogDeliveryErrorsNeverLeakToHealthOrStructuredLogs(t *testing.T) {
	const secret = "telegram-password-private-message-body"
	db := setupTestDB(t)
	repo := userlog.NewSQLiteRepository(db)
	tg := &mockTelegram{sendErr: errors.New("telegram RPC: " + secret)}
	core, observed := observer.New(zap.DebugLevel)
	svc := userlog.NewService(repo, tg, zap.New(core))
	ctx := context.Background()
	if err := svc.SetLogChat(ctx, 777); err != nil {
		t.Fatal(err)
	}
	if err := svc.LogPM(ctx, "Sender", 500, "intentional owner-visible message"); err == nil || !strings.Contains(err.Error(), secret) {
		t.Fatalf("caller did not receive original RPC error: %v", err)
	}
	stats := svc.Stats(ctx)
	if stats.FailedCount != 1 || stats.ConsecutiveFailures != 1 || stats.LastError != "delivery_failed" {
		t.Fatalf("wrong safe health diagnostics: %+v", stats)
	}
	for _, item := range observed.All() {
		if strings.Contains(fmt.Sprint(item.Message, item.ContextMap()), secret) {
			t.Fatalf("sensitive RPC error leaked into structured logs: %v", item.ContextMap())
		}
	}
	tg.sendErr = context.DeadlineExceeded
	if err := svc.LogPM(ctx, "Sender", 500, "another message"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost deadline error to caller: %v", err)
	}
	if got := svc.Stats(ctx).LastError; got != "deadline_exceeded" {
		t.Fatalf("missing safe deadline category: %q", got)
	}
}
