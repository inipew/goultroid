package myxl

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSavedPackageDetailUsesCanonicalReturnedOptionCodeForPayment(t *testing.T) {
	plugin, server, repo := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()
	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "acc_tok",
		IDToken:        "id_tok",
		RefreshToken:   "ref_tok",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatalf("save account: %v", err)
	}

	screen, err := plugin.menuMgr.BuildPackageDetailScreen(ctx, acc, "OPT-SAVED-OLD")
	if err != nil {
		t.Fatalf("BuildPackageDetailScreen: %v", err)
	}
	if !strings.Contains(screen.Body, "OPT-10GB") {
		t.Fatalf("detail screen did not expose canonical option code: %s", screen.Body)
	}

	var pulseData string
	for _, row := range screen.Rows {
		for _, button := range row {
			if button.Text == "💰 Pulsa" {
				pulseData = string(button.Data)
			}
		}
	}
	if pulseData != "myxl:method:balance:OPT-10GB" {
		t.Fatalf("Pulsa action = %q, want canonical option code", pulseData)
	}
	if strings.Contains(pulseData, "OPT-SAVED-OLD") {
		t.Fatalf("Pulsa action retained stale saved option code: %q", pulseData)
	}
}
