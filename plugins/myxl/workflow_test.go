package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/ui"
)

func TestMyXLWorkflowLoginAndAccountSwitch(t *testing.T) {
	p, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	ctx := context.Background()

	msisdn, err := p.requestLoginOTP(ctx, "081987654321")
	if err != nil {
		t.Fatalf("requestLoginOTP() error = %v", err)
	}
	if msisdn != "6281987654321" {
		t.Fatalf("normalized msisdn = %q", msisdn)
	}
	pending, err := repo.GetByMSISDN(ctx, msisdn)
	if err != nil || pending == nil || pending.SubscriberID != "SUB-TEST-1" {
		t.Fatalf("OTP placeholder account = %+v, err = %v", pending, err)
	}

	authenticated, err := p.completeLoginOTP(ctx, msisdn, "123456")
	if err != nil {
		t.Fatalf("completeLoginOTP() error = %v", err)
	}
	if !authenticated.IsActive || authenticated.AccessToken != "mock_acc_token" {
		t.Fatalf("authenticated account = %+v", authenticated)
	}

	second := &Account{
		MSISDN:         "6287711223344",
		Alias:          "Cadangan",
		AccessToken:    "second-access",
		RefreshToken:   "second-refresh",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := p.switchActiveAccount(ctx, "Cadangan"); err != nil {
		t.Fatalf("switchActiveAccount() error = %v", err)
	}
	active, err := repo.GetActive(ctx)
	if err != nil || active == nil || active.MSISDN != second.MSISDN {
		t.Fatalf("active account = %+v, err = %v", active, err)
	}
}

func TestMyXLQuotaSnapshotPreservesPartialSuccess(t *testing.T) {
	p, server, repo := setupTestMyXLEnv(t)
	server.Close()
	ctx := context.Background()

	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "access",
		RefreshToken:   "refresh",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatal(err)
	}

	partialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v8/packages/balance-and-credit":
			http.Error(w, "balance unavailable", http.StatusServiceUnavailable)
		case "/api/v8/packages/quota-details":
			payload := `{"status":"SUCCESS","message":"","data":{"quotas":[{"name":"Akrab Partial","expired_at":1735689600,"benefits":[{"name":"Utama","data_type":"DATA","remaining":1073741824,"total":2147483648}]}]}}`
			xTime := time.Now().UnixMilli()
			xData, _ := EncryptXData(payload, xTime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
		default:
			http.NotFound(w, r)
		}
	}))
	defer partialServer.Close()

	p.client.UpdateConfig(func(cfg *ClientConfig) {
		cfg.BaseAPIURL = partialServer.URL
	})
	p.client.SetHTTP(network.NewService(partialServer.Client(), nil).ForOwner("myxl"))

	snapshot := p.loadQuotaSnapshot(ctx, acc)
	if snapshot.BalanceErr == nil {
		t.Fatal("balance error was swallowed")
	}
	if snapshot.QuotaErr != nil || snapshot.Quota == nil || len(snapshot.Quota.Quotas) != 1 {
		t.Fatalf("quota success was lost: snapshot=%+v", snapshot)
	}

	text := FormatQuotaSnapshot(acc, snapshot, false)
	if !strings.Contains(text, "Pulsa:</b> <i>gagal dimuat") {
		t.Fatalf("partial balance failure not rendered: %s", text)
	}
	if !strings.Contains(text, "Akrab Partial") {
		t.Fatalf("successful quota missing: %s", text)
	}
	if strings.Contains(text, "Tidak ada paket kuota aktif") {
		t.Fatalf("quota result was misrepresented as empty: %s", text)
	}

	screen, err := p.menuMgr.BuildDashboardScreen(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"myxl:detail", "myxl:refresh", "myxl:accounts", "myxl:store"} {
		if !screenHasAction(screen, action) {
			t.Fatalf("dashboard missing task-flow action %q", action)
		}
	}
	if !strings.Contains(screen.Body, "Gagal dimuat") || !strings.Contains(screen.Body, "Akrab Partial") {
		t.Fatalf("dashboard did not preserve partial data: %s", screen.Body)
	}
}

func TestMyXLExecuteResolvedPurchaseIsSingleReservation(t *testing.T) {
	p, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	ctx := context.Background()

	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "access",
		RefreshToken:   "refresh",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatal(err)
	}

	resolved := resolvedPurchase{
		Intent: purchaseIntentState{
			MSISDN:      acc.MSISDN,
			OptionCode:  "OPT-10GB",
			Method:      "balance",
			QuotedPrice: 25000,
		},
		Account:        acc,
		PackageName:    "Combo 10GB",
		CanonicalPrice: 25000,
		EffectivePrice: 25000,
		Item: PurchaseItem{
			ItemCode:          "OPT-10GB",
			ItemPrice:         25000,
			ItemName:          "Combo 10GB",
			TokenConfirmation: "TOK-CONFIRM-123",
		},
		IdempotencyKey: "workflow-repeat-key",
	}

	first, err := p.executeResolvedPurchase(ctx, resolved)
	if err != nil {
		t.Fatalf("first executeResolvedPurchase() error = %v", err)
	}
	if first.Kind != purchaseOutcomeSuccess || first.Result == nil || !first.Result.IsSuccess {
		t.Fatalf("first outcome = %+v", first)
	}

	second, err := p.executeResolvedPurchase(ctx, resolved)
	if err != nil {
		t.Fatalf("second executeResolvedPurchase() error = %v", err)
	}
	if second.Kind != purchaseOutcomeDuplicate {
		t.Fatalf("repeated confirmation outcome = %s, want %s", second.Kind, purchaseOutcomeDuplicate)
	}

	var count int
	var status string
	if err := repo.db.QueryRowContext(ctx,
		"SELECT COUNT(*), MAX(status) FROM myxl_purchase_requests WHERE idempotency_key = ?",
		resolved.IdempotencyKey,
	).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "SUCCESS" {
		t.Fatalf("purchase rows=%d status=%q", count, status)
	}
}

