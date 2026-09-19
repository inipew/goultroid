package app

import (
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/settings"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type afkLiveSettings interface {
	SetAutoReply(bool)
	SetCooldown(time.Duration)
}

func buildLiveSettingsBinder(coreDeps *coreDependencies, dom *domainServices, plugins *plugin.Manager, logLevel zap.AtomicLevel) (*settings.LiveBinder, error) {
	if coreDeps == nil || coreDeps.router == nil || coreDeps.eventBus == nil {
		return nil, fmt.Errorf("settings live binder requires core router and event bus")
	}
	if dom == nil || dom.settingsService == nil {
		return nil, fmt.Errorf("settings live binder requires settings service")
	}

	binder := settings.NewLiveBinder(dom.settingsService, coreDeps.eventBus)
	if err := binder.Bind("core", "prefix", func(value settings.SettingValue) error {
		return coreDeps.router.SetPrefix(value.String())
	}); err != nil {
		return nil, err
	}
	if err := binder.Bind("debug", "log_level", func(value settings.SettingValue) error {
		raw, err := value.EnumE()
		if err != nil {
			return err
		}
		var level zapcore.Level
		if err := level.UnmarshalText([]byte(raw)); err != nil {
			return err
		}
		logLevel.SetLevel(level)
		return nil
	}); err != nil {
		return nil, err
	}

	if dom.pmpermitService != nil {
		if err := binder.Bind("pmpermit", "enabled", func(value settings.SettingValue) error {
			enabled, err := value.BoolE()
			if err != nil {
				return err
			}
			dom.pmpermitService.SetEnabled(enabled)
			return nil
		}); err != nil {
			return nil, err
		}
		if err := binder.Bind("pmpermit", "max_warns", func(value settings.SettingValue) error {
			maxWarns, err := value.IntE()
			if err != nil {
				return err
			}
			if maxWarns <= 0 {
				return fmt.Errorf("pmpermit max_warns must be positive")
			}
			dom.pmpermitService.SetMaxWarns(int(maxWarns))
			return nil
		}); err != nil {
			return nil, err
		}
	}

	if plugins != nil {
		if p, ok := plugins.Find("afk"); ok {
			afkSettings, ok := p.(afkLiveSettings)
			if !ok {
				return nil, fmt.Errorf("afk plugin does not expose live settings")
			}
			if err := binder.Bind("afk", "auto_reply", func(value settings.SettingValue) error {
				enabled, err := value.BoolE()
				if err != nil {
					return err
				}
				afkSettings.SetAutoReply(enabled)
				return nil
			}); err != nil {
				return nil, err
			}
			if err := binder.Bind("afk", "cooldown", func(value settings.SettingValue) error {
				cooldown, err := value.DurationE()
				if err != nil {
					return err
				}
				afkSettings.SetCooldown(cooldown)
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	return binder, nil
}
