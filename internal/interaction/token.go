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
	CallbackVersion                = "a2"
	MaxCallbackDataBytes           = 64
	sessionIDBytes                 = 16
	sessionIDLength                = 22
	maxCallbackRevisionBase36Bytes = 13
	callbackTokenFixedBytes        = len(CallbackVersion) + 2 + sessionIDLength + 1 + maxCallbackRevisionBase36Bytes
	MaxCallbackActionIDBytes       = MaxCallbackDataBytes - callbackTokenFixedBytes
)

var (
	ErrInvalidCallbackToken = errors.New("interaction: invalid callback token")
	ErrCallbackDataTooLong  = errors.New("interaction: callback data exceeds telegram limit")
)

// CallbackToken is the compact, versioned reference carried by Telegram callback data.
// FeatureID is resolved from server-side session state and is intentionally not
// serialized into Telegram's 64-byte callback payload.
type CallbackToken struct {
	Version   string
	FeatureID string
	ActionID  string
	SessionID string
	Revision  uint64
}

// ValidateCallbackActionID proves that an action identifier can fit in the a2
// callback payload even at the maximum uint64 revision.
func ValidateCallbackActionID(actionID string) error {
	actionID = normalizeFeatureID(actionID)
	if !validIdentifier(actionID) {
		return ErrInvalidCallbackToken
	}
	if len(actionID) > MaxCallbackActionIDBytes {
		return fmt.Errorf("%w: action id %q needs %d bytes, max %d", ErrCallbackDataTooLong, actionID, len(actionID), MaxCallbackActionIDBytes)
	}
	return nil
}

func newSessionID() (string, error) {
	var raw [sessionIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate interaction session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// EncodeCallbackToken encodes a2:<action>:<session>.<revision-base36>.
// featureID remains an argument so callers must still present a valid feature
// identity, but the canonical feature is recovered from the session at resolve.
func EncodeCallbackToken(featureID, actionID, sessionID string, revision uint64) ([]byte, error) {
	featureID = normalizeFeatureID(featureID)
	actionID = normalizeFeatureID(actionID)
	if !validIdentifier(featureID) || !validSessionID(sessionID) || revision == 0 {
		return nil, ErrInvalidCallbackToken
	}
	if err := ValidateCallbackActionID(actionID); err != nil {
		return nil, err
	}
	opaque := sessionID + "." + strconv.FormatUint(revision, 36)
	raw := CallbackVersion + ":" + actionID + ":" + opaque
	if len(raw) > MaxCallbackDataBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrCallbackDataTooLong, len(raw))
	}
	return []byte(raw), nil
}

// ParseCallbackToken strictly parses the compact a2 protocol.
func ParseCallbackToken(data []byte) (CallbackToken, error) {
	if len(data) == 0 || len(data) > MaxCallbackDataBytes {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	parts := strings.Split(string(data), ":")
	if len(parts) != 3 || parts[0] != CallbackVersion {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	actionID := parts[1]
	if err := ValidateCallbackActionID(actionID); err != nil {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	opaqueParts := strings.Split(parts[2], ".")
	if len(opaqueParts) != 2 || !validSessionID(opaqueParts[0]) {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	revision, err := strconv.ParseUint(opaqueParts[1], 36, 64)
	if err != nil || revision == 0 {
		return CallbackToken{}, ErrInvalidCallbackToken
	}
	return CallbackToken{
		Version:   CallbackVersion,
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
