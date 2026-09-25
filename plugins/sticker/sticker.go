package sticker

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	stddraw "image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/imageguard"
	"github.com/inipew/goultroid/internal/tasks"
)

const staticStickerMaxBytes int64 = 512 << 10

var errStaticStickerTooLarge = errors.New("static sticker exceeds Telegram size limit")

var stickerImagePolicy = imageguard.Policy{
	MaxInputBytes:   32 << 20,
	MaxWidth:        8192,
	MaxHeight:       8192,
	MaxPixels:       32_000_000,
	MaxDecodedBytes: 128 << 20,
}

// Plugin provides sticker creation and conversion utilities.
type Plugin struct {
	files *filesystem.Scope
}

// New creates a new Sticker plugin.
func New() *Plugin {
	return &Plugin{}
}

// InitPlugin initializes the plugin using capability-gated PluginContext.
func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	fsMgr, err := pctx.Files()
	if err != nil {
		return err
	}
	p.files = fsMgr
	return nil
}

// SetFiles sets the filesystem manager for the plugin.
func (p *Plugin) SetFiles(fs *filesystem.Manager) {
	p.files = fs.ForOwner("sticker")
}

func (p *Plugin) getFiles() *filesystem.Scope {
	if p.files == nil {
		manager, _ := filesystem.NewManager("data", "", "", nil)
		p.files = manager.ForOwner("sticker")
	}
	return p.files
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "sticker"
}

// Description returns a short summary of the plugin.
func (p *Plugin) Description() string {
	return "Convert images and photos into Telegram stickers"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Shutdown cleans up resources.
func (p *Plugin) Shutdown() error {
	return nil
}

// Commands returns the list of registered commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "sticker",
			Aliases:     []string{"stk"},
			Description: "Convert a replied or sent photo/image into a Telegram sticker",
			Usage:       ".sticker (reply to photo or image)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			Timeout:     60 * time.Second,
			Resources: []tasks.ResourceRequirement{
				{Name: "download", Amount: 1},
				{Name: "media", Amount: 1},
			},
			Handler: p.handleSticker,
		},
	}
}

// handleSticker converts an image into Telegram sticker dimensions (512x512 max, preserving aspect ratio).
func (p *Plugin) handleSticker(ctx *core.Context) error {
	media := findMedia(ctx)
	if media == nil {
		return ctx.EditOrReply("⚠️ <b>No image found!</b> Reply to a photo, image file, or sticker.")
	}

	// Only process visual media
	if media.Type != "photo" && media.Type != "sticker" && media.Type != "document" {
		return ctx.EditOrReply("⚠️ Please reply to a photo, sticker, or image document.")
	}
	if media.Type == "sticker" {
		switch stickerSourceFormat(media) {
		case "animated":
			return ctx.EditOrReply("⚠️ Animated <code>.tgs</code> stickers are already Telegram stickers and cannot be raster-converted. Reply to a static image/WebP/PNG instead.")
		case "video":
			return ctx.EditOrReply("⚠️ Video <code>.webm</code> stickers are already Telegram stickers and cannot be raster-converted. Reply to a static image/WebP/PNG instead.")
		case "unsupported":
			return ctx.EditOrReply("⚠️ The selected sticker format is not supported for static conversion.")
		}
	}
	if media.Type == "document" &&
		media.MimeType != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(media.MimeType)), "image/") {
		return ctx.EditOrReply("⚠️ The selected document is not a supported image.")
	}
	if err := imageguard.ValidateKnown(media.Size, media.Width, media.Height, stickerImagePolicy); err != nil {
		return ctx.Error(fmt.Sprintf("Image rejected by safety limits: %v", err))
	}

	_ = ctx.Progress("<i>Processing sticker...</i>")

	files := p.getFiles()
	tmpDir, err := files.CreateTempDir("goultroid-sticker-*")
	if err != nil {
		return ctx.Error(fmt.Sprintf("Failed to create temp directory: %v", err))
	}
	defer files.RemoveTempDir(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.Error(fmt.Sprintf("Failed to download media: %v", err))
	}

	// Decode source image
	srcImg, err := decodeImageFile(downloadedPath)
	if err != nil {
		return ctx.Error(fmt.Sprintf("Failed to decode image: %v", err))
	}

	// Calculate target dimensions (Telegram spec: 512px on one side, <= 512px on the other)
	dstW, dstH := calculateStickerDimensions(srcImg.Bounds().Dx(), srcImg.Bounds().Dy())

	// Resize using high-quality bilinear interpolation
	dstImg := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.BiLinear.Scale(dstImg, dstImg.Bounds(), srcImg, srcImg.Bounds(), draw.Over, nil)

	// Prefer the full-color lossless PNG. If an image with high entropy exceeds
	// Telegram's 512 KiB static-sticker limit, retry with a bounded 256-color
	// dithered palette rather than failing a perfectly usable source image.
	outPath := filepath.Join(tmpDir, "sticker.png")
	quantized, err := encodeStaticStickerOutput(outPath, dstImg)
	if err != nil {
		return ctx.Error(fmt.Sprintf("Sticker output is not Telegram-compliant: %v", err))
	}
	if quantized {
		_ = ctx.Progress("<i>Sticker optimized to fit Telegram's 512 KiB limit...</i>")
	}

	// Upload sticker
	if err := ctx.SendSticker(outPath); err != nil {
		return ctx.Error(fmt.Sprintf("Failed to send sticker: %v", err))
	}
	if ctx.LastResponseID > 0 {
		_ = ctx.Messages().DeleteResponse()
	}
	return nil
}

