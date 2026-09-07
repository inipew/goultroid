package callback

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// MaxCallbackDataLen is the maximum byte length of a callback_data payload permitted by Telegram MTProto.
	MaxCallbackDataLen = 64
	// PayloadPrefixV2 is the version indicator for assistant v2 payloads.
	PayloadPrefixV2 = "a1"
	// PayloadPrefixV1 is the version indicator for legacy v1 payloads.
	PayloadPrefixV1 = "v1"
)

var (
	// ErrPayloadTooLong indicates callback data exceeds 64 bytes.
	ErrPayloadTooLong = errors.New("assistant/callback: payload exceeds 64 bytes limit")
	// ErrMalformedPayload indicates callback data does not follow the required prefix and separator format.
	ErrMalformedPayload = errors.New("assistant/callback: malformed callback payload")
	// ErrEmptyField indicates a required field in callback data is empty.
	ErrEmptyField = errors.New("assistant/callback: required field is empty")
)

// ParsedPayload contains the decoded parts of an incoming callback data string.
type ParsedPayload struct {
	Version   string
	Namespace string
	Action    string
	State     string
}

// Encode builds an a1 callback data string from namespace and action.
func Encode(namespace, action string) (string, error) {
	return EncodeWithState(namespace, action, "")
}

// EncodeWithState builds an a1 callback data string with an optional state token.
func EncodeWithState(namespace, action, state string) (string, error) {
	if namespace == "" || action == "" {
		return "", ErrEmptyField
	}
	var res string
	if state == "" {
		res = fmt.Sprintf("%s:%s:%s", PayloadPrefixV2, namespace, action)
	} else {
		res = fmt.Sprintf("%s:%s:%s:%s", PayloadPrefixV2, namespace, action, state)
	}

	if len([]byte(res)) > MaxCallbackDataLen {
		return "", ErrPayloadTooLong
	}
	return res, nil
}

// Parse decodes raw callback bytes, supporting both v2 (a1:...) and legacy (v1:...) formats.
func Parse(data []byte) (*ParsedPayload, error) {
	if len(data) == 0 {
		return nil, ErrMalformedPayload
	}
	if len(data) > MaxCallbackDataLen {
		return nil, ErrPayloadTooLong
	}

	s := string(data)
	parts := strings.Split(s, ":")
	if len(parts) < 3 {
		return nil, ErrMalformedPayload
	}

	version := parts[0]
	if version != PayloadPrefixV2 && version != PayloadPrefixV1 {
		return nil, fmt.Errorf("%w: unsupported payload version %q", ErrMalformedPayload, version)
	}

	namespace := parts[1]
	action := parts[2]
	if namespace == "" || action == "" {
		return nil, ErrEmptyField
	}

	state := ""
	if len(parts) >= 4 {
		state = strings.Join(parts[3:], ":")
	}

	return &ParsedPayload{
		Version:   version,
		Namespace: namespace,
		Action:    action,
		State:     state,
	}, nil
}
