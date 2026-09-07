package callback

import (
	"fmt"
	"strings"
)

const (
	// MaxCallbackDataLen is the maximum byte length of a callback_data payload permitted by Telegram MTProto.
	MaxCallbackDataLen = 64
	// MaxFieldLen is the maximum character length for namespace and action identifiers.
	MaxFieldLen = 32
	// PayloadPrefixV2 is the version indicator for assistant v2 payloads.
	PayloadPrefixV2 = "a1"
	// PayloadPrefixV1 is the version indicator for legacy v1 payloads.
	PayloadPrefixV1 = "v1"
)

// ParsedPayload contains the decoded parts of an incoming callback data string.
type ParsedPayload struct {
	Version   string
	Namespace string
	Action    string
	State     string
}

func isValidField(s string) bool {
	if len(s) == 0 || len(s) > MaxFieldLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-' || c == '*' {
			continue
		}
		return false
	}
	return true
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
	if !isValidField(namespace) || !isValidField(action) {
		return "", fmt.Errorf("%w: invalid characters in namespace or action", ErrMalformedPayload)
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
	if !isValidField(namespace) || !isValidField(action) {
		return nil, fmt.Errorf("%w: invalid characters in payload field", ErrMalformedPayload)
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
