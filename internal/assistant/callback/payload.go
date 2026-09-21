package callback

import (
	"fmt"
	"strings"
)

const (
	MaxCallbackDataLen = 64
	MaxFieldLen         = 32
	PayloadPrefixV1     = "v1"
)

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

// Encode builds the canonical v1 callback bridge payload. "noop" is used when
// the caller has no opaque state so every payload keeps one unambiguous shape.
func Encode(namespace, action string) (string, error) {
	return EncodeWithState(namespace, action, "noop")
}

func EncodeWithState(namespace, action, state string) (string, error) {
	if namespace == "" || action == "" {
		return "", ErrEmptyField
	}
	if !isValidField(namespace) || !isValidField(action) {
		return "", fmt.Errorf("%w: invalid characters in namespace or action", ErrMalformedPayload)
	}
	state = strings.TrimSpace(state)
	if state == "" {
		state = "noop"
	}
	res := fmt.Sprintf("%s:%s:%s:%s", PayloadPrefixV1, namespace, action, state)
	if len([]byte(res)) > MaxCallbackDataLen {
		return "", ErrPayloadTooLong
	}
	return res, nil
}

// Parse decodes the canonical non-a2 callback bridge payload. a2 tokens are
// intercepted by the orchestration ingress before this parser is reached.
func Parse(data []byte) (*ParsedPayload, error) {
	if len(data) == 0 {
		return nil, ErrMalformedPayload
	}
	if len(data) > MaxCallbackDataLen {
		return nil, ErrPayloadTooLong
	}

	parts := strings.SplitN(string(data), ":", 4)
	if len(parts) != 4 || parts[0] != PayloadPrefixV1 {
		return nil, fmt.Errorf("%w: unsupported callback payload", ErrMalformedPayload)
	}
	namespace, action, state := parts[1], parts[2], parts[3]
	if namespace == "" || action == "" || state == "" {
		return nil, ErrEmptyField
	}
	if !isValidField(namespace) || !isValidField(action) {
		return nil, fmt.Errorf("%w: invalid characters in payload field", ErrMalformedPayload)
	}
	return &ParsedPayload{
		Version:   PayloadPrefixV1,
		Namespace: namespace,
		Action:    action,
		State:     state,
	}, nil
}
