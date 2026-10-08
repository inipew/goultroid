package pmpermit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	pmpermitSvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"go.uber.org/zap"
)

func TestPMPermitMissingAccessHashPreservesAuthoritativeApproval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		expires bool
		handled bool
	}{
		{name: "approved", status: pmpermitSvc.StatusApproved, handled: false},
		{name: "pending", status: pmpermitSvc.StatusPending, handled: true},
		{name: "blocked", status: pmpermitSvc.StatusBlocked, handled: true},
		{name: "expired approval", status: pmpermitSvc.StatusApproved, expires: true, handled: true},
		{name: "unknown", handled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			ctx := context.Background()
			repo := pmpermit.NewSQLiteRepository(db)
			userID := int64(2002)
			if tc.status != "" {
				var expiry *time.Time
				if tc.expires {
					past := time.Now().UTC().Add(-time.Minute)
					expiry = &past
				}
				if err := repo.SetPMStatus(ctx, userID, tc.status, "test setup", expiry); err != nil {
					t.Fatal(err)
				}
			}
			transport := &mockTelegram{}
			svc := pmpermitSvc.NewService(repo, transport, 1001, core.NewPermissions(1001, nil), zap.NewNop())
			plugin := pmpermit.New(svc)
			// A partial/min Telegram user has no usable access hash, and no resolver is available.
			msg := &tg.Message{ID: 19, PeerID: &tg.PeerUser{UserID: userID}, FromID: &tg.PeerUser{UserID: userID}, Message: "hello"}
			entities := tg.Entities{Users: map[int64]*tg.User{userID: {ID: userID}}}
			err := handleMessageEvent(plugin, ctx, entities, msg, false, "")
			if got := errors.Is(err, core.ErrInterceptHandled); got != tc.handled {
				t.Fatalf("intercepted=%v, want=%v (err=%v)", got, tc.handled, err)
			}
			if err != nil && !errors.Is(err, core.ErrInterceptHandled) {
				t.Fatalf("unexpected hook error: %v", err)
			}
			if transport.sentText != "" {
				t.Fatalf("sent warning without resolvable peer: %q", transport.sentText)
			}
			rec, err := repo.GetPMRecord(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			if rec != nil && rec.WarnCount != 0 {
				t.Fatalf("unresolvable peer changed warning count: %d", rec.WarnCount)
			}
			if tc.expires && (rec == nil || rec.Status != pmpermitSvc.StatusPending) {
				t.Fatalf("expired approval not reverted to pending: %+v", rec)
			}
		})
	}
}

func TestPMPermitApprovedMissingHashDoesNotTrustMismatchedSender(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := pmpermit.NewSQLiteRepository(db)
	if err := repo.SetPMStatus(ctx, 2002, pmpermitSvc.StatusApproved, "test setup", nil); err != nil {
		t.Fatal(err)
	}
	svc := pmpermitSvc.NewService(repo, &mockTelegram{}, 1001, core.NewPermissions(1001, nil), zap.NewNop())
	plugin := pmpermit.New(svc)
	message := &tg.Message{ID: 20, PeerID: &tg.PeerUser{UserID: 2002}, FromID: &tg.PeerUser{UserID: 3003}, Message: "spoofed"}
	if err := handleMessageEvent(plugin, ctx, tg.Entities{}, message, false, ""); !errors.Is(err, core.ErrInterceptHandled) {
		t.Fatalf("mismatched sender must be intercepted despite approved peer: %v", err)
	}
}

func TestPMPermitMissingHashRepositoryUnavailableFailsClosed(t *testing.T) {
	svc := pmpermitSvc.NewService(nil, &mockTelegram{}, 1001, core.NewPermissions(1001, nil), zap.NewNop())
	plugin := pmpermit.New(svc)
	msg := &tg.Message{ID: 21, PeerID: &tg.PeerUser{UserID: 2002}, FromID: &tg.PeerUser{UserID: 2002}}
	if err := handleMessageEvent(plugin, context.Background(), tg.Entities{}, msg, false, ""); !errors.Is(err, core.ErrInterceptHandled) {
		t.Fatalf("missing repository must not grant private message access: %v", err)
	}
}
