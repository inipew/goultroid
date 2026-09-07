package menu

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/settings"
)

// AttachSettingsRoutes binds the Settings service to the assistant menu.
// The menu is a presentation surface: validation, persistence, inheritance,
// cache invalidation and events remain owned by settings.Service.
func (c *Controller) AttachSettingsRoutes(r *callback.Router, svc *settings.Service) {
	if r == nil || svc == nil {
		return
	}

	r.Register("settings", "home", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		screen := buildSettingsHome(svc.Registry())
		return c.editScreen(ctx, tx, screen)
	})

	r.Register("settings", "category", func(ctx context.Context, tx *callback.Transaction) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
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
		ns, key, op, err := parseSettingState(tx.Payload.State)
		if err != nil {
			return err
		}

		def, ok := svc.Registry().Get(ns, key)
		if !ok {
			_ = tx.Answer(ctx, "Setting is no longer available", true)
			return fmt.Errorf("unknown setting %s:%s", ns, key)
		}

		current, err := svc.Resolve(ctx, tx.UserID, tx.Target.ChatID(), ns, key)
		if err != nil {
			return err
		}
		value, changed, err := nextSettingValue(def, current, op)
		if err != nil {
			_ = tx.Answer(ctx, "This setting cannot be changed from the menu", true)
			return err
		}
		if !changed {
			_ = tx.Answer(ctx, fmt.Sprintf("%s is already %s", def.Title, current), true)
		} else {
			if err := svc.Set(ctx, settings.ScopeUser, tx.UserID, ns, key, value, tx.UserID); err != nil {
				_ = tx.Answer(ctx, "Invalid setting value", true)
				return err
			}
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

	screen := NewScreen(ScreenIDSettings, "⚙️ "+categoryLabel(category), "Tap a setting to change it. Boolean and enum settings cycle directly; numeric settings use step controls. String settings remain command-editable to avoid an unsafe free-form input flow in callbacks.")
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

func nextSettingValue(def settings.SettingDefinition, current, op string) (string, bool, error) {
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
		if step <= 0 { step = 1 }
		v += step
		if def.MaxVal != nil && v > *def.MaxVal { v = *def.MinVal }
		return strconv.FormatInt(v, 10), true, nil
	case settings.TypeDuration:
		d, err := time.ParseDuration(current)
		if err != nil { return "", false, err }
		step := time.Duration(def.UI.Step) * time.Second
		if step <= 0 { step = 5 * time.Second }
		d += step
		if def.MaxVal != nil && int64(d/time.Second) > *def.MaxVal {
			if def.MinVal != nil { d = time.Duration(*def.MinVal) * time.Second }
		}
		return d.String(), true, nil
	case settings.TypeString:
		return "", false, fmt.Errorf("string settings require text input")
	default:
		return "", false, fmt.Errorf("unsupported setting type %q", def.Type)
	}
}

func displaySettingValue(def settings.SettingDefinition, value string) string {
	if def.Sensitive && value != "" { return "••••" }
	return value
}

func categoryLabel(category string) string {
	switch strings.ToLower(category) {
	case settings.CategoryGeneral: return "⚙️ General"
	case settings.CategorySecurity: return "🔒 Security"
	case settings.CategoryModeration: return "🛡 Moderation"
	case settings.CategoryAutomation: return "🤖 Automation"
	case settings.CategoryUI: return "🎨 Interface"
	case settings.CategoryAdvanced: return "🧰 Advanced"
	default: return "📁 " + category
	}
}
