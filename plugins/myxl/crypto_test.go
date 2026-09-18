package myxl

import (
	"testing"
	"time"
)

func TestDeriveIV(t *testing.T) {
	iv, err := DeriveIV(1682337722000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(iv) != 16 {
		t.Fatalf("expected 16 bytes IV, got %d", len(iv))
	}
}

func TestEncryptDecryptXData_Roundtrip(t *testing.T) {
	key := "1234567890123456"
	timeMs := int64(1234567890123)
	plaintext := `{"test":"data","user_id":12345}`

	encrypted, err := EncryptXData(plaintext, timeMs, key)
	if err != nil {
		t.Fatalf("EncryptXData failed: %v", err)
	}
	if len(encrypted) == 0 {
		t.Fatal("encrypted string is empty")
	}

	decrypted, err := DecryptXData(encrypted, timeMs, key)
	if err != nil {
		t.Fatalf("DecryptXData failed: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("roundtrip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestMakeAxAPISignature(t *testing.T) {
	sig := MakeAxAPISignature("1234567890", "62812345678", "1234", "SMS", "test_key")
	if len(sig) == 0 {
		t.Fatal("expected non-empty signature")
	}
}

func TestMakeXSignature(t *testing.T) {
	sig := MakeXSignature("token123", "POST", "api/v8/packages/balance-and-credit", 1700000000, "secret_key")
	if len(sig) != 128 { // SHA-512 hex is 128 characters
		t.Fatalf("expected 128 hex chars for SHA-512 signature, got %d", len(sig))
	}
}

func TestFormatMyXLHeaderTS(t *testing.T) {
	tm := time.Date(2026, 9, 16, 9, 30, 0, 450000000, time.UTC)
	ts := FormatMyXLHeaderTS(tm)
	if len(ts) == 0 {
		t.Fatal("formatted timestamp is empty")
	}
	// Check WIB timezone suffix
	if !testing.Short() && len(ts) < 22 {
		t.Fatalf("timestamp too short: %s", ts)
	}
}

func TestMakeXSignaturePaymentParams(t *testing.T) {
	params := PaymentSignatureParams{
		AccessToken:    "mock_access_token",
		SigTimeSec:     1700000000,
		PackageCode:    "PKG_123",
		TokenPayment:   "TOK_PAY_456",
		PaymentMethod:  "PULSA",
		PaymentFor:     "BUY_PACKAGE",
		Path:           "payments/api/v8/settlement",
		XAPIBaseSecret: "mock_secret_base",
	}
	sig := MakeXSignaturePaymentParams(params, "ae-hei_9Tee6he+Ik3Gais5=")
	if len(sig) != 128 {
		t.Fatalf("expected 128 hex chars for SHA-512 signature, got %d", len(sig))
	}
}

func TestBuildEncryptedFieldWithKey(t *testing.T) {
	efURLSafe := BuildEncryptedFieldWithKey("5dccbf08920a5527", true)
	if len(efURLSafe) < 16 {
		t.Fatalf("expected urlsafe encrypted field to be >= 16 chars, got %d", len(efURLSafe))
	}

	efStd := BuildEncryptedFieldWithKey("5dccbf08920a5527", false)
	if len(efStd) < 16 {
		t.Fatalf("expected std encrypted field to be >= 16 chars, got %d", len(efStd))
	}
}
