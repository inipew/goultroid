package config

import (
	"os"
	"reflect"
	"testing"
)

func clearEnv() {
	vars := []string{
		"APP_ID",
		"APP_HASH",
		"PHONE",
		"SESSION_FILE",
		"DATABASE_PATH",
		"PREFIX",
		"OWNER_ID",
		"SUDO_USERS",
		"LOG_LEVEL",
	}
	for _, v := range vars {
		os.Unsetenv(v)
	}
}

func TestLoad_ValidMinimal(t *testing.T) {
	clearEnv()
	os.Setenv("APP_ID", "123456")
	os.Setenv("APP_HASH", "test_hash")
	os.Setenv("PHONE", "+628123456789")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AppID != 123456 {
		t.Errorf("expected AppID 123456, got %d", cfg.AppID)
	}
	if cfg.AppHash != "test_hash" {
		t.Errorf("expected AppHash test_hash, got %s", cfg.AppHash)
	}
	if cfg.Phone != "+628123456789" {
		t.Errorf("expected Phone +628123456789, got %s", cfg.Phone)
	}
	if cfg.SessionFile != "data/session.json" {
		t.Errorf("expected default SessionFile data/session.json, got %s", cfg.SessionFile)
	}
	if cfg.DatabasePath != "data/goultroid.db" {
		t.Errorf("expected default DatabasePath data/goultroid.db, got %s", cfg.DatabasePath)
	}
	if cfg.Prefix != "." {
		t.Errorf("expected default Prefix ., got %s", cfg.Prefix)
	}
	if cfg.OwnerID != 0 {
		t.Errorf("expected OwnerID 0, got %d", cfg.OwnerID)
	}
	if len(cfg.SudoUsers) != 0 {
		t.Errorf("expected empty SudoUsers, got %v", cfg.SudoUsers)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected default LogLevel info, got %s", cfg.LogLevel)
	}
}

func TestLoad_ValidFull(t *testing.T) {
	clearEnv()
	os.Setenv("APP_ID", "999")
	os.Setenv("APP_HASH", "hash999")
	os.Setenv("PHONE", "+1234567890")
	os.Setenv("SESSION_FILE", "custom/session.json")
	os.Setenv("DATABASE_PATH", "custom/goultroid.db")
	os.Setenv("PREFIX", "!")
	os.Setenv("OWNER_ID", "11223344")
	os.Setenv("SUDO_USERS", " 101, 102 , , 103 ")
	os.Setenv("LOG_LEVEL", "DEBUG")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AppID != 999 {
		t.Errorf("expected AppID 999, got %d", cfg.AppID)
	}
	if cfg.SessionFile != "custom/session.json" {
		t.Errorf("expected custom SessionFile, got %s", cfg.SessionFile)
	}
	if cfg.DatabasePath != "custom/goultroid.db" {
		t.Errorf("expected custom DatabasePath, got %s", cfg.DatabasePath)
	}
	if cfg.Prefix != "!" {
		t.Errorf("expected custom Prefix !, got %s", cfg.Prefix)
	}
	if cfg.OwnerID != 11223344 {
		t.Errorf("expected OwnerID 11223344, got %d", cfg.OwnerID)
	}
	expectedSudo := []int64{101, 102, 103}
	if !reflect.DeepEqual(cfg.SudoUsers, expectedSudo) {
		t.Errorf("expected SudoUsers %v, got %v", expectedSudo, cfg.SudoUsers)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected LogLevel debug, got %s", cfg.LogLevel)
	}
}

func TestLoad_MissingAppID(t *testing.T) {
	clearEnv()
	os.Setenv("APP_HASH", "hash")
	os.Setenv("PHONE", "+123")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing APP_ID, got nil")
	}
}

func TestLoad_InvalidAppID(t *testing.T) {
	clearEnv()
	os.Setenv("APP_HASH", "hash")
	os.Setenv("PHONE", "+123")

	invalidIDs := []string{"not-a-number", "-1", "0"}
	for _, id := range invalidIDs {
		os.Setenv("APP_ID", id)
		_, err := Load()
		if err == nil {
			t.Fatalf("expected error for APP_ID=%q, got nil", id)
		}
	}
}

func TestLoad_MissingAppHash(t *testing.T) {
	clearEnv()
	os.Setenv("APP_ID", "123")
	os.Setenv("PHONE", "+123")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing APP_HASH, got nil")
	}
}

func TestLoad_MissingPhone(t *testing.T) {
	clearEnv()
	os.Setenv("APP_ID", "123")
	os.Setenv("APP_HASH", "hash")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing PHONE, got nil")
	}
}

func TestLoad_InvalidOwnerID(t *testing.T) {
	clearEnv()
	os.Setenv("APP_ID", "123")
	os.Setenv("APP_HASH", "hash")
	os.Setenv("PHONE", "+123")
	os.Setenv("OWNER_ID", "invalid_id")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid OWNER_ID, got nil")
	}
}

func TestLoad_InvalidSudoUser(t *testing.T) {
	clearEnv()
	os.Setenv("APP_ID", "123")
	os.Setenv("APP_HASH", "hash")
	os.Setenv("PHONE", "+123")
	os.Setenv("SUDO_USERS", "123, invalid_user")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid SUDO_USERS, got nil")
	}
}
