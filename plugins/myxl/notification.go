package myxl

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/network"
)

type NotificationItem struct {
	NotificationID string `json:"notification_id"`
	IsRead         bool   `json:"is_read"`
	BriefMessage   string `json:"brief_message"`
	FullMessage    string `json:"full_message"`
	Timestamp      string `json:"timestamp"`
}

func (c *Client) GetNotifications(ctx context.Context, acc *Account) ([]NotificationItem, error) {
	if err := c.EnsureFreshToken(ctx, acc); err != nil {
		return nil, err
	}
	resp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "dashboard/api/v8/segments", map[string]any{"access_token": acc.AccessToken})
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.IsSuccess() {
		return nil, fmt.Errorf("myxl: notification request failed")
	}
	var data struct {
		Notification struct {
			Data []NotificationItem `json:"data"`
		} `json:"notification"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, fmt.Errorf("decode notifications: %w", err)
	}
	return data.Notification.Data, nil
}

func (c *Client) ReadNotification(ctx context.Context, acc *Account, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("myxl: empty notification id")
	}
	resp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "api/v8/notification/detail", map[string]any{"is_enterprise": false, "lang": "en", "notification_id": id})
	if err != nil {
		return err
	}
	if resp == nil || !resp.IsSuccess() {
		return fmt.Errorf("myxl: mark notification read failed")
	}
	return nil
}

func (c *Client) ReadAllNotifications(ctx context.Context, acc *Account, items []NotificationItem) (int, int) {
	read, failed := 0, 0
	for _, item := range items {
		if item.IsRead || strings.TrimSpace(item.NotificationID) == "" {
			continue
		}
		if err := c.ReadNotification(ctx, acc, item.NotificationID); err != nil {
			failed++
		} else {
			read++
		}
	}
	return read, failed
}

func FormatNotifications(items []NotificationItem) string {
	return FormatNotificationsLimited(items, 0)
}

func FormatNotificationsLimited(items []NotificationItem, limit int) string {
	var b strings.Builder
	b.WriteString("🔔 <b>Notifikasi MyXL</b>\n")
	unread := 0
	for _, item := range items {
		if !item.IsRead {
			unread++
		}
	}
	footer := fmt.Sprintf("\nTotal: %d · Belum dibaca: %d", len(items), unread)
	shown := 0
	for i, item := range items {
		status := "Dibaca"
		if !item.IsRead {
			status = "Belum dibaca"
		}
		brief, full, timestamp := item.BriefMessage, item.FullMessage, item.Timestamp
		if limit > 0 {
			brief = truncateRunes(brief, 100)
			full = truncateRunes(full, 180)
			timestamp = truncateRunes(timestamp, 80)
		}
		line := fmt.Sprintf("\n%d. <b>%s</b> — %s\n🕐 %s\n%s\n", i+1, html.EscapeString(brief), status, html.EscapeString(timestamp), html.EscapeString(full))
		if limit > 0 && len([]rune(b.String()+line+footer+"\n… item lain tidak ditampilkan")) > limit {
			break
		}
		b.WriteString(line)
		shown++
	}
	if len(items) == 0 {
		b.WriteString("\nTidak ada notifikasi.\n")
	}
	if shown < len(items) {
		b.WriteString("\n… item lain tidak ditampilkan")
	}
	b.WriteString(footer)
	return b.String()
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}

func (p *Plugin) handleNotifications(ctx *core.Context) error {
	queryCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()
	acc, err := p.repo.GetActive(queryCtx)
	if err != nil {
		return ctx.Fail(err, "Gagal membaca akun MyXL.")
	}
	if acc == nil {
		return ctx.Status("Tidak ada akun MyXL aktif. Silakan login terlebih dahulu.")
	}
	items, err := p.client.GetNotifications(queryCtx, acc)
	if err != nil {
		return ctx.Fail(err, "Gagal memuat notifikasi MyXL.")
	}
	content := FormatNotifications(items)
	if !ctx.IsAssistant() && ctx.SenderID() > 0 {
		if attempted, err := p.openNativeNotifications(ctx, acc.MSISDN, FormatNotificationsLimited(items, 3800)); attempted {
			return err
		}
	}
	return deliverHTML(ctx, content)
}
