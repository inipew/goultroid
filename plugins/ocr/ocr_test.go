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
	"github.com/inipew/goultroid/internal/tasks"
)

type immediateOCRTicket struct {
	result tasks.TaskResult
	state  tasks.TaskState
	done   chan struct{}
}

func (t *immediateOCRTicket) TaskID() tasks.TaskID             { return t.result.TaskID }
func (t *immediateOCRTicket) State() tasks.TaskState           { return t.state }
func (t *immediateOCRTicket) Done() <-chan struct{}            { return t.done }
func (t *immediateOCRTicket) Result() (tasks.TaskResult, bool) { return t.result, true }
func (t *immediateOCRTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	select {
	case <-t.done:
		return t.result, nil
	case <-ctx.Done():
		return tasks.TaskResult{}, ctx.Err()
	}
}

type immediateOCRTaskClient struct {
	specs []tasks.WorkSpec
}

func (c *immediateOCRTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	recorded := spec
	recorded.Handler = nil
	recorded.Commit = nil
	recorded.OnComplete = nil
	recorded.Resources = append([]tasks.ResourceRequirement(nil), spec.Resources...)
	c.specs = append(c.specs, recorded)

	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	state := tasks.StateCompleted
	taskCtx := tasks.WithHeldResources(ctx, spec.Resources)
	if spec.Handler != nil {
		if err := spec.Handler(taskCtx); err != nil {
			result.Outcome = tasks.OutcomeFailed
			result.Failure.Message = err.Error()
			state = tasks.StateFailed
		}
	}
	done := make(chan struct{})
	close(done)
	return &immediateOCRTicket{result: result, state: state, done: done}, nil
}

func (c *immediateOCRTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}

func (c *immediateOCRTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }

func (c *immediateOCRTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

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
	gate.Register("ocr", []string{plugin.CapHTTP, plugin.CapFilesystemTemp, plugin.CapSecretRead, plugin.CapTasks})
	secMgr := secret.NewManager(map[string]string{"OCR_API": "test-key-123"})
	taskClient := &immediateOCRTaskClient{}
	scope := plugin.NewScope(context.Background(), "ocr")
	defer func() { _ = scope.Close(context.Background()) }()
	pctxGranted := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Scope:      scope,
		Owner:      "ocr",
		Gate:       gate,
		Network:    netSvc,
		Files:      fsMgr,
		Secrets:    secMgr,
		TaskClient: taskClient,
	})
	if err := p.InitPlugin(pctxGranted); err != nil {
		t.Fatalf("unexpected error when capabilities granted: %v", err)
	}
	if p.http == nil || p.files == nil || p.tasks == nil {
		t.Fatal("expected http, files, and task client to be configured")
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

func TestOCRCommandUsesStagedResources(t *testing.T) {
	cmds := New().Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands=%d, want 1", len(cmds))
	}
	cmd := cmds[0]
	if len(cmd.Resources) != 0 {
		t.Fatalf("OCR command must not hold static resources: %+v", cmd.Resources)
	}
	if cmd.Timeout != ocrCommandTimeout {
		t.Fatalf("command timeout=%v, want %v", cmd.Timeout, ocrCommandTimeout)
	}
}

func TestIsOCRMediaRejectsAnimatedStickerAndUnknownImageMIME(t *testing.T) {
	location := &tg.InputDocumentFileLocation{}
	for _, media := range []*core.MediaInfo{
		{Type: "sticker", MimeType: "application/x-tgsticker", Location: location},
		{Type: "sticker", MimeType: "video/webm", Location: location},
		{Type: "document", MimeType: "image/svg+xml", Location: location},
		{Type: "document", MimeType: "application/octet-stream", Location: location},
	} {
		if isOCRMedia(media) {
			t.Fatalf("unsupported media accepted: %+v", media)
		}
	}
	for _, media := range []*core.MediaInfo{
		{Type: "sticker", MimeType: "image/webp", Location: location},
		{Type: "document", MimeType: "image/png", Location: location},
		{Type: "document", MimeType: "image/jpeg", Location: location},
	} {
		if !isOCRMedia(media) {
			t.Fatalf("supported media rejected: %+v", media)
		}
	}
}

