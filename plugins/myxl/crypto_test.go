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
