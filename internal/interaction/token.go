package interaction

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	CallbackVersion      = "a2"
	MaxCallbackDataBytes = 64
	sessionIDBytes       = 16
	sessionIDLength      = 22
)

var (
	ErrInvalidCallbackToken = errors.New("interaction: invalid callback token")
	ErrCallbackDataTooLong  = errors.New("interaction: callback data exceeds telegram limit")
)

// CallbackToken is the compact, versioned reference carried by Telegram callback data.
// State stays server-side; Revision fences buttons rendered from older state.
type CallbackToken struct {
	Version   string
	FeatureID string
	ActionID  string
	SessionID string
	Revision  uint64
}

func newSessionID() (string, error) {
	var raw [sessionIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate interaction session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// EncodeCallbackToken encodes a2:<feature>:<action>:<session>.<revision-base36>.
func EncodeCallbackToken(featureID, actionID, sessionID string, revision uint64) ([]byte, error) {
	featureID = normalizeFeatureID(featureID)
	actionID = normalizeFeatureID(actionID)
	if !validIdentifier(featureID) || !validIdentifier(actionID) || !validSessionID(sessionID) || revision == 0 {
		return nil, ErrInvalidCallbackToken
	}
	opaque := sessionID + "." + strconv.FormatUint(revision, 36)
	raw := CallbackVersion + ":" + featureID + ":" + actionID + ":" + opaque
	if len(raw) > MaxCallbackDataBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrCallbackDataTooLong, len(raw))
	}
	return []byte(raw), nil
}

// ParseCallbackToken strictly parses the a2 protocol.
func ParseCallbackToken(data []byte) (CallbackToken, error) {
	if len(data) == 0 || len(data) > MaxCallbackDataBytes {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	parts := strings.Split(string(data), ":")
	if len(parts) != 4 || parts[0] != CallbackVersion {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	featureID, actionID := parts[1], parts[2]
	if !validIdentifier(featureID) || !validIdentifier(actionID) {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	opaqueParts := strings.Split(parts[3], ".")
	if len(opaqueParts) != 2 || !validSessionID(opaqueParts[0]) {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	revision, err := strconv.ParseUint(opaqueParts[1], 36, 64)
	if err != nil || revision == 0 {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	return CallbackToken{
		Version:   CallbackVersion,
		FeatureID: featureID,
		ActionID:  actionID,
		SessionID: opaqueParts[0],
		Revision:  revision,
	}, nil
}

func validSessionID(id string) bool {
	if len(id) != sessionIDLength {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(raw) == sessionIDBytes
}
