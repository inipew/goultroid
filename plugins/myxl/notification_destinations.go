package myxl

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// MaxNotificationDestinations caps the number of configured notification targets.
const MaxNotificationDestinations = 5

const maxDestinationLabelRunes = 48

// NotificationDestinationType identifies the Telegram peer semantics of a destination.
type NotificationDestinationType string

const (
	DestOwner      NotificationDestinationType = "owner"
	DestGroup      NotificationDestinationType = "group"
	DestSupergroup NotificationDestinationType = "supergroup"
	DestGlobalLog  NotificationDestinationType = "global_log"
	DestBotPM      NotificationDestinationType = "bot_pm"
	DestChannel    NotificationDestinationType = "channel"
)

// NotificationDestination represents one bounded, typed notification target.
// Empty destination slices imply the default Owner PM fallback.
type NotificationDestination struct {
	ID         string                      `json:"id"`
	Type       NotificationDestinationType `json:"type"`
	Label      string                      `json:"label"`
	Enabled    bool                        `json:"enabled"`
	PeerID     int64                       `json:"peer_id,omitempty"`
	AccessHash int64                       `json:"access_hash,omitempty"`
}

func defaultLabelForDestinationType(t NotificationDestinationType) string {
	switch t {
	case DestOwner:
		return "Owner PM"
	case DestGlobalLog:
		return "Global Log"
	case DestGroup:
		return "Group"
	case DestSupergroup:
		return "Supergroup"
	case DestBotPM:
		return "Bot PM"
	case DestChannel:
		return "Channel"
	default:
		return "Destination"
	}
}

func sanitizeDestinationLabel(label string, destType NotificationDestinationType) (string, error) {
	trimmed := strings.TrimSpace(label)
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", errors.New("destination label contains control characters")
		}
	}
	if trimmed == "" {
		return defaultLabelForDestinationType(destType), nil
	}
	runes := []rune(trimmed)
	if len(runes) > maxDestinationLabelRunes {
		trimmed = string(runes[:maxDestinationLabelRunes])
	}
	return trimmed, nil
}

func normalizeChannelPeerID(raw int64) int64 {
	if raw >= 0 {
		return raw
	}
	s := strconv.FormatInt(raw, 10)
	if strings.HasPrefix(s, "-100") {
		if parsed, err := strconv.ParseInt(s[4:], 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return raw
}

// NormalizeAndValidateDestination normalizes equivalent representations and
// strictly validates type-specific peer fields.
func NormalizeAndValidateDestination(d NotificationDestination) (NotificationDestination, error) {
	d.ID = strings.TrimSpace(d.ID)
	if d.ID == "" {
		return NotificationDestination{}, errors.New("destination id is required")
	}
	for _, r := range d.ID {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return NotificationDestination{}, errors.New("destination id cannot contain whitespace or control characters")
		}
	}

	d.Type = NotificationDestinationType(strings.ToLower(strings.TrimSpace(string(d.Type))))
	label, err := sanitizeDestinationLabel(d.Label, d.Type)
	if err != nil {
		return NotificationDestination{}, err
	}
	d.Label = label

	switch d.Type {
	case DestOwner, DestGlobalLog:
		if d.PeerID != 0 || d.AccessHash != 0 {
			return NotificationDestination{}, fmt.Errorf("destination type %s must not specify static peer_id or access_hash", d.Type)
		}
	case DestGroup:
		if d.PeerID < 0 {
			if strings.HasPrefix(strconv.FormatInt(d.PeerID, 10), "-100") {
				return NotificationDestination{}, errors.New("basic group peer_id cannot use -100 supergroup prefix")
			}
			d.PeerID = -d.PeerID
		}
		if d.PeerID <= 0 {
			return NotificationDestination{}, errors.New("group destination requires a positive peer_id")
		}
		if d.AccessHash != 0 {
			return NotificationDestination{}, errors.New("basic group destination must not specify access_hash")
		}
	case DestSupergroup, DestChannel:
		d.PeerID = normalizeChannelPeerID(d.PeerID)
		if d.PeerID <= 0 {
			return NotificationDestination{}, fmt.Errorf("%s destination requires a valid positive channel peer_id", d.Type)
		}
		if d.AccessHash == 0 {
			return NotificationDestination{}, fmt.Errorf("%s destination requires a non-zero access_hash", d.Type)
		}
	case DestBotPM:
		if d.PeerID <= 0 {
			return NotificationDestination{}, errors.New("bot_pm destination requires a positive peer_id")
		}
		if d.AccessHash == 0 {
			return NotificationDestination{}, errors.New("bot_pm destination requires a non-zero access_hash")
		}
	default:
		return NotificationDestination{}, fmt.Errorf("unsupported notification destination type %q", d.Type)
	}

	return d, nil
}

func destinationIdentityKey(d NotificationDestination) string {
	switch d.Type {
	case DestOwner:
		return "owner"
	case DestGlobalLog:
		return "global_log"
	case DestGroup:
		return fmt.Sprintf("group:%d", d.PeerID)
	case DestSupergroup, DestChannel:
		return fmt.Sprintf("peer_channel:%d", d.PeerID)
	case DestBotPM:
		return fmt.Sprintf("bot_pm:%d", d.PeerID)
	default:
		return fmt.Sprintf("%s:%d", d.Type, d.PeerID)
	}
}

// NormalizeAndValidateDestinations enforces the MaxNotificationDestinations bound,
// per-item validation, and deterministic typed deduplication.
func NormalizeAndValidateDestinations(dests []NotificationDestination) ([]NotificationDestination, error) {
	if len(dests) > MaxNotificationDestinations {
		return nil, fmt.Errorf("too many notification destinations (%d): maximum is %d", len(dests), MaxNotificationDestinations)
	}
	if len(dests) == 0 {
		return []NotificationDestination{}, nil
	}

	seenIDs := make(map[string]struct{}, len(dests))
	seenIdentities := make(map[string]struct{}, len(dests))
	normalized := make([]NotificationDestination, 0, len(dests))

	for i, raw := range dests {
		norm, err := NormalizeAndValidateDestination(raw)
		if err != nil {
			return nil, fmt.Errorf("destination[%d]: %w", i, err)
		}
		if _, dupID := seenIDs[norm.ID]; dupID {
			return nil, fmt.Errorf("duplicate destination id %q", norm.ID)
		}
		seenIDs[norm.ID] = struct{}{}

		ident := destinationIdentityKey(norm)
		if _, dupIdent := seenIdentities[ident]; dupIdent {
			return nil, fmt.Errorf("duplicate notification destination (%s)", ident)
		}
		seenIdentities[ident] = struct{}{}

		normalized = append(normalized, norm)
	}
	return normalized, nil
}

// EncodeNotificationDestinations validates, normalizes, and encodes destinations deterministically.
func EncodeNotificationDestinations(dests []NotificationDestination) (string, error) {
	normalized, err := NormalizeAndValidateDestinations(dests)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode notification destinations: %w", err)
	}
	return string(data), nil
}

// DecodeNotificationDestinations strictly decodes and validates persisted destinations JSON.
// Malformed JSON or invalid destination items always return an explicit error.
func DecodeNotificationDestinations(raw string) ([]NotificationDestination, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("notification destinations json is empty")
	}
	var dests []NotificationDestination
	if err := json.Unmarshal([]byte(trimmed), &dests); err != nil {
		return nil, fmt.Errorf("decode notification destinations: %w", err)
	}
	if dests == nil {
		return nil, errors.New("notification destinations json must be an array, not null")
	}
	return NormalizeAndValidateDestinations(dests)
}
