package menu

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/settings"
)

type pendingSettingInput struct {
	Namespace string
	Key       string
	Category  string
	ExpiresAt time.Time
	Target    interaction.MessageTarget
}

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
		category := tx.Payload.State
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
	r.Register("settings", "set", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		ns, key, op, err := parseSettingState(tx.Payload.State)
		if err != nil {
			return err
		}
		def, ok := svc.Registry().Get(ns, key)
		if !ok || def == nil {
			_ = tx.Answer(ctx, "Setting is no longer available", true)
			return fmt.Errorf("unknown setting %s:%s", ns, key)
		}
		current, err := svc.Resolve(ctx, tx.UserID, tx.Target.ChatID(), ns, key)
		if err != nil {
			return err
		}
		value, changed, err := nextSettingValue(def, current, op)
		if err != nil {
			if def.Type == settings.TypeString {
				return c.beginStringInput(ctx, tx, def)
			}
			_ = tx.Answer(ctx, "This setting cannot be changed from the menu", true)
			return err
		}
		if !changed {
			_ = tx.Answer(ctx, fmt.Sprintf("%s is already %s", def.Title, current), true)
		} else if err := svc.Set(ctx, settings.ScopeUser, tx.UserID, ns, key, value, tx.UserID); err != nil {
			_ = tx.Answer(ctx, "Invalid setting value", true)
			return err
		} else {
			_ = tx.Answer(ctx, fmt.Sprintf("%s → %s", def.Title, value), false)
		}
		screen, err := buildSettingsCategory(ctx, svc, tx.UserID, tx.Target.ChatID(), def.Category)
		if err != nil {
			return err
		}
		return c.editScreen(ctx, tx, screen)
	})
	r.Register("settings", "reset", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		ns, key, _, err := parseSettingState(tx.Payload.State)
		if err != nil {
			return err
		}
		def, ok := svc.Registry().Get(ns, key)
		if !ok {
			return fmt.Errorf("unknown setting %s:%s", ns, key)
		}
		if err := svc.Reset(ctx, settings.ScopeUser, tx.UserID, ns, key, tx.UserID); err != nil {
			return err
		}
		_ = tx.Answer(ctx, fmt.Sprintf("%s reset to default/inherited value", def.Title), false)
		screen, err := buildSettingsCategory(ctx, svc, tx.UserID, tx.Target.ChatID(), def.Category)
		if err != nil {
			return err
		}
		return c.editScreen(ctx, tx, screen)
	})
}

func (c *Controller) beginStringInput(ctx context.Context, tx *callback.Transaction, def *settings.SettingDefinition) error {
	if tx.Interaction == nil || def == nil {
		return fmt.Errorf("string setting input unavailable")
	}
	c.pendingMu.Lock()
	c.pending[tx.UserID] = pendingSettingInput{Namespace: def.Namespace, Key: def.Key, Category: def.Category, ExpiresAt: time.Now().Add(2 * time.Minute), Target: tx.Target}
	c.pendingMu.Unlock()
	_, err := tx.Interaction.SendMessage(ctx, tx.Target.Peer(), fmt.Sprintf("✏️ <b>%s</b>\n\n%s\n\nSend the new value as your next message.\nSend <code>/cancel</code> to abort.\n\n<i>This request expires in 2 minutes.</i>", def.Title, def.Description), nil)
	if err != nil {
		c.clearPending(tx.UserID)
		return err
	}
	_ = tx.Answer(ctx, "Waiting for your value…", false)
	return nil
}

// HandleTextMessage consumes a pending free-form setting input. It returns true when the message was consumed.
func (c *Controller) HandleTextMessage(ctx context.Context, userID, chatID int64, text string, inter interaction.MessageInteraction, svc *settings.Service) (bool, error) {
	if userID == 0 || svc == nil || inter == nil {
		return false, nil
	}
	c.pendingMu.Lock()
	pending, ok := c.pending[userID]
	if ok && time.Now().After(pending.ExpiresAt) {
		delete(c.pending, userID)
		ok = false
	}
	c.pendingMu.Unlock()
	if !ok {
		return false, nil
	}
	if chatID != pending.Target.ChatID() {
		return true, fmt.Errorf("pending setting belongs to another chat")
	}

	trimmed := strings.TrimSpace(text)
	if strings.EqualFold(trimmed, "/cancel") {
		c.clearPending(userID)
		_, err := inter.SendMessage(ctx, pending.Target.Peer(), "❌ Setting change cancelled.", nil)
		return true, err
	}
	if strings.HasPrefix(trimmed, "/") {
		return false, nil
	}

	def, exists := svc.Registry().Get(pending.Namespace, pending.Key)
	if !exists || def == nil {
		c.clearPending(userID)
		return true, fmt.Errorf("setting is no longer registered: %s:%s", pending.Namespace, pending.Key)
	}
	value := trimmed
	if value == "" {
		_, _ = inter.SendMessage(ctx, pending.Target.Peer(), "Value cannot be empty.", nil)
		return true, nil
	}
	if err := svc.Set(ctx, settings.ScopeUser, userID, pending.Namespace, pending.Key, value, userID); err != nil {
		_, _ = inter.SendMessage(ctx, pending.Target.Peer(), "⚠️ Invalid value: "+err.Error(), nil)
		return true, nil
	}
	c.clearPending(userID)
	_, err := inter.SendMessage(ctx, pending.Target.Peer(), fmt.Sprintf("✅ <b>%s</b> updated to <code>%s</code>.", def.Title, value), nil)
	return true, err
}