func TestMyXLUnknownSettlementIsNotRetriedBySameConfirmation(t *testing.T) {
	p, originalServer, repo := setupTestMyXLEnv(t)
	originalServer.Close()
	ctx := context.Background()

	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "access",
		RefreshToken:   "refresh",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatal(err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	p.client.UpdateConfig(func(cfg *ClientConfig) {
		cfg.BaseAPIURL = failing.URL
	})
	p.client.SetHTTP(network.NewService(failing.Client(), nil).ForOwner("myxl"))

	resolved := resolvedPurchase{
		Intent: purchaseIntentState{
			MSISDN:      acc.MSISDN,
			OptionCode:  "OPT-UNKNOWN",
			Method:      "balance",
			QuotedPrice: 30000,
		},
		Account:        acc,
		PackageName:    "Unknown Result",
		CanonicalPrice: 30000,
		EffectivePrice: 30000,
		Item: PurchaseItem{
			ItemCode:          "OPT-UNKNOWN",
			ItemPrice:         30000,
			ItemName:          "Unknown Result",
			TokenConfirmation: "TOK-UNKNOWN",
		},
		IdempotencyKey: "workflow-unknown-key",
	}

	first, err := p.executeResolvedPurchase(ctx, resolved)
	if err != nil {
		t.Fatalf("uncertain execute error = %v", err)
	}
	if first.Kind != purchaseOutcomeUnknown {
		t.Fatalf("uncertain outcome = %s, want %s", first.Kind, purchaseOutcomeUnknown)
	}

	var status string
	if err := repo.db.QueryRowContext(ctx,
		"SELECT status FROM myxl_purchase_requests WHERE idempotency_key = ?",
		resolved.IdempotencyKey,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "UNKNOWN" {
		t.Fatalf("persisted status = %q, want UNKNOWN", status)
	}

	second, err := p.executeResolvedPurchase(ctx, resolved)
	if err != nil {
		t.Fatalf("repeated uncertain execute error = %v", err)
	}
	if second.Kind != purchaseOutcomeDuplicate {
		t.Fatalf("same uncertain confirmation was retried: outcome=%s", second.Kind)
	}
}

func TestMyXLQRISPresentationIsPendingAndBotOnlyCancellation(t *testing.T) {
	p, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	ctx := context.Background()

	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "access",
		RefreshToken:   "refresh",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatal(err)
	}

	qr := "00020101021226570011ID.CO.QRIS.WWW01189360002140000000001030301UBE51440014ID.LINKAJA.WWW0215ID20201770500670303UBE52040000530336054031005802ID5921TEST MERCHANT QRIS6013JAKARTA PUSAT610512345630421A6"
	resultScreen := p.menuMgr.BuildPurchaseResultScreen(&SettlementResult{
		IsSuccess:       true,
		Status:          "SUCCESS",
		TransactionCode: "TRX-PENDING",
		QRCode:          qr,
	}, "Paket QRIS", 25000, "qris", "OPT-QRIS")
	if !strings.Contains(resultScreen.Body, "Menunggu Pembayaran QRIS") ||
		!strings.Contains(resultScreen.Body, "Menunggu pembayaran") {
		t.Fatalf("QRIS result is not pending: %s", resultScreen.Body)
	}
	if strings.Contains(resultScreen.Body, "Pembelian Berhasil") {
		t.Fatalf("QRIS pending was mislabeled as completed success: %s", resultScreen.Body)
	}

	now := time.Now().UTC()
	if err := repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TRX-PENDING",
		IdempotencyKey:  "IDEMP-PENDING",
		MSISDN:          acc.MSISDN,
		OptionCode:      "OPT-QRIS",
		PackageName:     "Paket QRIS",
		Price:           25000,
		QRCode:          qr,
		Status:          "PENDING",
		CreatedAt:       now,
		ExpiresAt:       now.Add(pendingQRISTTL),
	}); err != nil {
		t.Fatal(err)
	}
	pendingScreen, err := p.menuMgr.BuildPendingQRISScreen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pendingScreen.Body, "tidak membatalkan pembayaran atau tagihan di operator") {
		t.Fatalf("pending QRIS missing bot-only cancellation disclosure: %s", pendingScreen.Body)
	}
	if !screenHasButtonText(pendingScreen, "Hapus dari Bot") {
		t.Fatal("pending QRIS missing explicit bot-only delete action")
	}
}

func screenHasAction(screen *ui.Screen, action string) bool {
	if screen == nil {
		return false
	}
	for _, row := range screen.Rows {
		for _, button := range row {
			if string(button.Data) == action {
				return true
			}
		}
	}
	return false
}

func screenHasButtonText(screen *ui.Screen, needle string) bool {
	if screen == nil {
		return false
	}
	for _, row := range screen.Rows {
		for _, button := range row {
			if strings.Contains(button.Text, needle) {
				return true
			}
		}
	}
	return false
}
