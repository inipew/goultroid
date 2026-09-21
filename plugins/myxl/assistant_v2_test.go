package myxl

import (
	"strings"
	"testing"

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
	if len(spec.Interactions) != assistantV2SlotCount+2 {
		t.Fatalf("interaction count = %d, want %d", len(spec.Interactions), assistantV2SlotCount+2)
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

	raw, view, err := p.assistantV2Screen(assistantV2State{}, screen)
	if err != nil {
		t.Fatalf("compile screen: %v", err)
	}
	if len(view.Rows) != 1 || len(view.Rows[0]) != 1 {
		t.Fatalf("unexpected rows: %#v", view.Rows)
	}
	if got := view.Rows[0][0].ActionID; got != assistantV2SlotID(0) {
		t.Fatalf("action id = %q, want %q", got, assistantV2SlotID(0))
	}
	if strings.Contains(view.Text, "a1:") || strings.Contains(view.Text, "myxl:accounts") {
		t.Fatalf("transport view leaked internal action intent: %q", view.Text)
	}

	state := decodeAssistantV2State(raw)
	if len(state.Slots) != 1 || state.Slots[0] != "myxl:accounts" {
		t.Fatalf("session slots = %#v", state.Slots)
	}
}

func TestAssistantV2IntentParserRejectsLegacyA1Envelope(t *testing.T) {
	namespace, action, opaque, err := parseAssistantV2Template("myxl:method:balance:key")
	if err != nil {
		t.Fatalf("parse current intent: %v", err)
	}
	if namespace != "myxl" || action != "method" || opaque != "balance:key" {
		t.Fatalf("parsed = %q %q %q", namespace, action, opaque)
	}

	if _, _, _, err := parseAssistantV2Template("a1:myxl:home"); err == nil {
		t.Fatal("expected malformed legacy envelope to fail")
	}
}

func TestAssistantV2ActionDeclarationsAreTyped(t *testing.T) {
	p := &Plugin{}
	spec := p.FeatureSpec()
	for i := 0; i < assistantV2SlotCount; i++ {
		id := assistantV2SlotID(i)
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
