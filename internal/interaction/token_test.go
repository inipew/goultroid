package interaction

import (
	"errors"
	"strings"
	"testing"
)

func TestCallbackTokenRoundTrip(t *testing.T) {
	id, err := newSessionID()
	if err != nil {
		t.Fatalf("newSessionID() error = %v", err)
	}
	data, err := EncodeCallbackToken("demo", "next", id, 35)
	if err != nil {
		t.Fatalf("EncodeCallbackToken() error = %v", err)
	}
	if len(data) > MaxCallbackDataBytes {
		t.Fatalf("callback data length = %d", len(data))
	}
	token, err := ParseCallbackToken(data)
	if err != nil {
		t.Fatalf("ParseCallbackToken() error = %v", err)
	}
	if token.FeatureID != "demo" || token.ActionID != "next" || token.SessionID != id || token.Revision != 35 {
		t.Fatalf("token = %+v", token)
	}
}

func TestCallbackTokenRejectsOversizedFeatureAction(t *testing.T) {
	id, err := newSessionID()
	if err != nil {
		t.Fatalf("newSessionID() error = %v", err)
	}
	_, err = EncodeCallbackToken(strings.Repeat("f", 30), strings.Repeat("a", 30), id, 1)
	if !errors.Is(err, ErrCallbackDataTooLong) {
		t.Fatalf("error = %v, want %v", err, ErrCallbackDataTooLong)
	}
}

func TestParseCallbackTokenRejectsLegacyAndMalformedPayload(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("a1:demo:next:deadbeef"),
		[]byte("a2:demo:next"),
		[]byte("a2:Demo:next:AAAAAAAAAAAAAAAAAAAAAA.1"),
		[]byte("a2:demo:next:bad.1"),
		[]byte("a2:demo:next:AAAAAAAAAAAAAAAAAAAAAA.0"),
	} {
		if _, err := ParseCallbackToken(data); !errors.Is(err, ErrInvalidCallbackToken) {
			t.Fatalf("ParseCallbackToken(%q) error = %v", data, err)
		}
	}
}
