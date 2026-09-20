package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/imageguard"
	"github.com/inipew/goultroid/internal/tasks"
)

const defaultEndpoint = "https://api.ocr.space/parse/image"

const (
	maxResponseSize           = 8 << 20
	maxAttempts               = 3
	maxTelegramMessageRunes   = 4096
	maxOCRInlineChunks        = 6
	ocrChunkFormattingReserve = 192
)

var ocrImagePolicy = imageguard.Policy{
	MaxInputBytes:   25 << 20,
	MaxWidth:        8192,
	MaxHeight:       8192,
	MaxPixels:       40_000_000,
	MaxDecodedBytes: 160 << 20,
}

var supportedLanguages = map[string]struct{}{
	"eng": {}, "ara": {}, "bul": {}, "chs": {}, "cht": {}, "cze": {},
	"dan": {}, "dut": {}, "fin": {}, "fre": {}, "ger": {}, "gre": {},
	"hun": {}, "ind": {}, "ita": {}, "jpn": {}, "kor": {}, "lav": {},
	"lit": {}, "nor": {}, "pol": {}, "por": {}, "rus": {}, "slv": {},
	"spa": {}, "swe": {}, "tur": {}, "ukr": {}, "vie": {},
}

type Plugin struct {
	apiKey, endpoint string
	http             *network.Client
	files            *filesystem.Scope
}

func New() *Plugin {
	return &Plugin{
		endpoint: defaultEndpoint,
	}
}

func (p *Plugin) Init() error { return nil }

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	netSvc, err := pctx.HTTP()
	if err != nil {
		return err
	}
	p.http = netSvc

	fsMgr, err := pctx.Files()
	if err != nil {
		return err
	}
	p.files = fsMgr

	secMgr, err := pctx.Secrets()
	if err != nil {
		return err
	}
	if key, err := secMgr.Get("OCR_API"); err == nil && strings.TrimSpace(key) != "" {
		p.apiKey = strings.TrimSpace(key)
	}
	return nil
}

func (p *Plugin) SetAPIKey(key string) {
	p.apiKey = strings.TrimSpace(key)
}

func (p *Plugin) SetHTTP(svc *network.Service) {
	p.http = svc.ForOwner("ocr")
}

func (p *Plugin) SetFiles(fs *filesystem.Manager) {
	p.files = fs.ForOwner("ocr")
}

func (p *Plugin) getHTTP() *network.Client {
	if p.http == nil {
		p.http = network.NewService(nil, nil).ForOwner("ocr")
	}
	return p.http
}

func (p *Plugin) getFiles() *filesystem.Scope {
	if p.files == nil {
		manager, _ := filesystem.NewManager("data", "", "", nil)
		p.files = manager.ForOwner("ocr")
	}
	return p.files
}

func (p *Plugin) Name() string { return "ocr" }

func (p *Plugin) Description() string {
	return "Extract text from a replied image using OCR.Space"
}

func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{{
		ID:          "ocr",
		Name:        "Optical Character Recognition",
		Description: "Extract text from replied image media",
		Category:    "Media",
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
	}}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{{
		Name:        "ocr",
		Description: "Extract text from a replied photo or image document",
		Usage:       ".ocr [language]",
		Category:    "Media",
		Permission:  core.PermissionEveryone,
		Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		Resources: []tasks.ResourceRequirement{
			{Name: "download", Amount: 1},
			{Name: "media", Amount: 1},
		},
		Handler: p.handle,
	}}
}

func (p *Plugin) handle(ctx *core.Context) error {
	if p.apiKey == "" {
		return ctx.EditOrReply("❌ OCR is not configured. Set OCR_API to an OCR.Space API key.")
	}
	if ctx.Message == nil || ctx.Message.ReplyToID == 0 {
		return ctx.EditOrReply("⚠️ Reply to a photo or image document with .ocr [language].")
	}
	reply, err := ctx.GetReply()
	if err != nil || reply == nil || !isOCRMedia(reply.Media) {
		return ctx.EditOrReply("⚠️ The replied message must contain a photo or an image document.")
	}

	lang := "eng"
	if len(ctx.Args) > 0 && strings.TrimSpace(ctx.Args[0]) != "" {
		lang = strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	}
	if !validLanguage(lang) {
		return ctx.EditOrReply("⚠️ Unsupported OCR language. Use a valid OCR.Space language code such as eng, ind, jpn, kor, rus, or vie.")
	}
	if err := imageguard.ValidateKnown(reply.Media.Size, reply.Media.Width, reply.Media.Height, ocrImagePolicy); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Image rejected by safety limits: %v", err))
	}

	_ = ctx.EditOrReply("⏳ Processing OCR...")
	files := p.getFiles()
	dir, err := files.CreateTempDir("goultroid-ocr-*")
	if err != nil {
		return fmt.Errorf("create OCR temp directory: %w", err)
	}
	defer files.RemoveTempDir(dir)

	path, err := ctx.DownloadMedia(dir)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download image: %v", err))
	}

	text, err := p.extract(ctx.Ctx, path, lang)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ OCR failed: %v", err))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ctx.EditOrReply("ℹ️ OCR completed, but no text was detected.")
	}
	return p.deliverResult(ctx, dir, text)
}

