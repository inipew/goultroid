package ocr

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	client := &http.Client{Timeout: 2 * time.Second, Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("temporary failure")), Header: make(http.Header), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"IsErroredOnProcessing":false,"ParsedResults":[{"ParsedText":"hello"}]}`)), Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
	})}

	dir := t.TempDir()
	path := filepath.Join(dir, "image.jpg")
	if err := os.WriteFile(path, []byte("test image"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{
		apiKey:   "test-key",
		endpoint: "https://ocr.example.test/parse/image",
		client:   client,
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
