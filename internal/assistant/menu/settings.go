package menu

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/settings"
)

const pendingSettingTTL = 2 * time.Minute

type pendingSettingKey struct {
	UserID int64
	ChatID int64
}

type pendingSettingInput struct {
	Namespace string
	Key       string
	Category  string
	ExpiresAt time.Time
	Target    interaction.MessageTarget
	Sensitive bool
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
		if changed {
			if err := svc.Set(ctx, settings.ScopeUser, tx.UserID, ns, key, value, tx.UserID); err != nil {
				_ = tx.Answer(ctx, "Invalid setting value", true)
				return err
			}
			confirmation := fmt.Sprintf("%s → %s", def.Title, displaySettingValue(*def, value))
			_ = tx.Answer(ctx, confirmation, false)
		} else {
			_ = tx.Answer(ctx, fmt.Sprintf("%s is already %s", def.Title, displaySettingValue(*def, current)), false)
		}
		screen, err := buildSettingDetail(ctx, svc, tx.UserID, tx.Target.ChatID(), *def)
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
		if !ok || def == nil {
			return fmt.Errorf("unknown setting %s:%s", ns, key)
		}
		if err := svc.Reset(ctx, settings.ScopeUser, tx.UserID, ns, key, tx.UserID); err != nil {
			return err
		}
		_ = tx.Answer(ctx, fmt.Sprintf("%s reset to default/inherited value", def.Title), false)
		screen, err := buildSettingDetail(ctx, svc, tx.UserID, tx.Target.ChatID(), *def)
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
	key := pendingSettingKey{UserID: tx.UserID, ChatID: tx.Target.ChatID()}
	pending := pendingSettingInput{
		Namespace: def.Namespace,
		Key:       def.Key,
		Category:  def.Category,
		ExpiresAt: time.Now().Add(pendingSettingTTL),
		Target:    tx.Target,
		Sensitive: def.Sensitive,
	}
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = make(map[pendingSettingKey]pendingSettingInput)
	}
	c.pending[key] = pending
	c.pendingMu.Unlock()

	prompt := fmt.Sprintf("✏️ <b>%s</b>\n\n%s\n\nSend the new value as your next message.\nSend <code>/cancel</code> to abort.\n\n<i>This request expires in 2 minutes.</i>", def.Title, def.Description)
	if _, err := tx.Interaction.SendMessage(ctx, tx.Target.Peer(), prompt, nil); err != nil {
		c.clearPending(key)
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
	key := pendingSettingKey{UserID: userID, ChatID: chatID}
	c.pendingMu.Lock()
	pending, ok := c.pending[key]
	if ok && time.Now().After(pending.ExpiresAt) {
		delete(c.pending, key)
		ok = false
	}
	c.pendingMu.Unlock()
	if !ok {
		return false, nil
	}

	trimmed := strings.TrimSpace(text)
	if strings.EqualFold(trimmed, "/cancel") {
		c.clearPending(key)
		_, err := inter.SendMessage(ctx, pending.Target.Peer(), "❌ Setting change cancelled.", nil)
		return true, err
	}
	if strings.HasPrefix(trimmed, "/") {
		return false, nil
	}
	if trimmed == "" {
		_, _ = inter.SendMessage(ctx, pending.Target.Peer(), "Value cannot be empty.", nil)
		return true, nil
	}

	def, exists := svc.Registry().Get(pending.Namespace, pending.Key)
	if !exists || def == nil {
		c.clearPending(key)
		return true, fmt.Errorf("setting is no longer registered: %s:%s", pending.Namespace, pending.Key)
	}
	if err := svc.Set(ctx, settings.ScopeUser, userID, pending.Namespace, pending.Key, trimmed, userID); err != nil {
		_, _ = inter.SendMessage(ctx, pending.Target.Peer(), "⚠️ Invalid value: "+escapeText(err.Error()), nil)
		return true, nil
	}
	c.clearPending(key)
	valueText := "updated"
	if !def.Sensitive {
		valueText = "updated to <code>" + escapeText(trimmed) + "</code>"
	}
	_, err := inter.SendMessage(ctx, pending.Target.Peer(), fmt.Sprintf("✅ <b>%s</b> %s.", def.Title, valueText), nil)
	return true, err
}

