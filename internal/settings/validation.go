package settings

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

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
