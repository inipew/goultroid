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
)

func TestNotificationFormattingEscapesContent(t *testing.T) {
	items := []NotificationItem{{NotificationID: "id-1", BriefMessage: "<promo>", FullMessage: "A & B", Timestamp: "today", IsRead: false}}
	got := FormatNotifications(items)
	if !strings.Contains(got, "&lt;promo&gt;") || !strings.Contains(got, "A &amp; B") || !strings.Contains(got, "Belum dibaca: 1") {
		t.Fatalf("unexpected notification output: %s", got)
	}
}

func TestNotificationClientUsesNativeEndpoints(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var request EncryptedBody
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		plain, err := DecryptXData(request.XData, request.XTime, DefaultXDataKey)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/dashboard/api/v8/segments" && !strings.Contains(plain, `"access_token":"access"`) {
			t.Errorf("missing access token: %s", plain)
		}
		if r.URL.Path == "/api/v8/notification/detail" && !strings.Contains(plain, `"notification_id":"n1"`) {
			t.Errorf("missing notification id: %s", plain)
		}
		payload := `{"status":"SUCCESS","data":{}}`
		if r.URL.Path == "/dashboard/api/v8/segments" {
			payload = `{"status":"SUCCESS","data":{"notification":{"data":[{"notification_id":"n1","brief_message":"Hello"}]}}}`
		}
		xTime := time.Now().UnixMilli()
		xData, _ := EncryptXData(payload, xTime, DefaultXDataKey)
		_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
	}))
	defer server.Close()
	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	c := NewClient(cfg, nil, network.NewService(server.Client(), nil).ForOwner("myxl"))
	acc := &Account{MSISDN: "6281987654321", IDToken: "id", AccessToken: "access", TokenExpiresAt: time.Now().Add(time.Hour)}
	items, err := c.GetNotifications(context.Background(), acc)
	if err != nil || len(items) != 1 {
		t.Fatalf("get notifications: %v, %+v", err, items)
	}
	if err := c.ReadNotification(context.Background(), acc, "n1"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/dashboard/api/v8/segments" || paths[1] != "/api/v8/notification/detail" {
		t.Fatalf("paths: %v", paths)
	}
}

func TestNotificationClientRejectsFailedAPIStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		xTime := time.Now().UnixMilli()
		xData, _ := EncryptXData(`{"status":"FAILED","message":"unavailable","data":{}}`, xTime, DefaultXDataKey)
		_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
	}))
	defer server.Close()
	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	c := NewClient(cfg, nil, network.NewService(server.Client(), nil).ForOwner("myxl"))
	acc := &Account{MSISDN: "6281987654321", IDToken: "id", AccessToken: "access", TokenExpiresAt: time.Now().Add(time.Hour)}
	if _, err := c.GetNotifications(context.Background(), acc); err == nil {
		t.Fatal("failed API status accepted")
	}
	if err := c.ReadNotification(context.Background(), acc, "n1"); err == nil {
		t.Fatal("failed read status accepted")
	}
}

func TestNotificationFormattingLimitKeepsHTMLValid(t *testing.T) {
	items := []NotificationItem{{BriefMessage: strings.Repeat("A", 4000), FullMessage: "<tail>"}}
	got := FormatNotificationsLimited(items, 500)
	if len([]rune(got)) > 500 || strings.Contains(got, "<tail>") || !strings.Contains(got, "1. <b>") || !strings.Contains(got, "Total: 1") {
		t.Fatalf("invalid limited output: %s", got)
	}
}

func TestNotificationMenuHasEntry(t *testing.T) {
	p, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	if err := repo.Save(context.Background(), &Account{MSISDN: "6281987654321", IsActive: true, TokenExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	screen, err := p.menuMgr.BuildDashboardScreen(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !screenHasAction(screen, "myxl:notifications") {
		t.Fatal("dashboard missing notifications action")
	}
}