func (c *Controller) clearPending(key pendingSettingKey) {
	c.pendingMu.Lock()
	delete(c.pending, key)
	c.pendingMu.Unlock()
}

func (c *Controller) editScreen(ctx context.Context, tx *callback.Transaction, screen *Screen) error {
	if screen == nil {
		return fmt.Errorf("settings screen is nil")
	}
	if c.instances != nil {
		c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), ScreenIDSettings)
	}
	if c.renderer == nil {
		return tx.Edit(ctx, screen.Text(), nil)
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
	screen := NewScreen(ScreenIDSettings, "⚙️ "+categoryLabel(category), "Select a setting for its current value, details, and controls.")
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
	card := NewScreen(ScreenIDSettings, "⚙️ "+def.Title, "")
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
	card.Body = body

	switch def.Type {
	case settings.TypeBool, settings.TypeEnum:
		card.AddRow(NewButton("✏️ Change", fmt.Sprintf("a1:settings:set:%s:%s:next", def.Namespace, def.Key)))
	case settings.TypeInt, settings.TypeDuration:
		card.AddRow(NewButton("➖ Decrease", fmt.Sprintf("a1:settings:set:%s:%s:prev", def.Namespace, def.Key)), NewButton("➕ Increase", fmt.Sprintf("a1:settings:set:%s:%s:next", def.Namespace, def.Key)))
	case settings.TypeString:
		card.AddRow(NewButton("✏️ Change", fmt.Sprintf("a1:settings:set:%s:%s:next", def.Namespace, def.Key)))
	}
	if explicit != nil {
		card.AddRow(NewButton("↩ Reset", fmt.Sprintf("a1:settings:reset:%s:%s:reset", def.Namespace, def.Key)))
	}
	card.AddRow(NewButton("« "+categoryLabel(def.Category), "a1:settings:category:"+def.Category), NewButton("⚙️ Settings", "a1:settings:home"))
	return card, nil
}

func parseSettingRef(state string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(state), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid setting reference")
	}
	return parts[0], parts[1], nil
}

func parseSettingState(state string) (string, string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(state), ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("invalid setting state")
	}
	op := "next"
	if len(parts) == 3 && parts[2] != "" {
		op = parts[2]
	}
	return parts[0], parts[1], op, nil
}

func nextSettingValue(def *settings.SettingDefinition, current, op string) (string, bool, error) {
	if def == nil {
		return "", false, fmt.Errorf("nil setting definition")
	}
	if op != "next" && op != "prev" {
		return "", false, fmt.Errorf("unsupported setting operation %q", op)
	}
	direction := int64(1)
	if op == "prev" {
		direction = -1
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
				n := (i + int(direction)) % len(def.AllowedValues)
				if n < 0 {
					n += len(def.AllowedValues)
				}
				return def.AllowedValues[n], true, nil
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
		v += direction * step
		if def.MaxVal != nil && v > *def.MaxVal {
			if def.MinVal != nil {
				v = *def.MinVal
			} else {
				v = *def.MaxVal
			}
		}
		if def.MinVal != nil && v < *def.MinVal {
			if def.MaxVal != nil {
				v = *def.MaxVal
			} else {
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
		d += time.Duration(direction) * step
		if def.MaxVal != nil && int64(d/time.Second) > *def.MaxVal {
			d = time.Duration(*def.MaxVal) * time.Second
		}
		if def.MinVal != nil && int64(d/time.Second) < *def.MinVal {
			d = time.Duration(*def.MinVal) * time.Second
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
	return escapeText(value)
}

func boundText(v *int64) string {
	if v == nil {
		return "∞"
	}
	return strconv.FormatInt(*v, 10)
}

func escapeText(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(s)
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

// Keep sync imported for compatibility with existing Controller pending state synchronization.
var _ sync.Locker
