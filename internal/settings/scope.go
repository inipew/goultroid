package settings

import (
	"errors"
	"fmt"
	"strings"
)

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
