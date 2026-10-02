package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
)

func TestRefreshAllTokensContinuesAfterAccountFailure(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	for i, number := range []string{"6281111111111", "6282222222222", "6283333333333"} {
		now := time.Now().UTC()
		if err := repo.Save(context.Background(), &Account{
			MSISDN: number, IsActive: i == 0, RefreshToken: "refresh-" + number,
			CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if r.Form.Get("refresh_token") == "refresh-6282222222222" {
			http.Error(w, "rejected", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken: "new-access", IDToken: "new-id", RefreshToken: "new-refresh", ExpiresIn: 3600,
		})
	}))
	defer server.Close()
	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	p := New(repo, NewClient(cfg, repo, network.NewService(server.Client(), nil).ForOwner("myxl")))

	results, err := p.refreshAllTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Err != nil || results[1].Err == nil || results[2].Err != nil {
		t.Fatalf("unexpected refresh results: %+v", results)
	}
	for _, number := range []string{"6281111111111", "6283333333333"} {
		acc, err := repo.GetByMSISDN(context.Background(), number)
		if err != nil || acc.IDToken != "new-id" {
			t.Fatalf("account %s not refreshed: %v", number, err)
		}
	}
	failed, err := repo.GetByMSISDN(context.Background(), "6282222222222")
	if err != nil || failed.RefreshToken != "refresh-6282222222222" {
		t.Fatalf("failed account was overwritten: %v", err)
	}
	active, err := repo.GetActive(context.Background())
	if err != nil || active.MSISDN != "6281111111111" {
		t.Fatalf("active account changed: %v", err)
	}
}

func TestAccountsScreenOffersRefreshAllTokens(t *testing.T) {
	p, server, repo := setupTestMyXLEnv(t)
	defer server.Close()
	now := time.Now().UTC()
	if err := repo.Save(context.Background(), &Account{
		MSISDN: "6281111111111", RefreshToken: "refresh", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	screen, err := p.menuMgr.BuildAccountsScreen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !screenHasAction(screen, "myxl:token_refresh_all") {
		t.Fatal("Kelola Akun has no refresh-all action")
	}
}
