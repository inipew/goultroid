package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
)

const defaultEndpoint = "https://api.ocr.space/parse/image"

const (
	maxResponseSize = 8 << 20
	maxAttempts     = 3
)

var supportedLanguages = map[string]struct{}{
	"eng": {}, "ara": {}, "bul": {}, "chs": {}, "cht": {}, "cze": {},
	"dan": {}, "dut": {}, "fin": {}, "fre": {}, "ger": {}, "gre": {},
	"hun": {}, "ind": {}, "ita": {}, "jpn": {}, "kor": {}, "lav": {},
	"lit": {}, "nor": {}, "pol": {}, "por": {}, "rus": {}, "slv": {},
	"spa": {}, "swe": {}, "tur": {}, "ukr": {}, "vie": {},
}

type Plugin struct {
	apiKey, endpoint string
	http             *network.Service
	files            *filesystem.Manager
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
	p.http = svc
}

func (p *Plugin) SetFiles(fs *filesystem.Manager) {
	p.files = fs
}

func (p *Plugin) getHTTP() *network.Service {
	if p.http == nil {
		p.http = network.NewService(nil, nil)
	}
	return p.http
}

func (p *Plugin) getFiles() *filesystem.Manager {
	if p.files == nil {
		p.files, _ = filesystem.NewManager("data", "", "", nil)
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
		Handler:     p.handle,
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

	_ = ctx.EditOrReply("⏳ Processing OCR...")
	files := p.getFiles()
	dir, err := files.CreateTempDir("ocr", "goultroid-ocr-*")
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
	return ctx.EditOrReply("🎉 <b>OCR RESULT</b>\n\n" + core.EscapeHTML(text))
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

func (p *Plugin) extractOnce(ctx context.Context, path, language string) (string, bool, time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, 0, err
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("language", language); err != nil {
		return "", false, 0, err
	}
	if err := mw.WriteField("isOverlayRequired", "false"); err != nil {
		return "", false, 0, err
	}
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", false, 0, err
	}
	if _, err = io.Copy(part, f); err != nil {
		return "", false, 0, err
	}
	if err = mw.Close(); err != nil {
		return "", false, 0, err
	}

	httpSvc := p.getHTTP()
	resp, err := httpSvc.Post(ctx, "ocr", p.endpoint, mw.FormDataContentType(), &body, map[string]string{
		"apikey": p.apiKey,
	})
	if err != nil {
		return "", true, 0, err
	}
	defer resp.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retryable := resp.StatusCode == network.StatusTooManyRequests || resp.StatusCode >= 500
		return "", retryable, retryAfter(resp), fmt.Errorf("OCR service returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out response
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&out); err != nil {
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
