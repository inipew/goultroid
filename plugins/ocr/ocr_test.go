package ocr

import (
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/secret"
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
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("temporary failure")), Header: make(http.Header), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"IsErroredOnProcessing":false,"ParsedResults":[{"ParsedText":"hello"}]}`)), Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
	})}

	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	writeTestPNG(t, path, 8, 8)

	netSvc := network.NewService(client, nil)
	p := &Plugin{
		apiKey:   "test-key",
		endpoint: "https://ocr.example.test/parse/image",
		http:     netSvc.ForOwner("ocr"),
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

	gate.AllowPrivileged("ocr", plugin.CapSecretRead)
	gate.Register("ocr", []string{plugin.CapHTTP, plugin.CapFilesystemTemp, plugin.CapSecretRead})
	secMgr := secret.NewManager(map[string]string{"OCR_API": "test-key-123"})
	pctxGranted := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "ocr",
		Gate:    gate,
		Network: netSvc,
		Files:   fsMgr,
		Secrets: secMgr,
	})
	if err := p.InitPlugin(pctxGranted); err != nil {
		t.Fatalf("unexpected error when capabilities granted: %v", err)
	}
	if p.http == nil || p.files == nil {
		t.Fatal("expected http and files to be configured")
	}
	if p.apiKey != "test-key-123" {
		t.Fatalf("expected apiKey 'test-key-123', got %q", p.apiKey)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExtractRejectsUnsafeImageBeforeHTTP(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"IsErroredOnProcessing":false}`)), Header: make(http.Header), Request: r}, nil
	})}

	path := filepath.Join(t.TempDir(), "too-wide.png")
	writeTestPNG(t, path, 9000, 1)

	p := &Plugin{
		apiKey:   "test-key",
		endpoint: "https://ocr.example.test/parse/image",
		http:     network.NewService(client, nil).ForOwner("ocr"),
	}
	if _, err := p.extract(context.Background(), path, "eng"); err == nil {
		t.Fatal("expected unsafe image to be rejected")
	}
	if attempts != 0 {
		t.Fatalf("unsafe image reached HTTP client %d times", attempts)
	}
}

func writeTestPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOCRMultipartBodyStreamsValidForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	writeTestPNG(t, path, 16, 8)
	wantFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	body, contentType, err := newOCRMultipartBody(context.Background(), path, "ind")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("unexpected content type %q", contentType)
	}

	reader := multipart.NewReader(body, params["boundary"])
	fields := map[string]string{}
	var gotFile []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() == "file" {
			gotFile = data
		} else {
			fields[part.FormName()] = string(data)
		}
	}
	if fields["language"] != "ind" || fields["isOverlayRequired"] != "false" {
		t.Fatalf("unexpected multipart fields: %+v", fields)
	}
	if string(gotFile) != string(wantFile) {
		t.Fatal("multipart file payload did not match source image")
	}
}

func TestOCRMultipartBodyHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	writeTestPNG(t, path, 8, 8)
	ctx, cancel := context.WithCancel(context.Background())
	body, _, err := newOCRMultipartBody(ctx, path, "eng")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	cancel()

	if _, err := body.Read(make([]byte, 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v, want context.Canceled", err)
	}
}

type ocrDeliveryService struct {
	core.MockTelegramServicer
	edits     []string
	sends     []string
	mediaType string
	mediaData string
}

func (m *ocrDeliveryService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	m.edits = append(m.edits, text)
	return nil
}

func (m *ocrDeliveryService) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sends = append(m.sends, text)
	return &tg.Message{ID: 100 + len(m.sends), Message: text}, nil
}

func (m *ocrDeliveryService) SendMedia(_ context.Context, _ tg.InputPeerClass, mediaType, filePath, _ string) (*tg.Message, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	m.mediaType = mediaType
	m.mediaData = string(data)
	return &tg.Message{ID: 500}, nil
}

func TestDeliverResultChunksUnicodeAndEscapedHTML(t *testing.T) {
	svc := &ocrDeliveryService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 1},
		Message: &core.Message{
			ID:         10,
			IsOutgoing: true,
		},
		Svc: svc,
	}
	text := strings.Repeat("日本語 <tag> & OCR line\n", 500)
	if err := New().deliverResult(ctx, t.TempDir(), text); err != nil {
		t.Fatalf("deliverResult failed: %v", err)
	}
	if len(svc.edits) != 1 || len(svc.sends) == 0 {
		t.Fatalf("expected one edit plus continuation replies, edits=%d sends=%d", len(svc.edits), len(svc.sends))
	}
	all := append(append([]string{}, svc.edits...), svc.sends...)
	for i, chunk := range all {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8", i)
		}
		if got := utf8.RuneCountInString(chunk); got > maxTelegramMessageRunes {
			t.Fatalf("chunk %d has %d runes, exceeds %d", i, got, maxTelegramMessageRunes)
		}
	}
	joined := strings.Join(all, "")
	if !strings.Contains(joined, "&lt;tag&gt;") || !strings.Contains(joined, "&amp;") {
		t.Fatal("OCR output was not HTML-escaped safely")
	}
}

func TestDeliverResultUsesAttachmentForVeryLongOutput(t *testing.T) {
	svc := &ocrDeliveryService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 1},
		Message: &core.Message{
			ID:         10,
			IsOutgoing: true,
		},
		Svc: svc,
	}
	text := strings.Repeat("&long OCR text<>\n", 10000)
	if err := New().deliverResult(ctx, t.TempDir(), text); err != nil {
		t.Fatalf("deliverResult failed: %v", err)
	}
	if len(svc.edits) != 1 {
		t.Fatalf("expected one preview edit, got %d", len(svc.edits))
	}
	if svc.mediaType != "file" {
		t.Fatalf("mediaType=%q, want file", svc.mediaType)
	}
	if svc.mediaData != text {
		t.Fatal("attached OCR result did not preserve full raw text")
	}
	if got := utf8.RuneCountInString(svc.edits[0]); got > maxTelegramMessageRunes {
		t.Fatalf("preview has %d runes, exceeds Telegram limit", got)
	}
	if !strings.Contains(svc.edits[0], "full OCR result is attached") {
		t.Fatalf("preview did not explain attachment fallback: %q", svc.edits[0])
	}
}