func isOCRMedia(media *core.MediaInfo) bool {
	if media == nil || media.Location == nil {
		return false
	}
	if media.Type == "photo" {
		return true
	}
	if media.Type == "sticker" {
		return strings.HasPrefix(strings.ToLower(media.MimeType), "image/")
	}
	return media.Type == "document" && strings.HasPrefix(strings.ToLower(media.MimeType), "image/")
}

func validLanguage(language string) bool {
	_, ok := supportedLanguages[strings.ToLower(strings.TrimSpace(language))]
	return ok
}

type response struct {
	IsErroredOnProcessing bool `json:"IsErroredOnProcessing"`
	ErrorMessage          any  `json:"ErrorMessage"`
	ParsedResults         []struct {
		ParsedText string `json:"ParsedText"`
	} `json:"ParsedResults"`
}

func (p *Plugin) extract(ctx context.Context, path, language string) (string, error) {
	if !validLanguage(language) {
		return "", fmt.Errorf("unsupported OCR language %q", language)
	}
	if _, err := imageguard.Inspect(path, ocrImagePolicy); err != nil {
		return "", fmt.Errorf("image safety validation failed: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		text, retryable, retryAfter, err := p.extractOnce(ctx, path, language)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable || attempt == maxAttempts {
			break
		}
		delay := retryAfter
		if delay <= 0 {
			delay = time.Duration(1<<(attempt-1)) * time.Second
		}
		if delay > 4*time.Second {
			delay = 4 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", lastErr
}

type ocrMultipartBody struct {
	ctx    context.Context
	reader io.Reader
	file   *os.File
}

func (b *ocrMultipartBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.reader.Read(p)
}

func (b *ocrMultipartBody) Close() error {
	if b.file == nil {
		return nil
	}
	return b.file.Close()
}

// newOCRMultipartBody keeps only multipart framing in memory. Image bytes are
// pulled directly from the file by the HTTP transport for every retry attempt.
func newOCRMultipartBody(ctx context.Context, path, language string) (*ocrMultipartBody, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}

	var framing bytes.Buffer
	mw := multipart.NewWriter(&framing)
	fail := func(err error) (*ocrMultipartBody, string, error) {
		_ = f.Close()
		return nil, "", err
	}
	if err := mw.WriteField("language", language); err != nil {
		return fail(err)
	}
	if err := mw.WriteField("isOverlayRequired", "false"); err != nil {
		return fail(err)
	}
	if _, err := mw.CreateFormFile("file", filepath.Base(path)); err != nil {
		return fail(err)
	}
	contentType := mw.FormDataContentType()
	boundary := mw.Boundary()
	if err := mw.Close(); err != nil {
		return fail(err)
	}

	closingBoundary := []byte("\r\n--" + boundary + "--\r\n")
	framed := framing.Bytes()
	closingAt := bytes.LastIndex(framed, closingBoundary)
	if closingAt < 0 {
		return fail(errors.New("multipart closing boundary was not generated"))
	}

	reader := io.MultiReader(
		bytes.NewReader(framed[:closingAt]),
		f,
		bytes.NewReader(framed[closingAt:]),
	)
	return &ocrMultipartBody{ctx: ctx, reader: reader, file: f}, contentType, nil
}

func (p *Plugin) extractOnce(ctx context.Context, path, language string) (string, bool, time.Duration, error) {
	body, contentType, err := newOCRMultipartBody(ctx, path, language)
	if err != nil {
		return "", false, 0, err
	}
	defer func() { _ = body.Close() }()

	httpSvc := p.getHTTP()
	resp, err := httpSvc.Post(ctx, p.endpoint, contentType, body, map[string]string{
		"apikey": p.apiKey,
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return "", false, 0, ctx.Err()
		}
		return "", true, 0, err
	}
	defer resp.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retryable := resp.StatusCode == network.StatusTooManyRequests || resp.StatusCode >= 500
		return "", retryable, retryAfter(resp), fmt.Errorf("OCR service returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	out, err := decodeOCRResponse(resp.Body)
	if err != nil {
		return "", false, 0, err
	}
	if out.IsErroredOnProcessing {
		return "", false, 0, fmt.Errorf("OCR service rejected the image: %v", out.ErrorMessage)
	}

	var sb strings.Builder
	for _, r := range out.ParsedResults {
		if text := strings.TrimSpace(r.ParsedText); text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(text)
		}
	}
	return sb.String(), false, 0, nil
}

func decodeOCRResponse(r io.Reader) (response, error) {
	var out response
	limited := &io.LimitedReader{R: r, N: maxResponseSize + 1}
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return out, err
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return out, err
	}
	if limited.N == 0 {
		return out, fmt.Errorf("OCR service response exceeds %d bytes", maxResponseSize)
	}
	return out, nil
}

