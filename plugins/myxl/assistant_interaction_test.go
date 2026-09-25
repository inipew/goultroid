package myxl

import (
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/ui"
)

func TestAssistantV2FeatureSpecIsPrivateOwnerOnly(t *testing.T) {
	p := &Plugin{}
	spec := p.FeatureSpec()

	if spec.ID != "myxl" {
		t.Fatalf("feature id = %q, want myxl", spec.ID)
	}
	if len(spec.Interactions) != assistantActionSlotCount+2 {
		t.Fatalf("interaction count = %d, want %d", len(spec.Interactions), assistantActionSlotCount+2)
	}

	for _, interaction := range spec.Interactions {
		if !interaction.Surfaces.Supports(execution.SourceAssistant) {
			t.Fatalf("%s does not expose Assistant", interaction.ID)
		}
		if !interaction.Policy.PrivateOnly {
			t.Fatalf("%s is not private-only", interaction.ID)
		}
		if interaction.Policy.Permission != core.PermissionOwner {
			t.Fatalf("%s permission = %v, want owner", interaction.ID, interaction.Policy.Permission)
		}
		if interaction.Policy.Invocation.Assistant != core.InvocationSelfOnly {
			t.Fatalf("%s assistant invocation = %v, want self-only", interaction.ID, interaction.Policy.Invocation.Assistant)
		}
	}
}

func TestAssistantV2ScreenCompilesIntentIntoSessionBoundSlot(t *testing.T) {
	p := &Plugin{}
	screen := ui.NewScreen("myxl", "", "MyXL")
	screen.AddRow(ui.NewCallbackButton("Accounts", []byte("myxl:accounts")))

	raw, view, err := p.assistantScreen(assistantState{}, screen)
	if err != nil {
		t.Fatalf("compile screen: %v", err)
	}
	if len(view.Rows) != 1 || len(view.Rows[0]) != 1 {
		t.Fatalf("unexpected rows: %#v", view.Rows)
	}
	if got := view.Rows[0][0].ActionID; got != assistantSlotID(0) {
		t.Fatalf("action id = %q, want %q", got, assistantSlotID(0))
	}
	if strings.Contains(view.Text, "a1:") || strings.Contains(view.Text, "myxl:accounts") {
		t.Fatalf("transport view leaked internal action intent: %q", view.Text)
	}

	state := decodeAssistantState(raw)
	if len(state.Slots) != 1 || state.Slots[0] != "myxl:accounts" {
		t.Fatalf("session slots = %#v", state.Slots)
	}
}

func TestAssistantV2IntentParserRejectsLegacyA1Envelope(t *testing.T) {
	namespace, action, opaque, err := parseAssistantIntent("myxl:method:balance:key")
	if err != nil {
		t.Fatalf("parse current intent: %v", err)
	}
	if namespace != "myxl" || action != "method" || opaque != "balance:key" {
		t.Fatalf("parsed = %q %q %q", namespace, action, opaque)
	}

	if _, _, _, err := parseAssistantIntent("a1:myxl:home"); err == nil {
		t.Fatal("expected malformed legacy envelope to fail")
	}
}

func TestAssistantV2ActionDeclarationsAreTyped(t *testing.T) {
	p := &Plugin{}
	spec := p.FeatureSpec()
	for i := 0; i < assistantActionSlotCount; i++ {
		id := assistantSlotID(i)
		found := false
		for _, interaction := range spec.Interactions {
			if interaction.Kind == feature.InteractionAction && interaction.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing typed action declaration %s", id)
		}
	}
}

func TestAssistantV2OwnerBoundLifetimePolicy(t *testing.T) {
	if assistantTTL != 24*time.Hour {
		t.Fatalf("assistantTTL = %v, want 24h", assistantTTL)
	}
	if myxlCallbackTTL != 24*time.Hour {
		t.Fatalf("myxlCallbackTTL = %v, want 24h", myxlCallbackTTL)
	}
	if assistantInputTTL != 2*time.Minute {
		t.Fatalf("assistantInputTTL = %v, want 2m", assistantInputTTL)
	}
	if assistantConfirmationTTL != 5*time.Minute {
		t.Fatalf("assistantConfirmationTTL = %v, want 5m", assistantConfirmationTTL)
	}
	if pendingQRISTTL != 5*time.Minute {
		t.Fatalf("pendingQRISTTL = %v, want 5m", pendingQRISTTL)
	}
	long := decodeAssistantState(mustAssistantState(t, assistantState{Sustain: true}))
	if !long.Sustain {
		t.Fatal("long-lived owner navigation lost sustain marker")
	}
	short := decodeAssistantState(mustAssistantState(t, assistantState{Sustain: false}))
	if short.Sustain {
		t.Fatal("short-lived confirmation/input state became sustainable")
	}
}

func mustAssistantState(t *testing.T, state assistantState) []byte {
	t.Helper()
	raw, err := encodeAssistantState(state)
	if err != nil {
		t.Fatalf("encodeAssistantState() error = %v", err)
	}
	return raw
}
