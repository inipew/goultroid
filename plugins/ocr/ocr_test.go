package ocr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidLanguage(t *testing.T) {
	for _, lang := range []string{"eng", "IND", "jpn", "vie", " rus "} {
		if !validLanguage(lang) {
			t.Errorf("expected language %q to be valid", lang)
		}
	}
	for _, lang := range []string{"", "xx", "english", "id"} {
		if validLanguage(lang) {
			t.Errorf("expected language %q to be invalid", lang)
		}
	}
}

func TestExtractRetriesTransientHTTPFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"IsErroredOnProcessing":false,"ParsedResults":[{"ParsedText":"hello"}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "image.jpg")
	if err := os.WriteFile(path, []byte("test image"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{
		apiKey:   "test-key",
		endpoint: server.URL,
		client:   &http.Client{Timeout: 2 * time.Second},
	}
	text, err := p.extract(context.Background(), path, "eng")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if text != "hello" {
		t.Fatalf("expected hello, got %q", text)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 HTTP attempts, got %d", attempts)
	}
}
