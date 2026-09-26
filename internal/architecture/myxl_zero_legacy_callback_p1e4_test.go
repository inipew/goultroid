package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1E4MyXLHasZeroLegacyCallbackProductionSurface(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "plugins", "myxl")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	forbidden := []string{
		"/internal/services/callback",
		"callback.",
		"SetStateStore",
		"ScopedCallbackStore",
		"RequiresCallbackState",
		"CallbackOptions",
		"HandleCallback(",
		"EncodeCallbackData(",
		"ParseCallbackData(",
		"stateStore",
		"purchaseDraftState",
		"v1:myxl",
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, token := range forbidden {
			if strings.Contains(source, token) {
				t.Fatalf("%s retained legacy MyXL callback surface %q", name, token)
			}
		}
	}
}

func TestP1E4MyXLModuleDoesNotAllocateLegacyCallbackStore(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "module.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, "ScopedCallbackStore") || strings.Contains(source, "SetStateStore") {
		t.Fatal("MyXL module still allocates legacy callback state")
	}
}

func TestP1E4DirectAssistantPurchaseUsesAssistantA2Checkout(t *testing.T) {
	root := repositoryRoot(t)
	assistantRaw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "assistant_interaction.go"))
	if err != nil {
		t.Fatal(err)
	}
	assistant := string(assistantRaw)
	for _, required := range []string{
		`assistantScreenCheckout = "checkout"`,
		"openAssistantPurchaseConfirmation(",
		"ID:          assistantScreenCheckout",
		"TTL:       assistantConfirmationTTL",
		"BuildCheckoutScreen(quote)",
	} {
		if !strings.Contains(assistant, required) {
			t.Fatalf("Assistant direct purchase a2 path missing %q", required)
		}
	}

	myxlRaw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "myxl.go"))
	if err != nil {
		t.Fatal(err)
	}
	myxl := string(myxlRaw)
	start := strings.Index(myxl, "func (p *Plugin) handleBuy(")
	if start < 0 {
		t.Fatal("handleBuy missing")
	}
	end := strings.Index(myxl[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("handleBuy terminator missing")
	}
	body := myxl[start : start+end]
	assistantOpen := strings.Index(body, "openAssistantPurchaseConfirmation(")
	nativeOpen := strings.Index(body, "openNativePurchaseConfirmation(")
	if assistantOpen < 0 || nativeOpen < 0 {
		t.Fatal("handleBuy must route both Assistant and native purchases to a2")
	}
	for _, forbidden := range []string{"StoreWithScope(", "EncodeCallbackData(", "stateStore"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("handleBuy retained legacy transport %q", forbidden)
		}
	}
}

func TestP1EAssistantPurchaseUsesPreparedExecutionAndStalesBeforeReserve(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "assistant_interaction.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "rt.Engine.RegisterPreparedAction(") ||
		!strings.Contains(source, "assistantPurchaseConfirmExec") {
		t.Fatal("Assistant MyXL actions must expose a prepared TaskEngine profile for purchase confirmation")
	}

	start := strings.Index(source, "func (p *Plugin) confirmAssistantPurchase(")
	if start < 0 {
		t.Fatal("confirmAssistantPurchase missing")
	}
	end := strings.Index(source[start:], "\nvar _ assistantinteraction.FeatureDriver")
	if end < 0 {
		t.Fatal("confirmAssistantPurchase terminator missing")
	}
	body := source[start : start+end]
	transition := strings.Index(body, "ctx.Transition(")
	reserve := strings.Index(body, "ReservePurchase(")
	if transition < 0 || reserve < 0 || transition > reserve {
		t.Fatal("Assistant purchase must advance session revision before ReservePurchase")
	}
}