func TestNormalizeOCRTextRemovesUnsafeControlsAndNormalizesLines(t *testing.T) {
	got := normalizeOCRText("  one\r\ntwo\rthree\x00\x01\t四  ")
	want := "one\ntwo\nthree\t四"
	if got != want {
		t.Fatalf("normalizeOCRText=%q, want %q", got, want)
	}
}

func TestSafeOCRErrorEscapesAndBounds(t *testing.T) {
	err := errors.New(strings.Repeat("<bad>&", 200))
	got := safeOCRError(err)
	if strings.Contains(got, "<bad>") || !strings.Contains(got, "&lt;bad&gt;") || !strings.Contains(got, "&amp;") {
		t.Fatalf("unsafe OCR error rendering: %q", got)
	}
	if utf8.RuneCountInString(got) > maxOCRErrorRunes*5 {
		t.Fatalf("escaped OCR error unexpectedly large: %d runes", utf8.RuneCountInString(got))
	}
}

func TestRenderOCRChunksStopsAfterAttachmentDecision(t *testing.T) {
	text := strings.Repeat("x", 2_000_000)
	chunks, attach := renderOCRChunks(text)
	if !attach {
		t.Fatal("very long OCR output should use attachment")
	}
	if len(chunks) != maxOCRInlineChunks+1 {
		t.Fatalf("planned chunks=%d, want %d", len(chunks), maxOCRInlineChunks+1)
	}
	var retained int
	for _, chunk := range chunks {
		retained += len(chunk)
	}
	if retained > (maxTelegramMessageRunes-ocrChunkFormattingReserve)*(maxOCRInlineChunks+1)+1024 {
		t.Fatalf("chunk planner retained too much text: %d bytes", retained)
	}
}

type ocrHandlerService struct {
	ocrDeliveryService
	reply        *tg.Message
	payload      []byte
	downloadedID int64
}

func (s *ocrHandlerService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return s.reply, nil
}

func (s *ocrHandlerService) DownloadFile(
	_ context.Context,
	location tg.InputFileLocationClass,
	dstPath string,
) error {
	if doc, ok := location.(*tg.InputDocumentFileLocation); ok {
		s.downloadedID = doc.ID
	}
	return os.WriteFile(dstPath, s.payload, 0o600)
}

func TestOCRHandlerStagesResourcesAndUsesRepliedMedia(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "source.png")
	writeTestPNG(t, imagePath, 64, 64)
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	svc := &ocrHandlerService{
		payload: imageBytes,
		reply: &tg.Message{
			ID: 77,
			Media: &tg.MessageMediaDocument{Document: &tg.Document{
				ID: 222, MimeType: "image/png", Size: int64(len(imageBytes)),
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: "reply.png"},
					&tg.DocumentAttributeImageSize{W: 64, H: 64},
				},
			}},
		},
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"IsErroredOnProcessing":false,"ParsedResults":[{"ParsedText":"hello OCR"}]}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}

	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.ForOwner("ocr").TempDir()
	if err != nil {
		t.Fatal(err)
	}
	taskClient := &immediateOCRTaskClient{}
	p := New()
	p.SetAPIKey("test-key")
	p.SetHTTP(network.NewService(client, nil))
	p.SetFiles(manager)
	p.SetTaskClient(taskClient)

	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		PeerID: &tg.InputPeerChat{ChatID: 1},
		Message: &core.Message{
			ID: 10, ReplyToID: 77, IsOutgoing: true,
			Media: &core.MediaInfo{
				Type: "document", MimeType: "image/png", FileName: "command.png",
				Location: &tg.InputDocumentFileLocation{ID: 111},
			},
		},
	}
	if err := p.handle(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.downloadedID != 222 {
		t.Fatalf("downloaded document ID=%d, want replied media 222", svc.downloadedID)
	}
	if len(taskClient.specs) != 2 {
		t.Fatalf("task stages=%d, want download+extract", len(taskClient.specs))
	}
	download := taskClient.specs[0]
	if download.Pool != tasks.PoolID("download") || download.ExecutionTimeout != ocrDownloadTimeout {
		t.Fatalf("download stage=%+v", download)
	}
	if len(download.Resources) != 1 || download.Resources[0].Name != "download" || download.Resources[0].Amount != 1 {
		t.Fatalf("download resources=%+v", download.Resources)
	}
	extract := taskClient.specs[1]
	if extract.Pool != tasks.PoolID("general") || extract.ExecutionTimeout != ocrExtractTimeout || len(extract.Resources) != 0 {
		t.Fatalf("extract stage=%+v", extract)
	}
	if len(svc.edits) == 0 || !strings.Contains(svc.edits[len(svc.edits)-1], "hello OCR") {
		t.Fatalf("OCR result was not delivered: edits=%q", svc.edits)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("OCR workspace leaked %d entries after success", len(entries))
	}
}

