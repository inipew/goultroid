package interaction

import (
	"context"
	"errors"
	"math"
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
	if token.FeatureID != "" || token.ActionID != "next" || token.SessionID != id || token.Revision != 35 {
		t.Fatalf("token = %+v", token)
	}
}

func TestCallbackTokenBudgetIsIndependentOfFeatureID(t *testing.T) {
	id, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeCallbackToken(strings.Repeat("f", 48), "next", id, math.MaxUint64)
	if err != nil {
		t.Fatalf("long server-side feature identity affected callback budget: %v", err)
	}
	if len(data) > MaxCallbackDataBytes {
		t.Fatalf("callback bytes=%d, max=%d", len(data), MaxCallbackDataBytes)
	}
}

func TestCallbackTokenRejectsActionThatCannotFitWorstCaseRevision(t *testing.T) {
	id, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	maxAction := strings.Repeat("a", MaxCallbackActionIDBytes)
	data, err := EncodeCallbackToken("demo", maxAction, id, math.MaxUint64)
	if err != nil {
		t.Fatalf("max action unexpectedly rejected: %v", err)
	}
	if len(data) != MaxCallbackDataBytes {
		t.Fatalf("max action callback bytes=%d, want %d", len(data), MaxCallbackDataBytes)
	}
	_, err = EncodeCallbackToken("demo", maxAction+"a", id, 1)
	if !errors.Is(err, ErrCallbackDataTooLong) {
		t.Fatalf("oversized action error=%v, want %v", err, ErrCallbackDataTooLong)
	}
}

func TestResolveCallbackRehydratesFeatureFromBoundSession(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCallbackToken(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.FeatureID != "" {
		t.Fatalf("wire token unexpectedly retained feature %q", parsed.FeatureID)
	}
	resolved, err := runtime.ResolveCallback(context.Background(), data, Binding{ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Token.FeatureID != "demo" {
		t.Fatalf("resolved feature=%q, want demo", resolved.Token.FeatureID)
	}
}

func TestParseCallbackTokenRejectsLegacyAndMalformedPayload(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("a1:next:deadbeef"),
		[]byte("a2:demo:next:AAAAAAAAAAAAAAAAAAAAAA.1"),
		[]byte("a2:Next:AAAAAAAAAAAAAAAAAAAAAA.1"),
		[]byte("a2:next:bad.1"),
		[]byte("a2:next:AAAAAAAAAAAAAAAAAAAAAA.0"),
	} {
		if _, err := ParseCallbackToken(data); !errors.Is(err, ErrInvalidCallbackToken) {
			t.Fatalf("ParseCallbackToken(%q) error = %v", data, err)
		}
	}
}
