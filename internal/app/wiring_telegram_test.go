package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDCFromSession(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if dc := readDCFromSession(""); dc != 0 {
			t.Fatalf("expected 0, got %d", dc)
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		if dc := readDCFromSession("non_existent_file.json"); dc != 0 {
			t.Fatalf("expected 0, got %d", dc)
		}
	})

	t.Run("valid session file", func(t *testing.T) {
		dir := t.TempDir()
		sessionFile := filepath.Join(dir, "session.json")
		content := `{"Version":1,"Data":{"DC":5,"Addr":"149.154.167.51:443"}}`
		if err := os.WriteFile(sessionFile, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if dc := readDCFromSession(sessionFile); dc != 5 {
			t.Fatalf("expected DC 5, got %d", dc)
		}
	})

	t.Run("env override", func(t *testing.T) {
		t.Setenv("TELEGRAM_DC", "4")
		if dc := readDCFromSession(""); dc != 4 {
			t.Fatalf("expected DC 4 from env, got %d", dc)
		}
	})
}
