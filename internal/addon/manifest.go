package addon

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

var (
	validAddonNameRegex   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)
	validCommandNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// ParseManifest parses a YAML or JSON manifest byte slice.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: manifest content is empty", ErrInvalidManifest)
	}

	var m Manifest
	// Try YAML first (which is a superset of JSON)
	if err := yaml.Unmarshal(data, &m); err != nil {
		// Fallback to JSON
		if errJSON := json.Unmarshal(data, &m); errJSON != nil {
			return nil, fmt.Errorf("%w: failed to parse manifest as YAML or JSON: %v", ErrInvalidManifest, err)
		}
	}

	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}

	return &m, nil
}

// ValidateManifest checks all fields of a Manifest for correctness.
func ValidateManifest(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("%w: manifest is nil", ErrInvalidManifest)
	}

	m.Name = strings.ToLower(strings.TrimSpace(m.Name))
	if !validAddonNameRegex.MatchString(m.Name) {
		return fmt.Errorf("%w: addon name %q must be alphanumeric with dashes/underscores (2-64 chars)", ErrInvalidManifest, m.Name)
	}

	m.Version = strings.TrimSpace(m.Version)
	if m.Version == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidManifest)
	}

	m.MinGoUltroid = strings.TrimSpace(m.MinGoUltroid)

	if len(m.Commands) == 0 && len(m.Events) == 0 {
		return fmt.Errorf("%w: addon must declare at least one command or event", ErrInvalidManifest)
	}

	seenCaps := make(map[Capability]struct{}, len(m.Capabilities))
	for i, capability := range m.Capabilities {
		if !ValidCapabilities[capability] {
			return fmt.Errorf("%w: unknown or unauthorized capability %q", ErrInvalidManifest, capability)
		}
		if _, exists := seenCaps[capability]; exists {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidManifest, capability)
		}
		seenCaps[capability] = struct{}{}
		m.Capabilities[i] = capability
	}

	seenCommands := make(map[string]struct{}, len(m.Commands))
	for i, command := range m.Commands {
		command = strings.ToLower(strings.TrimSpace(command))
		if !validCommandNameRegex.MatchString(command) {
			return fmt.Errorf("%w: invalid command name %q", ErrInvalidManifest, command)
		}
		if _, exists := seenCommands[command]; exists {
			return fmt.Errorf("%w: duplicate command %q", ErrInvalidManifest, command)
		}
		seenCommands[command] = struct{}{}
		m.Commands[i] = command
	}

	seenEvents := make(map[EventType]struct{}, len(m.Events))
	for i, eventType := range m.Events {
		eventType = EventType(strings.TrimSpace(string(eventType)))
		required, ok := eventCapability(eventType)
		if !ok {
			return fmt.Errorf("%w: unsupported event %q", ErrInvalidManifest, eventType)
		}
		if _, exists := seenEvents[eventType]; exists {
			return fmt.Errorf("%w: duplicate event %q", ErrInvalidManifest, eventType)
		}
		if _, declared := seenCaps[required]; !declared {
			return fmt.Errorf("%w: event %q requires capability %q", ErrInvalidManifest, eventType, required)
		}
		seenEvents[eventType] = struct{}{}
		m.Events[i] = eventType
	}

	return nil
}

// CheckCompatibility compares the addon's MinGoUltroid requirement against the running version.
func CheckCompatibility(m *Manifest, appVersion string) error {
	if m.MinGoUltroid == "" {
		return nil // No minimum version constraint
	}

	cleanApp := strings.TrimPrefix(strings.TrimSpace(appVersion), "v")
	cleanMin := strings.TrimPrefix(strings.TrimSpace(m.MinGoUltroid), "v")

	appParts := parseVersion(cleanApp)
	minParts := parseVersion(cleanMin)

	for i := 0; i < 3; i++ {
		if appParts[i] < minParts[i] {
			return fmt.Errorf("%w: requires GoUltroid >= %s, current is %s", ErrIncompatibleVersion, m.MinGoUltroid, appVersion)
		}
		if appParts[i] > minParts[i] {
			return nil
		}
	}

	return nil
}

func parseVersion(v string) [3]int {
	var parts [3]int
	split := strings.Split(v, ".")
	for i := 0; i < len(split) && i < 3; i++ {
		// Strip prerelease tags e.g. "0-beta" -> "0"
		clean := strings.Split(split[i], "-")[0]
		if num, err := strconv.Atoi(clean); err == nil {
			parts[i] = num
		}
	}
	return parts
}
