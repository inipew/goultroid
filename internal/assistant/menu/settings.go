package menu

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/settings"
)

// AttachSettingsRoutes binds the Settings service to the assistant menu.
func (c *Controller) AttachSettingsRoutes(r *callback.Router, svc *settings.Service) {
	if r == nil || svc == nil {
		return
	}
	r.Register("settings", "home", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		return c.editScreen(ctx, tx, buildSettingsHome(svc.Registry()))
	})
	r.Register("settings", "category", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		category := strings.TrimSpace(tx.Payload.State)
		if category == "" {
			return fmt.Errorf("settings category is required")
		}
		screen, err := buildSettingsCategory(ctx, svc, tx.UserID, tx.Target.ChatID(), category)
		if err != nil {
			_ = tx.Answer(ctx, "Unable to load settings", true)
			return err
		}
		return c.editScreen(ctx, tx, screen)
	})
	r.Register("settings", "info", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		ns, key, err := parseSettingRef(tx.Payload.State)
		if err != nil {
			return err
		}
		def, ok := svc.Registry().Get(ns, key)
		if !ok || def == nil {
			_ = tx.Answer(ctx, "Setting is no longer available", true)
			return fmt.Errorf("unknown setting %s:%s", ns, key)
		}
		screen, err := buildSettingDetail(ctx, svc, tx.UserID, tx.Target.ChatID(), *def)
		if err != nil {
			return err
		}
		return c.editScreen(ctx, tx, screen)
	})
	cutover := func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		_ = tx.Answer(ctx, "Settings moved to the new /start flow. Reopen Settings there.", true)
		return c.editScreen(ctx, tx, BuildSettingsScreen(""))
	}
	r.Register("settings", "set", cutover)
	r.Register("settings", "reset", cutover)
}

// HandleTextMessage dispatches generic Assistant text handlers. Settings input
// ownership moved to the bounded a2 interaction runtime in P5-E.
func (c *Controller) HandleTextMessage(ctx context.Context, userID, chatID int64, text string, inter interaction.MessageInteraction) (bool, error) {
	if userID == 0 || inter == nil {
		return false, nil
	}
	c.textHandlersMu.RLock()
	handlers := append([]TextHandler(nil), c.textHandlers...)
	c.textHandlersMu.RUnlock()
	for _, h := range handlers {
		if handled, err := h.HandleTextMessage(ctx, userID, chatID, text, inter); handled {
			return true, err
		}
	}
	return false, nil
}

func (c *Controller) editScreen(ctx context.Context, tx *callback.Transaction, screen *Screen) error {
	return c.EditScreen(ctx, tx, screen)
}

func buildSettingsHome(reg *settings.Registry) *Screen {
	screen := NewScreen(ScreenIDSettings, "⚙️ GoUltroid Settings", "Legacy read-only Settings browser. Reopen /start to make changes through the a2 Settings flow.")
	if reg == nil {
		return screen.AddRow(NewButton("« Back", "a1:assistant:start"))
	}
	for _, category := range reg.Categories() {
		screen.AddRow(NewButton(categoryLabel(category), "a1:settings:category:"+category))
	}
	screen.AddRow(NewButton("« Back to Menu", "a1:assistant:start"))
	return screen
}

func buildSettingsCategory(ctx context.Context, svc *settings.Service, userID, chatID int64, category string) (*Screen, error) {
	if svc == nil || svc.Registry() == nil {
		return nil, fmt.Errorf("settings service is unavailable")
	}
	defs := svc.Registry().ListByCategory(category)
	if len(defs) == 0 {
		screen := NewScreen(ScreenIDSettings, "⚙️ Settings", "No settings are registered in this category.")
		screen.AddRow(NewButton("« Settings", "a1:settings:home"))
		return screen, nil
	}
	screen := NewScreen(ScreenIDSettings, "⚙️ "+categoryLabel(category), "Legacy read-only browser for current setting values and details.")
	for _, def := range defs {
		value, err := svc.Resolve(ctx, userID, chatID, def.Namespace, def.Key)
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("%s · %s", def.Title, displaySettingValue(def, value))
		screen.AddRow(NewButton(label, "a1:settings:info:"+def.Namespace+":"+def.Key))
	}
	screen.AddRow(NewButton("« Settings", "a1:settings:home"))
	return screen, nil
}

func buildSettingDetail(ctx context.Context, svc *settings.Service, userID, chatID int64, def settings.SettingDefinition) (*Screen, error) {
	current, err := svc.Resolve(ctx, userID, chatID, def.Namespace, def.Key)
	if err != nil {
		return nil, err
	}
	explicit, err := svc.Get(ctx, settings.ScopeUser, userID, def.Namespace, def.Key)
	if err != nil {
		return nil, err
	}
	status := "Inherited/default"
	if explicit != nil {
		status = "User override"
	}
	screen := NewScreen(ScreenIDSettings, "⚙️ "+def.Title, "")
	body := fmt.Sprintf("<b>%s</b>\n%s", escapeText(def.Title), escapeText(def.Description))
	body += fmt.Sprintf("\n\n<b>Current</b>: %s\n<b>Source</b>: %s\n<b>Type</b>: <code>%s</code>\n<b>Default</b>: %s\n<b>Widget</b>: <code>%s</code>", displaySettingValue(def, current), status, escapeText(string(def.Type)), displaySettingValue(def, def.DefaultValue), escapeText(string(def.UI.Widget)))
	if len(def.AllowedValues) > 0 {
		body += "\n<b>Options</b>: <code>" + escapeText(strings.Join(def.AllowedValues, ", ")) + "</code>"
	}
	if def.MinVal != nil || def.MaxVal != nil {
		body += fmt.Sprintf("\n<b>Range</b>: %s … %s", boundText(def.MinVal), boundText(def.MaxVal))
	}
	if def.UI.Step > 0 {
		body += fmt.Sprintf("\n<b>Step</b>: <code>%d</code>", def.UI.Step)
	}
	body += "\n\n<i>Legacy Settings is read-only. Reopen <code>/start</code> to change or reset this setting.</i>"
	screen.Body = body
	screen.AddRow(NewButton("« "+categoryLabel(def.Category), "a1:settings:category:"+def.Category), NewButton("⚙️ Settings", "a1:settings:home"))
	return screen, nil
}

func parseSettingRef(state string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(state), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid setting reference")
	}
	return parts[0], parts[1], nil
}

func displaySettingValue(def settings.SettingDefinition, value string) string {
	if def.Sensitive && value != "" {
		return "••••"
	}
	return escapeText(value)
}
func boundText(v *int64) string {
	if v == nil {
		return "∞"
	}
	return strconv.FormatInt(*v, 10)
}
func escapeText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}

func categoryLabel(category string) string {
	switch strings.ToLower(category) {
	case settings.CategoryGeneral:
		return "⚙️ General"
	case settings.CategorySecurity:
		return "🔒 Security"
	case settings.CategoryModeration:
		return "🛡 Moderation"
	case settings.CategoryAutomation:
		return "🤖 Automation"
	case settings.CategoryUI:
		return "🎨 Interface"
	case settings.CategoryAdvanced:
		return "🧰 Advanced"
	default:
		return "📁 " + category
	}
}
