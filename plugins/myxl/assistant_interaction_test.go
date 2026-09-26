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
	assistantCount := 0
	for _, interaction := range spec.Interactions {
		if !interaction.Surfaces.Supports(execution.SourceAssistant) {
			continue
		}
		assistantCount++
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
	if want := assistantActionSlotCount + 3; assistantCount != want {
		t.Fatalf("Assistant interaction count = %d, want %d", assistantCount, want)
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
	if assistantInputTTL != 2*time.Minute {
		t.Fatalf("assistantInputTTL = %v, want 2m", assistantInputTTL)
	}
	if assistantConfirmationTTL != 5*time.Minute {
		t.Fatalf("assistantConfirmationTTL = %v, want 5m", assistantConfirmationTTL)
	}
	if purchaseProcessingTTL != 2*time.Minute {
		t.Fatalf("purchaseProcessingTTL = %v, want 2m", purchaseProcessingTTL)
	}
	if assistantPurchaseConfirmExec != 65*time.Second {
		t.Fatalf("assistantPurchaseConfirmExec = %v, want 65s", assistantPurchaseConfirmExec)
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

func TestP4AssistantDestructiveConfirmationIsShortLivedAndRevisionBound(t *testing.T) {
	raw, view, err := assistantDestructiveConfirmation(
		assistantState{Sustain: true},
		"Confirm delete",
		"myxl:bookmark_del_exec:key",
		"myxl:saved",
		"Delete",
	)
	if err != nil {
		t.Fatal(err)
	}
	state := decodeAssistantState(raw)
	if state.Sustain {
		t.Fatal("destructive confirmation retained long-lived sustain marker")
	}
	if len(state.Slots) != 2 || state.Slots[0] != "myxl:bookmark_del_exec:key" || state.Slots[1] != "myxl:saved" {
		t.Fatalf("confirmation slots=%v", state.Slots)
	}
	if len(view.Rows) != 1 || len(view.Rows[0]) != 2 ||
		view.Rows[0][0].ActionID != assistantSlotID(0) ||
		view.Rows[0][1].ActionID != assistantSlotID(1) {
		t.Fatalf("confirmation view rows=%+v", view.Rows)
	}
}

func TestP1E4AssistantDirectPurchaseCheckoutCompilesA2State(t *testing.T) {
	p := &Plugin{}
	p.menuMgr = NewMenuManager(p)
	intent := purchaseIntentState{
		MSISDN:      "6281912345678",
		OptionCode:  "OPT-A",
		Method:      "balance",
		QuotedPrice: 25000,
	}
	quote := purchaseCheckoutPreview{
		Intent:         intent,
		PackageName:    "Paket A",
		CanonicalPrice: 25000,
		EffectivePrice: 25000,
	}
	screen, err := p.menuMgr.BuildCheckoutScreen(quote)
	if err != nil {
		t.Fatal(err)
	}
	raw, view, err := p.assistantScreen(assistantState{Draft: &intent}, screen)
	if err != nil {
		t.Fatal(err)
	}
	state := decodeAssistantState(raw)
	if state.Draft == nil || state.Draft.OptionCode != "OPT-A" || state.Draft.QuotedPrice != 25000 {
		t.Fatalf("checkout state draft=%+v", state.Draft)
	}
	if len(state.Slots) != 2 || state.Slots[0] != "myxl:checkout" || state.Slots[1] != "myxl:cancel_draft" {
		t.Fatalf("checkout slots=%v", state.Slots)
	}
	if len(view.Rows) != 1 || len(view.Rows[0]) != 2 {
		t.Fatalf("checkout rows=%+v", view.Rows)
	}
	if view.Rows[0][0].ActionID != assistantSlotID(0) || view.Rows[0][1].ActionID != assistantSlotID(1) {
		t.Fatalf("checkout action IDs=%q,%q", view.Rows[0][0].ActionID, view.Rows[0][1].ActionID)
	}
	if strings.Contains(view.Text, "v1:myxl") || strings.Contains(view.Text, "myxl:checkout") {
		t.Fatalf("checkout presentation leaked transport/internal callback data: %q", view.Text)
	}
}

func TestP1EAssistantPurchaseSlotGetsLongExecutionProfile(t *testing.T) {
	raw, err := encodeAssistantState(assistantState{Slots: []string{"myxl:checkout"}})
	if err != nil {
		t.Fatal(err)
	}
	profile := assistantSlotExecutionProfile(raw, 0)
	if profile.ExecutionTimeout != assistantPurchaseConfirmExec {
		t.Fatalf("purchase execution timeout = %v, want %v", profile.ExecutionTimeout, assistantPurchaseConfirmExec)
	}

	raw, err = encodeAssistantState(assistantState{Slots: []string{"myxl:home"}})
	if err != nil {
		t.Fatal(err)
	}
	if profile := assistantSlotExecutionProfile(raw, 0); profile.ExecutionTimeout != 0 {
		t.Fatalf("ordinary action timeout override = %v, want zero/default", profile.ExecutionTimeout)
	}
}
