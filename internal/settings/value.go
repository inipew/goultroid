package settings

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SettingValue provides typed getters with fallback defaults around a resolved setting.
// It carries the associated definition when available so typed parsing is centralized.
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