func TestOCRHandlerCleansWorkspaceAndEscapesServiceFailure(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "source.png")
	writeTestPNG(t, imagePath, 32, 32)
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	svc := &ocrHandlerService{
		payload: imageBytes,
		reply: &tg.Message{
			ID: 88,
			Media: &tg.MessageMediaDocument{Document: &tg.Document{
				ID: 333, MimeType: "image/png", Size: int64(len(imageBytes)),
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: "failure.png"},
					&tg.DocumentAttributeImageSize{W: 32, H: 32},
				},
			}},
		},
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader("<bad>& rejected")),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}

	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.ForOwner("ocr").TempDir()
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	p.SetAPIKey("test-key")
	p.SetHTTP(network.NewService(client, nil))
	p.SetFiles(manager)
	p.SetTaskClient(&immediateOCRTaskClient{})

	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChat{ChatID: 1},
		Message: &core.Message{ID: 11, ReplyToID: 88, IsOutgoing: true},
	}
	if err := p.handle(ctx); err == nil {
		t.Fatal("expected OCR service rejection to remain an execution error")
	}
	if len(svc.edits) == 0 {
		t.Fatal("expected user-facing OCR failure")
	}
	last := svc.edits[len(svc.edits)-1]
	if strings.Contains(last, "<bad>") || !strings.Contains(last, "&lt;bad&gt;") || !strings.Contains(last, "&amp;") {
		t.Fatalf("unsafe service error UI: %q", last)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("OCR workspace leaked %d entries after failure", len(entries))
	}
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
	p := New()
	taskClient := &immediateOCRTaskClient{}
	p.SetTaskClient(taskClient)
	if err := p.deliverResult(ctx, t.TempDir(), text); err != nil {
		t.Fatalf("deliverResult failed: %v", err)
	}
	if len(svc.edits) != 1 {
		t.Fatalf("expected one preview edit, got %d", len(svc.edits))
	}
	if svc.mediaType != "file" {
		t.Fatalf("mediaType=%q, want file", svc.mediaType)
	}
	if svc.mediaData != normalizeOCRText(text) {
		t.Fatal("attached OCR result did not preserve normalized full text")
	}
	if got := utf8.RuneCountInString(svc.edits[0]); got > maxTelegramMessageRunes {
		t.Fatalf("preview has %d runes, exceeds Telegram limit", got)
	}
	if !strings.Contains(svc.edits[0], "full OCR result is attached") {
		t.Fatalf("preview did not explain attachment fallback: %q", svc.edits[0])
	}
	if len(taskClient.specs) != 1 {
		t.Fatalf("delivery task count=%d, want 1", len(taskClient.specs))
	}
	if got := taskClient.specs[0].Resources; len(got) != 1 || got[0].Name != "media" || got[0].Amount != 1 {
		t.Fatalf("delivery resources=%+v, want media:1", got)
	}
}
