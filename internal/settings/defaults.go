package settings

// RegisterDefaultDefinitions populates the registry with standard GoUltroid settings.
func RegisterDefaultDefinitions(reg *Registry) error {
	minWarns, maxWarns := int64(1), int64(20)
	minFlood, maxFlood := int64(1), int64(50)
	minCooldown, maxCooldown := int64(0), int64(3600) // in seconds
	minPageSize, maxPageSize := int64(3), int64(20)

	defs := []SettingDefinition{
		// General
		{
			Namespace:    "core",
			Key:          "prefix",
			Type:         TypeString,
			DefaultValue: ".",
			Title:        "Command Prefix",
			Description:  "Prefix symbol used to trigger userbot commands",
			Category:     CategoryGeneral,
		},
		{
			Namespace:    "core",
			Key:          "timezone",
			Type:         TypeString,
			DefaultValue: "UTC",
			Title:        "Timezone",
			Description:  "Timezone used for formatting timestamps and schedules",
			Category:     CategoryGeneral,
		},

		// Security
		{
			Namespace:    "pmpermit",
			Key:          "enabled",
			Type:         TypeBool,
			DefaultValue: "true",
			Title:        "PM Guard Protection",
			Description:  "Enable or disable private message antispam guardian",
			Category:     CategorySecurity,
		},
		{
			Namespace:    "pmpermit",
			Key:          "max_warns",
			Type:         TypeInt,
			DefaultValue: "3",
			MinVal:       &minWarns,
			MaxVal:       &maxWarns,
			Title:        "PM Max Warnings",
			Description:  "Number of warnings sent before spammer is blocked",
			Category:     CategorySecurity,
		},
		{
			Namespace:    "pmpermit",
			Key:          "flood_limit",
			Type:         TypeInt,
			DefaultValue: "5",
			MinVal:       &minFlood,
			MaxVal:       &maxFlood,
			Title:        "PM Flood Limit",
			Description:  "Max messages within 5 seconds before trigger",
			Category:     CategorySecurity,
		},

		// Moderation
		{
			Namespace:    "antispam",
			Key:          "enabled",
			Type:         TypeBool,
			DefaultValue: "false",
			Title:        "Group Antispam",
			Description:  "Automated spam detection in group chats",
			Category:     CategoryModeration,
		},
		{
			Namespace:     "antispam",
			Key:           "action",
			Type:          TypeEnum,
			DefaultValue:  "warn",
			AllowedValues: []string{"warn", "mute", "kick", "ban"},
			Title:         "Antispam Action",
			Description:   "Punishment action executed upon spam detection",
			Category:      CategoryModeration,
		},

		// Automation
		{
			Namespace:    "afk",
			Key:          "auto_reply",
			Type:         TypeBool,
			DefaultValue: "true",
			Title:        "AFK Auto Reply",
			Description:  "Automatically respond to mentions while AFK",
			Category:     CategoryAutomation,
		},
		{
			Namespace:    "afk",
			Key:          "cooldown",
			Type:         TypeDuration,
			DefaultValue: "5s",
			MinVal:       &minCooldown,
			MaxVal:       &maxCooldown,
			Title:        "AFK Reply Cooldown",
			Description:  "Cooldown between AFK reply notifications to the same peer",
			Category:     CategoryAutomation,
		},

		// UI
		{
			Namespace:    "ui",
			Key:          "page_size",
			Type:         TypeInt,
			DefaultValue: "6",
			MinVal:       &minPageSize,
			MaxVal:       &maxPageSize,
			Title:        "UI Items Per Page",
			Description:  "Default number of list items shown per menu screen",
			Category:     CategoryUI,
		},
		{
			Namespace:    "ui",
			Key:          "inline_buttons",
			Type:         TypeBool,
			DefaultValue: "true",
			Title:        "Interactive Inline Buttons",
			Description:  "Use Telegram inline buttons for interactive menus",
			Category:     CategoryUI,
		},

		// Advanced
		{
			Namespace:     "debug",
			Key:           "log_level",
			Type:          TypeEnum,
			DefaultValue:  "info",
			AllowedValues: []string{"debug", "info", "warn", "error"},
			Title:         "System Log Level",
			Description:   "Granularity of system logging output",
			Category:      CategoryAdvanced,
		},
	}

	for _, def := range defs {
		if err := reg.Register(def); err != nil {
			return err
		}
	}
	return nil
}
