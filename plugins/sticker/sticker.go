package sticker

import (
	"fmt"
	"image"
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
			Resources:   []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
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
	if (media.Type == "document" || media.Type == "sticker") &&
		media.MimeType != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(media.MimeType)), "image/") {
		return ctx.EditOrReply("⚠️ The selected document or sticker is not a supported image.")
	}
	if err := imageguard.ValidateKnown(media.Size, media.Width, media.Height, stickerImagePolicy); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Image rejected by safety limits: %v", err))
	}

	_ = ctx.EditOrReply("⏳ <i>Processing sticker...</i>")

	files := p.getFiles()
	tmpDir, err := files.CreateTempDir("goultroid-sticker-*")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer files.RemoveTempDir(tmpDir)

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