func stickerSourceFormat(media *core.MediaInfo) string {
	if media == nil || media.Type != "sticker" {
		return ""
	}
	mime := strings.ToLower(strings.TrimSpace(media.MimeType))
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(media.FileName)))
	switch {
	case mime == "application/x-tgsticker" || mime == "application/x-tgs" || ext == ".tgs":
		return "animated"
	case mime == "video/webm" || ext == ".webm":
		return "video"
	case mime == "image/webp" || mime == "image/png" || ext == ".webp" || ext == ".png" || (mime == "" && ext == ""):
		return "static"
	default:
		return "unsupported"
	}
}

func encodeStaticStickerOutput(path string, img image.Image) (bool, error) {
	if err := writeStickerPNG(path, img); err != nil {
		return false, err
	}
	if err := validateStaticStickerOutput(path); err == nil {
		return false, nil
	} else if !errors.Is(err, errStaticStickerTooLarge) {
		return false, err
	}

	base := palette.Plan9
	if len(base) > 255 {
		base = base[:255]
	}
	pal := make(color.Palette, 0, 256)
	pal = append(pal, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
	pal = append(pal, base...)
	indexed := image.NewPaletted(img.Bounds(), pal)
	stddraw.FloydSteinberg.Draw(indexed, indexed.Bounds(), img, img.Bounds().Min)

	if err := writeStickerPNG(path, indexed); err != nil {
		return true, err
	}
	if err := validateStaticStickerOutput(path); err != nil {
		return true, err
	}
	return true, nil
}

func writeStickerPNG(path string, img image.Image) error {
	outFile, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create sticker PNG: %w", err)
	}
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(outFile, img); err != nil {
		_ = outFile.Close()
		return fmt.Errorf("encode sticker PNG: %w", err)
	}
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("finalize sticker PNG: %w", err)
	}
	return nil
}

func validateStaticStickerOutput(path string) error {
	info, err := imageguard.Inspect(path, imageguard.Policy{
		MaxInputBytes:   staticStickerMaxBytes,
		MaxWidth:        512,
		MaxHeight:       512,
		MaxPixels:       512 * 512,
		MaxDecodedBytes: 4 * 512 * 512,
		AllowedFormats:  []string{"png"},
	})
	if err != nil {
		if errors.Is(err, imageguard.ErrInputTooLarge) {
			return fmt.Errorf("%w: %v", errStaticStickerTooLarge, err)
		}
		return err
	}
	if info.Width != 512 && info.Height != 512 {
		return fmt.Errorf("one side must be exactly 512px, got %dx%d", info.Width, info.Height)
	}
	return nil
}

// calculateStickerDimensions scales dimensions so the max side is 512 and aspect ratio is preserved.
func calculateStickerDimensions(srcW, srcH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		return 512, 512
	}

	if srcW >= srcH {
		dstW := 512
		dstH := int(math.Round(float64(srcH) * 512.0 / float64(srcW)))
		if dstH < 1 {
			dstH = 1
		}
		return dstW, dstH
	}

	dstH := 512
	dstW := int(math.Round(float64(srcW) * 512.0 / float64(srcH)))
	if dstW < 1 {
		dstW = 1
	}
	return dstW, dstH
}

// decodeImageFile validates compressed and decoded image budgets before full decode.
func decodeImageFile(path string) (image.Image, error) {
	img, _, err := imageguard.Decode(path, stickerImagePolicy)
	return img, err
}

// findMedia retrieves media from the current message or the replied message.
func findMedia(ctx *core.Context) *core.MediaInfo {
	if ctx == nil {
		return nil
	}
	if ctx.Message != nil && ctx.Message.Media != nil {
		return ctx.Message.Media
	}
	reply, err := ctx.GetReply()
	if err == nil && reply != nil && reply.Media != nil {
		return reply.Media
	}
	return nil
}
