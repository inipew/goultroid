package settings

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SettingType denotes the data type of a setting value.
type SettingType string

const (
	TypeBool     SettingType = "bool"
	TypeInt      SettingType = "int"
	TypeString   SettingType = "string"
	TypeDuration SettingType = "duration"
	TypeEnum     SettingType = "enum"
)

// SettingScope defines the target context hierarchy.
type SettingScope string

const (
	ScopeGlobal SettingScope = "global"
	ScopeChat   SettingScope = "chat"
	ScopeUser   SettingScope = "user"
)

// Standard category names for UI grouping.
const (
	CategoryGeneral    = "general"
	CategorySecurity   = "security"
	CategoryModeration = "moderation"
	CategoryAutomation = "automation"
	CategoryUI         = "ui"
	CategoryAdvanced   = "advanced"
)

// WidgetType hints how UI should render a setting.
type WidgetType string

const (
	WidgetToggle   WidgetType = "toggle"
	WidgetStepper  WidgetType = "stepper"
	WidgetDuration WidgetType = "duration"
	WidgetSelector WidgetType = "selector"
	WidgetText     WidgetType = "text"
)

// UIHint describes how a setting should be presented in Telegram UI.
type UIHint struct {
	Widget     WidgetType
	Step       int64
	Presets    []string
	Confirm    bool
	Searchable bool
}

// SettingDefinition holds the schema, validation rules, and metadata for a setting.
type SettingDefinition struct {
	Namespace     string
	Key           string
	Type          SettingType
	DefaultValue  string
	AllowedValues []string // For TypeEnum
	MinVal        *int64   // For TypeInt or TypeDuration (seconds)
	MaxVal        *int64   // For TypeInt or TypeDuration (seconds)
	Title         string
	Description   string
	Category      string
	Validator     func(val string) error
	UI            UIHint
	Order         int
	Sensitive     bool
}

// Validate checks whether a given string value complies with the definition schema.
func (d *SettingDefinition) Validate(val string) error {
	switch d.Type {
	case TypeBool:
		lower := strings.ToLower(strings.TrimSpace(val))
		if lower != "true" && lower != "false" && lower != "1" && lower != "0" && lower != "yes" && lower != "no" && lower != "on" && lower != "off" {
			return fmt.Errorf("invalid boolean value %q (must be true/false/1/0/yes/no/on/off)", val)
		}

	case TypeInt:
		parsed, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer value %q: %w", val, err)
		}
		if d.MinVal != nil && parsed < *d.MinVal {
			return fmt.Errorf("value %d is below minimum allowed %d", parsed, *d.MinVal)
		}
		if d.MaxVal != nil && parsed > *d.MaxVal {
			return fmt.Errorf("value %d is above maximum allowed %d", parsed, *d.MaxVal)
		}

	case TypeDuration:
		dur, err := time.ParseDuration(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("invalid duration format %q (e.g. 5s, 10m, 1h): %w", val, err)
		}
		durSec := int64(dur.Seconds())
		if d.MinVal != nil && durSec < *d.MinVal {
			return fmt.Errorf("duration %s is below minimum allowed %ds", dur, *d.MinVal)
		}
		if d.MaxVal != nil && durSec > *d.MaxVal {
			return fmt.Errorf("duration %s is above maximum allowed %ds", dur, *d.MaxVal)
		}

	case TypeEnum:
		trimmed := strings.TrimSpace(val)
		found := false
		for _, allowed := range d.AllowedValues {
			if strings.EqualFold(trimmed, allowed) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("invalid choice %q (allowed: %s)", trimmed, strings.Join(d.AllowedValues, ", "))
		}

	case TypeString:
		// Any string is structurally acceptable

	default:
		return fmt.Errorf("unknown setting type: %s", d.Type)
	}

	if d.Validator != nil {
		if err := d.Validator(val); err != nil {
			return fmt.Errorf("custom validation failed: %w", err)
		}
	}

	return nil
}

// Canonicalize formats value into canonical representation (e.g. boolean to "true"/"false").
func (d *SettingDefinition) Canonicalize(val string) (string, error) {
	val = strings.TrimSpace(val)
	if err := d.Validate(val); err != nil {
		return "", err
	}

	switch d.Type {
	case TypeBool:
		lower := strings.ToLower(val)
		if lower == "true" || lower == "1" || lower == "yes" || lower == "on" {
			return "true", nil
		}
		return "false", nil

	case TypeInt:
		parsed, _ := strconv.ParseInt(val, 10, 64)
		return strconv.FormatInt(parsed, 10), nil

	case TypeDuration:
		dur, _ := time.ParseDuration(val)
		return dur.String(), nil

	case TypeEnum:
		for _, allowed := range d.AllowedValues {
			if strings.EqualFold(val, allowed) {
				return allowed, nil
			}
		}
		return val, nil

	default:
		return val, nil
	}
}

// NormalizeScope validates and normalizes scope string.
func NormalizeScope(scope string) (SettingScope, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "global", "g":
		return ScopeGlobal, nil
	case "chat", "c":
		return ScopeChat, nil
	case "user", "u":
		return ScopeUser, nil
	default:
		return "", errors.New("invalid scope: must be 'global', 'chat', or 'user'")
	}
}

// ScopeRef identifies an explicit settings context hierarchy target with its ID.
type ScopeRef struct {
	Type SettingScope
	ID   int64
}

