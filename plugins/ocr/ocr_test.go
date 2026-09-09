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

	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
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

	netSvc := network.NewService(client, nil)
	p := &Plugin{
		apiKey:   "test-key",
		endpoint: "https://ocr.example.test/parse/image",
		http:     netSvc,
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

func TestOCRPlugin_InitPluginCapabilities(t *testing.T) {
	gate := plugin.NewCapabilityGate()
	netSvc := network.NewService(nil, nil)
	fsMgr, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	gate.Register("ocr", []string{})
	pctxDenied := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "ocr",
		Gate:    gate,
		Network: netSvc,
		Files:   fsMgr,
	})
	p := New()
	if err := p.InitPlugin(pctxDenied); err == nil {
		t.Fatal("expected error when CapHTTP is not registered")
	}

	gate.Register("ocr", []string{plugin.CapHTTP, plugin.CapFilesystemTemp})
	pctxGranted := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "ocr",
		Gate:    gate,
		Network: netSvc,
		Files:   fsMgr,
	})
	if err := p.InitPlugin(pctxGranted); err != nil {
		t.Fatalf("unexpected error when capabilities granted: %v", err)
	}
	if p.http == nil || p.files == nil {
		t.Fatal("expected http and files to be configured")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