func escapedOCRRuneCost(r rune) int {
	switch r {
	case '&':
		return 5
	case '<', '>':
		return 4
	default:
		return 1
	}
}

func splitOCRText(text string, maxEscapedRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" || maxEscapedRunes <= 0 {
		return nil
	}

	chunks := make([]string, 0, 2)
	for len(text) > 0 {
		cost := 0
		cutAt := 0
		lastNewline := 0
		lastNewlineCost := 0

		for i, r := range text {
			runeCost := escapedOCRRuneCost(r)
			if cost+runeCost > maxEscapedRunes {
				if lastNewline > 0 && lastNewlineCost >= maxEscapedRunes/3 {
					cutAt = lastNewline
				} else {
					cutAt = i
				}
				break
			}
			cost += runeCost
			next := i + utf8.RuneLen(r)
			cutAt = next
			if r == '\n' {
				lastNewline = next
				lastNewlineCost = cost
			}
		}

		if cutAt <= 0 {
			_, size := utf8.DecodeRuneInString(text)
			if size <= 0 {
				break
			}
			cutAt = size
		}
		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}
	return chunks
}

func renderOCRChunks(text string) []string {
	payloadBudget := maxTelegramMessageRunes - ocrChunkFormattingReserve
	raw := splitOCRText(text, payloadBudget)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for i, chunk := range raw {
		header := "🎉 <b>OCR RESULT</b>\n\n"
		if len(raw) > 1 {
			header = fmt.Sprintf("🎉 <b>OCR RESULT</b> <code>%d/%d</code>\n\n", i+1, len(raw))
		}
		out = append(out, header+core.EscapeHTML(chunk))
	}
	return out
}

func renderOCRPreview(text string) string {
	payloadBudget := maxTelegramMessageRunes - ocrChunkFormattingReserve
	raw := splitOCRText(text, payloadBudget)
	if len(raw) == 0 {
		return "🎉 <b>OCR RESULT</b>"
	}
	return "🎉 <b>OCR RESULT</b>\n\n" + core.EscapeHTML(raw[0]) +
		"\n\n<i>Output is long; the full OCR result is attached as a text file.</i>"
}

func (p *Plugin) deliverResult(ctx *core.Context, tempDir, text string) error {
	chunks := renderOCRChunks(text)
	if len(chunks) == 0 {
		return ctx.EditOrReply("ℹ️ OCR completed, but no text was detected.")
	}

	if len(chunks) <= maxOCRInlineChunks {
		if err := ctx.EditOrReply(chunks[0]); err != nil {
			return err
		}
		for i, chunk := range chunks[1:] {
			if err := ctx.Reply(chunk); err != nil {
				return fmt.Errorf("send OCR result chunk %d/%d: %w", i+2, len(chunks), err)
			}
		}
		return nil
	}

	if err := ctx.EditOrReply(renderOCRPreview(text)); err != nil {
		return err
	}

	resultPath := filepath.Join(tempDir, "ocr-result.txt")
	f, err := os.OpenFile(resultPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create OCR result attachment: %w", err)
	}
	if _, err := io.WriteString(f, text); err != nil {
		_ = f.Close()
		return fmt.Errorf("write OCR result attachment: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close OCR result attachment: %w", err)
	}
	if err := ctx.SendFile(resultPath, "🧾 Full OCR result"); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to attach full OCR result: %v", err))
	}
	return nil
}

func retryAfter(resp *network.Response) time.Duration {
	if resp == nil {
		return 0
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := time.Parse(time.RFC1123, value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}
