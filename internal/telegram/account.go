package telegram

import (
	"context"
	"fmt"
	"os"
	"strings"

	gotd "github.com/gotd/td/telegram"
	"github.com/inipew/goultroid/internal/config"
)

// Account describes the Telegram account stored in a local session.
type Account struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	Phone     string
	Bot       bool
}

func (a Account) DisplayName() string {
	if name := strings.TrimSpace(a.FirstName + " " + a.LastName); name != "" {
		return name
	}
	if a.Username != "" {
		return "@" + a.Username
	}
	return fmt.Sprintf("%d", a.ID)
}

// WhoAmI validates an existing file session and returns its authenticated user.
// It never starts the update dispatcher or an interactive authentication flow.
func WhoAmI(ctx context.Context, cfg *config.Config) (*Account, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if _, err := os.Stat(cfg.SessionFile); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s does not exist; run 'goultroid run' to login", cfg.SessionFile)
		}
		return nil, fmt.Errorf("inspect session: %w", err)
	}
	client := gotd.NewClient(cfg.AppID, cfg.AppHash, gotd.Options{
		SessionStorage: &gotd.FileSessionStorage{Path: cfg.SessionFile},
		NoUpdates:      true,
	})
	var account *Account
	err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("check authentication: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("session is not authorized; run 'goultroid run' to login")
		}
		me, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("fetch current account: %w", err)
		}
		account = &Account{ID: me.ID, FirstName: me.FirstName, LastName: me.LastName, Username: me.Username, Phone: me.Phone, Bot: me.Bot}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return account, nil
}
