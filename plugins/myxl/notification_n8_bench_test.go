package myxl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func makeBenchDests(n int, enabled bool) []NotificationDestination {
	out := make([]NotificationDestination, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, NotificationDestination{
			ID:         fmt.Sprintf("bot_pm:%d", 900000+i),
			Type:       DestBotPM,
			PeerID:     int64(900000 + i),
			AccessHash: int64(800000 + i),
			Label:      fmt.Sprintf("Bot%d", i),
			Enabled:    enabled,
		})
	}
	return out
}

func BenchmarkN8_Delivery(b *testing.B) {
	ctx := context.Background()
	okSvc := &routingTestMessageServicer{}
	failAllSvc := &routingTestMessageServicer{
		errByPeerFn: func(tg.InputPeerClass) error { return errors.New("CHAT_WRITE_FORBIDDEN") },
	}
	failOneSvc := &routingTestMessageServicer{
		errByPeerFn: func(p tg.InputPeerClass) error {
			if u, ok := p.(*tg.InputPeerUser); ok && u.UserID == 900000 {
				return errors.New("USER_IS_BLOCKED")
			}
			return nil
		},
	}

	cases := []struct {
		name  string
		dests []NotificationDestination
		svc   core.MessageServicer
	}{
		{"1_dest_ok", makeBenchDests(1, true), okSvc},
		{"3_dest_ok", makeBenchDests(3, true), okSvc},
		{"5_dest_ok", makeBenchDests(5, true), okSvc},
		{"5_dest_1_fail", makeBenchDests(5, true), failOneSvc},
		{"5_dest_all_fail", makeBenchDests(5, true), failAllSvc},
		{"1_dest_invalid", []NotificationDestination{{ID: "bad", Type: DestChannel, Enabled: true}}, okSvc},
		{"5_dest_disabled", makeBenchDests(5, false), okSvc},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			cfg := DefaultTokenMaintenanceConfig()
			cfg.Destinations = tc.dests
			eff := effectiveNotificationDestinations(cfg)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = deliverMaintenanceNotifications(ctx, tc.svc, 1001, nil, nil, EventFailure, eff, "msg", 15*time.Second, 5*time.Second)
			}
		})
	}
}

func BenchmarkN8_PersistenceAndService(b *testing.B) {
	ctx := context.Background()
	dbPath := filepath.Join(b.TempDir(), "n8_bench.db")
	tFake := &testing.T{}
	_, repo, _, _, _, p := initMyXLPluginWithJobs(tFake, dbPath, "")

	sendSvc := &routingTestMessageServicer{}
	p.SetOwnerNotifier(1001, func() core.MessageServicer { return sendSvc })
	svc := p.maintenanceService()

	cfg := DefaultTokenMaintenanceConfig()
	cfg.Destinations = makeBenchDests(3, true)
	if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
		b.Fatal(err)
	}

	b.Run("repo_get_config", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := repo.GetTokenMaintenanceConfig(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("repo_save_config", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := repo.SaveTokenMaintenanceConfig(ctx, &cfg); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("repo_list_history", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := repo.ListTokenMaintenanceRuns(ctx, 10); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("svc_get_status", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := svc.GetStatus(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("svc_set_notify_event", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := svc.SetNotifyEvent(ctx, EventCompletion, i%2 == 0); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("svc_toggle_dest_enabled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := svc.SetNotificationDestinationEnabled(ctx, "bot_pm:900000", i%2 == 0); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("svc_test_send", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := svc.TestNotificationDestination(ctx, "bot_pm:900000"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
