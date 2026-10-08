package userlog_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/userlog"
	"go.uber.org/zap"
)

type a6C2FailingSettings struct {
	userlog.Repository
	failKey string
	failure error
}

func (r *a6C2FailingSettings) SetUserLogSetting(ctx context.Context, key, value string) error {
	if key == r.failKey {
		return r.failure
	}
	return r.Repository.SetUserLogSetting(ctx, key, value)
}

func TestA6C2UserLogClearFailureDoesNotSilentlyReenableAfterRestart(t *testing.T) {
	for _, failedKey := range []string{userlog.SettingLogChatID, userlog.SettingLogDestination} {
		t.Run(failedKey, func(t *testing.T) {
			db := setupTestDB(t)
			repo := userlog.NewSQLiteRepository(db)
			injected := errors.New("sqlite write failed")
			failing := &a6C2FailingSettings{Repository: repo, failure: injected}
			svc := userlog.NewService(failing, &mockTelegram{}, zap.NewNop())
			ctx := context.Background()
			dest := userlog.LogDestination{Type: userlog.LogDestinationChannel, ID: 9001, AccessHash: 444}
			if err := svc.SetDestination(ctx, dest); err != nil {
				t.Fatal(err)
			}
			failing.failKey = failedKey
			if err := svc.ClearDestination(ctx); !errors.Is(err, injected) {
				t.Fatalf("failed clear reported success: %v", err)
			}
			// The prior structured destination must survive in cache AND disk.
			if got, err := svc.GetDestination(ctx); err != nil || got == nil || got.ID != dest.ID {
				t.Fatalf("failed clear invalidated live destination: %v (%v)", got, err)
			}
			restarted := userlog.NewService(repo, &mockTelegram{}, zap.NewNop())
			got, err := restarted.GetDestination(ctx)
			if err != nil || got == nil || got.ID != dest.ID || got.AccessHash != dest.AccessHash {
				t.Fatalf("failed clear silently changed restart destination: %v (%v)", got, err)
			}
			failing.failKey = ""
			if err := svc.ClearDestination(ctx); err != nil {
				t.Fatal(err)
			}
			afterRestart := userlog.NewService(repo, &mockTelegram{}, zap.NewNop())
			got, err = afterRestart.GetDestination(ctx)
			if err != nil || got != nil {
				t.Fatalf("cleared target resurrected after restart: %v (%v)", got, err)
			}
			if svc.IsLogDestinationRef(core.PeerRef{Kind: core.PeerKindChannel, ID: dest.ID}) {
				t.Fatal("cleared destination still cached")
			}
		})
	}
}

func TestA6C2UserLogCategoryTogglesAndDestinationValidation(t *testing.T) {
	db := setupTestDB(t)
	repo := userlog.NewSQLiteRepository(db)
	transport := &mockTelegram{}
	svc := userlog.NewService(repo, transport, zap.NewNop())
	ctx := context.Background()
	if err := svc.SetDestination(ctx, userlog.LogDestination{Type: "invalid", ID: 42}); err == nil {
		t.Fatal("unknown destination peer type was accepted")
	}
	if err := svc.SetDestination(ctx, userlog.LogDestination{Type: userlog.LogDestinationChannel, ID: 0}); err == nil {
		t.Fatal("zero channel ID was accepted")
	}
	if err := svc.SetDestination(ctx, userlog.LogDestination{Type: userlog.LogDestinationChat, ID: 777}); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{
		userlog.SettingPMsEnable,
		userlog.SettingTagsEnable,
		userlog.SettingActionsEnable,
	} {
		if err := svc.SetFeatureEnabled(ctx, category, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.LogPM(ctx, "Alice", 500, "private-message"); err != nil {
		t.Fatal(err)
	}
	if err := svc.LogMention(ctx, "Group", "Bob", 501, "@owner"); err != nil {
		t.Fatal(err)
	}
	if err := svc.LogAction(ctx, "ban", 502, "reason"); err != nil {
		t.Fatal(err)
	}
	if transport.attempts != 0 {
		t.Fatalf("disabled categories sent %d Telegram RPCs", transport.attempts)
	}
	for _, category := range []string{
		userlog.SettingPMsEnable,
		userlog.SettingTagsEnable,
		userlog.SettingActionsEnable,
	} {
		if err := svc.SetFeatureEnabled(ctx, category, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.LogPM(ctx, "Alice", 500, "private-message"); err != nil {
		t.Fatal(err)
	}
	if err := svc.LogMention(ctx, "Group", "Bob", 501, "@owner"); err != nil {
		t.Fatal(err)
	}
	if err := svc.LogAction(ctx, "ban", 502, "reason"); err != nil {
		t.Fatal(err)
	}
	if transport.attempts != 3 {
		t.Fatalf("enabled categories delivered %d, expected three", transport.attempts)
	}
	if _, ok := transport.lastPeer.(*tg.InputPeerChat); !ok {
		t.Fatalf("wrong chat delivery peer type: %T", transport.lastPeer)
	}
	const channelID int64 = 998877
	if err := svc.SetDestination(ctx, userlog.LogDestination{
		Type: userlog.LogDestinationChannel, ID: channelID, AccessHash: 555,
	}); err != nil {
		t.Fatal(err)
	}
	if svc.IsLogDestinationRef(core.PeerRef{Kind: core.PeerKindChat, ID: 777}) {
		t.Fatal("old destination remained active after switch")
	}
	if !svc.IsLogDestinationRef(core.PeerRef{Kind: core.PeerKindChannel, ID: channelID}) {
		t.Fatal("new destination was not recognized")
	}
	if err := svc.LogPM(ctx, "Alice", 500, "another"); err != nil {
		t.Fatal(err)
	}
	channel, ok := transport.lastPeer.(*tg.InputPeerChannel)
	if !ok || channel.ChannelID != channelID || channel.AccessHash != 555 {
		t.Fatalf("delivery was not routed to the new channel: %v", transport.lastPeer)
	}
	fresh := userlog.NewService(repo, &mockTelegram{}, zap.NewNop())
	stored, err := fresh.GetDestination(ctx)
	if err != nil || stored == nil || stored.ID != channelID || stored.AccessHash != 555 {
		t.Fatalf("persisted channel coordinates lost: %v (%v)", stored, err)
	}
	if strings.Contains(transport.sentText, "private-message") {
		t.Fatal("outdated private text reused across deliveries")
	}
}
