package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1E2PurchaseIntentRetainsNoSettlementAuthority(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "myxl", "purchase_state.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "type purchaseIntentState struct")
	if start < 0 {
		t.Fatal("purchaseIntentState missing")
	}
	end := strings.Index(source[start:], "\n}")
	if end < 0 {
		t.Fatal("purchaseIntentState terminator missing")
	}
	intent := source[start : start+end]
	for _, forbidden := range []string{"TokenConfirmation", "PackageName", "AccessToken", "RefreshToken", "IDToken"} {
		if strings.Contains(intent, forbidden) {
			t.Fatalf("purchase intent retains forbidden authority field %q", forbidden)
		}
	}
	for _, required := range []string{
		"QuotedPrice",
		"preparePurchaseIntent(",
		"resolvePurchaseIntent(",
		"GetByMSISDN(",
		"GetPackageDetails(",
		"ErrPurchaseQuoteChanged",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("purchase state model missing %q", required)
		}
	}
}

func TestP1E2BothPurchaseConfirmPathsResolveFreshBeforeReserve(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("plugins", "myxl", "native_interaction.go"),
		filepath.Join("plugins", "myxl", "assistant_interaction.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		resolve := strings.Index(source, "resolvePurchaseIntent(")
		reserve := strings.Index(source, "ReservePurchase(")
		if resolve < 0 || reserve < 0 || resolve > reserve {
			t.Fatalf("%s must fresh-resolve purchase intent before ReservePurchase", rel)
		}
	}
}