func (c *Controller) clearPending(userID int64) {
	c.pendingMu.Lock()
	delete(c.pending, userID)
	c.pendingMu.Unlock()
}

func (c *Controller) editScreen(ctx context.Context, tx *callback.Transaction, screen *Screen) error {
	if c.instances != nil {
		c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), ScreenIDSettings)
	}
	text, markup := c.renderer(screen)
	return tx.Edit(ctx, text, markup)
}
func buildSettingsHome(reg *settings.Registry) *Screen {
	screen := NewScreen(ScreenIDSettings, "⚙️ GoUltroid Settings", "Configure bot variables and plugin behavior. Changes are persisted and applied through the central settings service.")
	if reg == nil {
		return screen.AddRow(NewButton("« Back", "a1:assistant:start"))
	}
	for _, category := range reg.Categories() {
		data := fmt.Sprintf("a1:settings:category:%s", category)
		screen.AddRow(NewButton(categoryLabel(category), data))
	}
	screen.AddRow(NewButton("« Back to Menu", "a1:assistant:start"))
	return screen
}
func buildSettingsCategory(ctx context.Context, svc *settings.Service, userID, chatID int64, category string) (*Screen, error) {
	defs := svc.Registry().ListByCategory(category)
	if len(defs) == 0 {
		return NewScreen(ScreenIDSettings, "⚙️ Settings", "No settings are registered in this category."), nil
	}
	screen := NewScreen(ScreenIDSettings, "⚙️ "+categoryLabel(category), "Tap a setting to change it. String settings open a secure one-message input flow.")
	for _, def := range defs {
		value, err := svc.Resolve(ctx, userID, chatID, def.Namespace, def.Key)
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("%s: %s", def.Title, displaySettingValue(def, value))
		state := fmt.Sprintf("%s:%s:next", def.Namespace, def.Key)
		screen.AddRow(NewButton(label, "a1:settings:set:"+state))
		if value != def.DefaultValue {
			screen.AddRow(NewButton("↩ Reset "+def.Title, "a1:settings:reset:"+def.Namespace+":"+def.Key+":reset"))
		}
	}
	screen.AddRow(NewButton("« Settings", "a1:settings:home"))
	return screen, nil
}
func parseSettingState(state string) (string, string, string, error) {
	parts := strings.Split(state, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("invalid setting state")
	}
	op := "next"
	if len(parts) >= 3 && parts[2] != "" {
		op = parts[2]
	}
	return parts[0], parts[1], op, nil
}
func nextSettingValue(def *settings.SettingDefinition, current, op string) (string, bool, error) {
	if def == nil {
		return "", false, fmt.Errorf("nil setting definition")
	}
	switch def.Type {
	case settings.TypeBool:
		v, err := strconv.ParseBool(current)
		if err != nil {
			return "", false, err
		}
		return strconv.FormatBool(!v), true, nil
	case settings.TypeEnum:
		if len(def.AllowedValues) == 0 {
			return "", false, fmt.Errorf("enum %s:%s has no values", def.Namespace, def.Key)
		}
		for i, v := range def.AllowedValues {
			if v == current {
				return def.AllowedValues[(i+1)%len(def.AllowedValues)], true, nil
			}
		}
		return def.AllowedValues[0], true, nil
	case settings.TypeInt:
		v, err := strconv.ParseInt(current, 10, 64)
		if err != nil {
			return "", false, err
		}
		step := def.UI.Step
		if step <= 0 {
			step = 1
		}
		v += step
		if def.MaxVal != nil && v > *def.MaxVal {
			if def.MinVal != nil {
				v = *def.MinVal
			}
		}
		return strconv.FormatInt(v, 10), true, nil
	case settings.TypeDuration:
		d, err := time.ParseDuration(current)
		if err != nil {
			return "", false, err
		}
		step := time.Duration(def.UI.Step) * time.Second
		if step <= 0 {
			step = 5 * time.Second
		}
		d += step
		if def.MaxVal != nil && int64(d/time.Second) > *def.MaxVal {
			if def.MinVal != nil {
				d = time.Duration(*def.MinVal) * time.Second
			}
		}
		return d.String(), true, nil
	case settings.TypeString:
		return "", false, fmt.Errorf("string settings require text input")
	default:
		return "", false, fmt.Errorf("unsupported setting type %q", def.Type)
	}
}
func displaySettingValue(def settings.SettingDefinition, value string) string {
	if def.Sensitive && value != "" {
		return "••••"
	}
	return value
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