// Validate checks whether ScopeRef specifies a valid type and consistent ID.
func (s ScopeRef) Validate() error {
	switch s.Type {
	case ScopeGlobal:
		if s.ID != 0 {
			return errors.New("global scope must have ID 0")
		}
	case ScopeChat:
		if s.ID == 0 {
			return errors.New("chat scope must have non-zero chat ID")
		}
	case ScopeUser:
		if s.ID == 0 {
			return errors.New("user scope must have non-zero user ID")
		}
	default:
		return fmt.Errorf("unknown scope type: %s", s.Type)
	}
	return nil
}

// GlobalScope returns a ScopeRef for global bot settings.
func GlobalScope() ScopeRef {
	return ScopeRef{Type: ScopeGlobal, ID: 0}
}

// UserScope returns a ScopeRef for user-specific settings.
func UserScope(userID int64) ScopeRef {
	return ScopeRef{Type: ScopeUser, ID: userID}
}

// ChatScope returns a ScopeRef for chat-specific settings.
func ChatScope(chatID int64) ScopeRef {
	return ScopeRef{Type: ScopeChat, ID: chatID}
}

// SettingValue provides typed getters with fallback defaults around a resolved setting.
// It now carries the associated definition when available so typed parsing is centralized.
type SettingValue struct {
	raw string
	err error
	def *SettingDefinition
}

// NewSettingValue creates a SettingValue wrapping a raw string and optional error.
func NewSettingValue(val string, err error) SettingValue {
	return SettingValue{raw: val, err: err}
}

// NewSettingValueWithDef creates a SettingValue with definition awareness for typed parsing.
func NewSettingValueWithDef(val string, err error, def *SettingDefinition) SettingValue {
	return SettingValue{raw: val, err: err, def: def}
}

// Raw returns the underlying raw string value.
func (v SettingValue) Raw() string {
	return v.raw
}

// Err returns any error encountered during value resolution.
func (v SettingValue) Err() error {
	return v.err
}

// String returns the setting as a string.
func (v SettingValue) String() string {
	return v.raw
}

// Definition returns the associated definition if any.
func (v SettingValue) Definition() *SettingDefinition {
	return v.def
}

// Bool returns the setting parsed as a boolean (lossy, for backward compat).
func (v SettingValue) Bool() bool {
	b, _ := v.BoolE()
	return b
}

// BoolE returns the setting parsed as boolean with validation via definition when available.
func (v SettingValue) BoolE() (bool, error) {
	if v.err != nil {
		return false, v.err
	}
	if v.def != nil && v.def.Type == TypeBool {
		canonical, err := v.def.Canonicalize(v.raw)
		if err != nil {
			return false, err
		}
		return canonical == "true", nil
	}
	lower := strings.ToLower(strings.TrimSpace(v.raw))
	switch lower {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q", v.raw)
	}
}

// Int returns the setting parsed as an int64 (lossy).
func (v SettingValue) Int() int64 {
	i, _ := v.IntE()
	return i
}

// IntE returns the setting parsed as int64 with definition validation.
func (v SettingValue) IntE() (int64, error) {
	if v.err != nil {
		return 0, v.err
	}
	if strings.TrimSpace(v.raw) == "" {
		return 0, nil
	}
	if v.def != nil && v.def.Type == TypeInt {
		canonical, err := v.def.Canonicalize(v.raw)
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(canonical), 10, 64)
	}
	val, err := strconv.ParseInt(strings.TrimSpace(v.raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer value %q: %w", v.raw, err)
	}
	if v.def != nil {
		if v.def.MinVal != nil && val < *v.def.MinVal {
			return 0, fmt.Errorf("value %d is below minimum allowed %d", val, *v.def.MinVal)
		}
		if v.def.MaxVal != nil && val > *v.def.MaxVal {
			return 0, fmt.Errorf("value %d is above maximum allowed %d", val, *v.def.MaxVal)
		}
	}
	return val, nil
}

// Duration returns the setting parsed as a time.Duration (lossy).
func (v SettingValue) Duration() time.Duration {
	d, _ := v.DurationE()
	return d
}

// DurationE returns the setting parsed as time.Duration with definition validation.
func (v SettingValue) DurationE() (time.Duration, error) {
	if v.err != nil {
		return 0, v.err
	}
	if strings.TrimSpace(v.raw) == "" {
		return 0, nil
	}
	if v.def != nil && v.def.Type == TypeDuration {
		canonical, err := v.def.Canonicalize(v.raw)
		if err != nil {
			return 0, err
		}
		return time.ParseDuration(strings.TrimSpace(canonical))
	}
	dur, err := time.ParseDuration(strings.TrimSpace(v.raw))
	if err != nil {
		return 0, fmt.Errorf("invalid duration value %q: %w", v.raw, err)
	}
	return dur, nil
}

// Enum returns canonical enum value or raw fallback.
func (v SettingValue) Enum() string {
	s, _ := v.EnumE()
	return s
}

// EnumE returns canonical enum value with validation.
func (v SettingValue) EnumE() (string, error) {
	if v.err != nil {
		return "", v.err
	}
	if v.def != nil && v.def.Type == TypeEnum {
		canonical, err := v.def.Canonicalize(v.raw)
		if err != nil {
			return "", err
		}
		return canonical, nil
	}
	return strings.TrimSpace(v.raw), nil
}

