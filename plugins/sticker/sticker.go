package sticker

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"image/png"
)

// Plugin provides sticker creation and conversion utilities.
type Plugin struct{}

// New creates a new Sticker plugin.
func New() *Plugin {
	return &Plugin{}
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
			Handler:     p.handleSticker,
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

	_ = ctx.EditOrReply("⏳ <i>Processing sticker...</i>")

	tmpDir, err := os.MkdirTemp("", "goultroid-sticker-*")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer os.RemoveAll(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download media: %v", err))
	}

	// Decode source image
	srcImg, err := decodeImageFile(downloadedPath)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to decode image: %v", err))
	}

	// Calculate target dimensions (Telegram spec: 512px on one side, <= 512px on the other)
	dstW, dstH := calculateStickerDimensions(srcImg.Bounds().Dx(), srcImg.Bounds().Dy())

	// Resize using high-quality bilinear interpolation
	dstImg := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.BiLinear.Scale(dstImg, dstImg.Bounds(), srcImg, srcImg.Bounds(), draw.Over, nil)

	// Save as PNG
	outPath := filepath.Join(tmpDir, "sticker.png")
	outFile, err := os.Create(outPath)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create output file: %v", err))
	}
	defer outFile.Close()

	if err := png.Encode(outFile, dstImg); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to encode sticker PNG: %v", err))
	}
	_ = outFile.Close()

	// Upload sticker
	if err := ctx.SendSticker(outPath); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send sticker: %v", err))
	}

	_ = ctx.Delete()
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

// decodeImageFile decodes JPEG, PNG, GIF, or WEBP image formats.
func decodeImageFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Attempt standard library decoding (JPEG, PNG, GIF)
	img, _, err := image.Decode(f)
	if err == nil {
		return img, nil
	}

	// Rewind and attempt WebP decoding
	if _, seekErr := f.Seek(0, io.SeekStart); seekErr == nil {
		if webpImg, webpErr := webp.Decode(f); webpErr == nil {
			return webpImg, nil
		}
	}

	return nil, fmt.Errorf("unsupported or corrupted image format: %w", err)
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
