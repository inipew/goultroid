package addon_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	pluginAddon "github.com/inipew/goultroid/plugins/addon"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
}

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestAddonPlugin(t *testing.T) {
	db := setupTestDB(t)
	repo := addon.NewSQLiteRepository(db)
	gate := addon.NewCapabilityGate()
	mgr := addon.NewManager(repo, gate, "1.0.0", zap.NewNop())

	p := pluginAddon.New(mgr)
	if p.Name() != "addon" {
		t.Errorf("expected plugin name addon, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}

	cmd := cmds[0]
	mockTG := &mockTelegram{}

	newCtx := func(args ...string) *core.Context {
		return &core.Context{
			Ctx:     context.Background(),
			Svc:     mockTG,
			PeerID:  &tg.InputPeerUser{UserID: 12345},
			Message: &core.Message{ID: 1, SenderID: 12345, IsOutgoing: true},
			Args:    args,
		}
	}

	// 1. Initial list empty
	if err := cmd.Handler(newCtx("list")); err != nil {
		t.Fatalf("addon list failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "No external addons installed") {
		t.Errorf("expected empty list message, got %s", mockTG.edited)
	}

	// 2. Install addon
	manifest := `
name: mini-calc
version: 1.0.0
commands: [calc]
capabilities: [telegram.send]
`
	if err := cmd.Handler(newCtx("install", manifest)); err != nil {
		t.Fatalf("addon install failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Addon Installed Successfully") || !strings.Contains(mockTG.edited, "mini-calc") {
		t.Errorf("expected successful install card, got %s", mockTG.edited)
	}

	// 3. List with item
	if err := cmd.Handler(newCtx("list")); err != nil {
		t.Fatalf("addon list failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "mini-calc") || !strings.Contains(mockTG.edited, "active") {
		t.Errorf("expected list to contain mini-calc, got %s", mockTG.edited)
	}

	// 4. Info
	if err := cmd.Handler(newCtx("info", "mini-calc")); err != nil {
		t.Fatalf("addon info failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "mini-calc") || !strings.Contains(mockTG.edited, "telegram.send") {
		t.Errorf("expected info card, got %s", mockTG.edited)
	}

	// 5. Disable
	if err := cmd.Handler(newCtx("disable", "mini-calc")); err != nil {
		t.Fatalf("addon disable failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Disabled addon") {
		t.Errorf("expected disabled message, got %s", mockTG.edited)
	}

	// 6. Enable
	if err := cmd.Handler(newCtx("enable", "mini-calc")); err != nil {
		t.Fatalf("addon enable failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Enabled addon") {
		t.Errorf("expected enabled message, got %s", mockTG.edited)
	}

	// 7. Uninstall
	if err := cmd.Handler(newCtx("uninstall", "mini-calc")); err != nil {
		t.Fatalf("addon uninstall failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Uninstalled addon") {
		t.Errorf("expected uninstalled message, got %s", mockTG.edited)
	}
}
