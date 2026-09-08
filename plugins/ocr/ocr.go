package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

const defaultEndpoint = "https://api.ocr.space/parse/image"

type Plugin struct { apiKey, endpoint string; client *http.Client }
func New() *Plugin { return &Plugin{apiKey: strings.TrimSpace(os.Getenv("OCR_API")), endpoint: defaultEndpoint, client: &http.Client{Timeout: 90 * time.Second}} }
func (p *Plugin) Name() string { return "ocr" }
func (p *Plugin) Description() string { return "Extract text from a replied Telegram photo using OCR.Space" }
func (p *Plugin) Init() error { if p.client == nil { p.client = &http.Client{Timeout: 90 * time.Second} }; if p.endpoint == "" { p.endpoint = defaultEndpoint }; return nil }
func (p *Plugin) Shutdown() error { return nil }
func (p *Plugin) Capabilities() []execution.Capability { return []execution.Capability{{ID: "ocr", Name: "OCR", Description: "Optical character recognition for images", Category: "Media", Surfaces: execution.SurfaceUserbot}} }
func (p *Plugin) Commands() []core.Command { return []core.Command{{Name: "ocr", Description: "Recognize text from a replied photo", Usage: ".ocr [language] (reply to photo)", Category: "Media", Permission: core.PermissionSudo, Surfaces: execution.SurfaceUserbot, Timeout: 2 * time.Minute, Handler: p.handle}} }
func (p *Plugin) handle(ctx *core.Context) error {
	if p.apiKey == "" { return ctx.EditOrReply("❌ OCR is not configured. Set OCR_API to an OCR.Space API key.") }
	if ctx.Message == nil || ctx.Message.ReplyToID == 0 { return ctx.EditOrReply("⚠️ Reply to a photo with .ocr [language].") }
	reply, err := ctx.GetReply(); if err != nil || reply == nil || reply.Media == nil || reply.Media.Type != "photo" { return ctx.EditOrReply("⚠️ The replied message must contain a photo.") }
	_ = ctx.EditOrReply("⏳ Processing OCR...")
	dir, err := os.MkdirTemp("", "goultroid-ocr-*"); if err != nil { return fmt.Errorf("create OCR temp directory: %w", err) }; defer os.RemoveAll(dir)
	path, err := ctx.DownloadMedia(dir); if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download photo: %v", err)) }
	lang := "eng"; if len(ctx.Args) > 0 && strings.TrimSpace(ctx.Args[0]) != "" { lang = strings.TrimSpace(ctx.Args[0]) }
	text, err := p.extract(ctx.Ctx, path, lang); if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ OCR failed: %v", err)) }
	if strings.TrimSpace(text) == "" { return ctx.EditOrReply("ℹ️ OCR completed, but no text was detected.") }
	return ctx.EditOrReply("🎉 <b>OCR RESULT</b>\n\n" + core.EscapeHTML(strings.TrimSpace(text)))
}

type response struct { IsErroredOnProcessing bool `json:"IsErroredOnProcessing"`; ErrorMessage any `json:"ErrorMessage"`; ParsedResults []struct { ParsedText string `json:"ParsedText"` } `json:"ParsedResults"` }
func (p *Plugin) extract(ctx context.Context, path, language string) (string, error) {
	f, err := os.Open(path); if err != nil { return "", err }; defer f.Close()
	var body bytes.Buffer; mw := multipart.NewWriter(&body); _ = mw.WriteField("language", language); _ = mw.WriteField("isOverlayRequired", "false")
	part, err := mw.CreateFormFile("file", filepath.Base(path)); if err != nil { return "", err }; if _, err = io.Copy(part, f); err != nil { return "", err }; if err = mw.Close(); err != nil { return "", err }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, &body); if err != nil { return "", err }; req.Header.Set("apikey", p.apiKey); req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := p.client.Do(req); if err != nil { return "", err }; defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)); return "", fmt.Errorf("OCR service returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b))) }
	var out response; if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil { return "", err }; if out.IsErroredOnProcessing { return "", fmt.Errorf("OCR service rejected the image: %v", out.ErrorMessage) }
	var sb strings.Builder; for _, r := range out.ParsedResults { if strings.TrimSpace(r.ParsedText) != "" { if sb.Len() > 0 { sb.WriteString("\n") }; sb.WriteString(r.ParsedText) } }; return sb.String(), nil
}
