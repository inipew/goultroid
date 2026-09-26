package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1E3NativeMyXLPurchaseProducerUsesA2(t *testing.T) {
	root := repositoryRoot(t)

	nativeRaw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "native_interaction.go"))
	if err != nil {
		t.Fatal(err)
	}
	native := string(nativeRaw)
	for _, required := range []string{
		"nativePurchaseScreen",
		"nativePurchaseConfirmAction",
		"nativePurchaseCancelAction",
		"openNativePurchaseConfirmation(",
		"handleNativePurchaseConfirm(",
		"handleNativePurchaseCancel(",
	} {
		if !strings.Contains(native, required) {
			t.Fatalf("native MyXL purchase a2 path missing %q", required)
		}
	}
	if strings.Contains(native, "/internal/services/callback") || strings.Contains(native, "EncodeCallbackData(") {
		t.Fatal("native MyXL purchase a2 path must not depend on legacy callback state/protocol")
	}

	start := strings.Index(native, "func (p *Plugin) handleNativePurchaseConfirm(")
	if start < 0 {
		t.Fatal("native purchase confirm handler missing")
	}
	end := strings.Index(native[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("native purchase confirm terminator missing")
	}
	body := native[start : start+end]
	transition := strings.Index(body, "ctx.Transition(")
	reserve := strings.Index(body, "ReservePurchase(")
	if transition < 0 || reserve < 0 || transition > reserve {
		t.Fatal("native purchase confirm must advance revision before ReservePurchase")
	}
	if !strings.Contains(body, "resolvePurchaseIntent(") || !strings.Contains(body, "ctx.Terminate(") {
		t.Fatal("native purchase confirm must fresh-resolve and terminate the session")
	}
}

func TestP1E3NativeHandleBuyCannotFallBackToLegacyState(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "myxl.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (p *Plugin) handleBuy(")
	if start < 0 {
		t.Fatal("handleBuy missing")
	}
	end := strings.Index(source[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("handleBuy terminator missing")
	}
	body := source[start : start+end]
	nativeGate := strings.Index(body, "useNativePurchase := !ctx.IsAssistant()")
	nativeOpen := strings.Index(body, "openNativePurchaseConfirmation(")
	legacyStore := strings.Index(body, "StoreWithScope(")
	if nativeGate < 0 || nativeOpen < 0 || legacyStore < 0 {
		t.Fatal("handleBuy native/legacy migration gates incomplete")
	}
	if !(nativeGate < nativeOpen && nativeOpen < legacyStore) {
		t.Fatal("native purchase a2 path must be selected before Assistant-only legacy callback storage")
	}
	if !strings.Contains(body, "Compatibility only for direct Assistant command surfaces until P1-E4") {
		t.Fatal("legacy purchase producer must be explicitly fenced as Assistant compatibility only")
	}
}

func TestP1E3FeatureSpecDeclaresNativePurchaseInteractions(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugins", "myxl", "assistant_interaction.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"ID:          nativePurchaseScreen",
		"ID:          nativePurchaseConfirmAction",
		"ID:          nativePurchaseCancelAction",
		"Surfaces:    execution.SurfaceUserbot",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("MyXL FeatureSpec missing native purchase declaration %q", required)
		}
	}
}
