package media

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/media"
	"github.com/inipew/goultroid/internal/services/storage"
)

var (
	_ execution.CapabilityProvider = (*Plugin)(nil)
)

// Plugin provides media inspection, audio extraction, and transcoding utilities.
type Plugin struct {
	mediaService *media.Service
	files        *filesystem.Scope
}

// New creates a new Media plugin with optional dependencies.
func New(deps ...any) *Plugin {
	p := &Plugin{}
	for _, dep := range deps {
		switch v := dep.(type) {
		case *media.Service:
			p.mediaService = v
		}
	}
	return p
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
	p.files = fs.ForOwner("media")
}

func (p *Plugin) getFiles() *filesystem.Scope {
	if p.files == nil {
		manager, _ := filesystem.NewManager("data", "", "", nil)
		p.files = manager.ForOwner("media")
	}
	return p.files
}

// SetMediaService sets the media service platform.
func (p *Plugin) SetMediaService(svc *media.Service) {
	p.mediaService = svc
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "media"
}

// Description returns a short summary of the plugin.
func (p *Plugin) Description() string {
	return "Inspect media metadata, extract audio tracks, and transcode video/stickers"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	if p.mediaService == nil {
		fs, err := storage.NewFileStorage(filepath.Join("data", "media"), 5*1024*1024*1024)
		if err == nil {
			p.mediaService = media.NewService(nil, fs, nil)
		}
	}
	return nil
}

// Shutdown cleans up resources.
func (p *Plugin) Shutdown() error {
	return nil
}

// Capabilities declares the capabilities provided by this plugin (§4, §28 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "media",
			Name:        "Media",
			Description: "Media inspection, extraction, and transcoding tools",
			Category:    "Media",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

// Commands returns the list of registered commands.
func (p *Plugin) Commands() []core.Command {
	mediaSurfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{
			Name:        "mediainfo",
			Aliases:     []string{"media", "minfo"},
			Description: "Inspect metadata, dimensions, duration, and file size of media attachments",
			Usage:       ".mediainfo (or reply to media)",
			Category:    "Media",
			Permission:  core.PermissionEveryone,
			Surfaces:    mediaSurfaces,
			Handler:     p.handleMediaInfo,
		},
		{
			Name:        "extractaudio",
			Aliases:     []string{"extaudio"},
			Description: "Extract audio track from video or media into MP3",
			Usage:       ".extractaudio (reply to video/audio/document)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			Timeout:     5 * time.Minute,
			Surfaces:    mediaSurfaces,
			Handler:     p.handleExtractAudio,
		},
		{
			Name:        "convert",
			Aliases:     []string{"transcode"},
			Description: "Convert video or audio into another format (e.g. mp4, mp3, aac, webm)",
			Usage:       ".convert <format> (reply to media)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			Timeout:     5 * time.Minute,
			Surfaces:    mediaSurfaces,
			Handler:     p.handleConvert,
		},
		{
			Name:        "gif",
			Aliases:     []string{"togif"},
			Description: "Convert replied video or animation into an animated GIF",
			Usage:       ".gif (reply to video)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			Timeout:     5 * time.Minute,
			Surfaces:    mediaSurfaces,
			Handler:     p.handleConvertToGIF,
		},
		{
			Name:        "vstick",
			Aliases:     []string{"videosticker"},
			Description: "Convert replied video or media into a Telegram video sticker (WebM VP9 512x512)",
			Usage:       ".vstick (reply to video)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			Timeout:     5 * time.Minute,
			Surfaces:    mediaSurfaces,
			Handler:     p.handleConvertToSticker,
		},
	}
}

// handleMediaInfo displays technical metadata of an attached or replied media item.
func (p *Plugin) handleMediaInfo(ctx *core.Context) error {
	item := findMedia(ctx)
	if item == nil {
		return ctx.EditOrReply("⚠️ <b>No media found!</b> Please reply to a photo, video, audio, voice, sticker, or document.")
	}

	var sb strings.Builder
	sb.WriteString("📊 <b>Media Information</b>\n\n")

	// Type
	mediaTypeDisplay := item.Type
	if len(mediaTypeDisplay) > 0 {
		mediaTypeDisplay = strings.ToUpper(mediaTypeDisplay[:1]) + mediaTypeDisplay[1:]
	} else {
		mediaTypeDisplay = "Unknown"
	}
	sb.WriteString(fmt.Sprintf("• <b>Type</b>: <code>%s</code>\n", core.EscapeHTML(mediaTypeDisplay)))

	// File Name
	if item.FileName != "" {
		sb.WriteString(fmt.Sprintf("• <b>File Name</b>: <code>%s</code>\n", core.EscapeHTML(item.FileName)))
	}

	// MIME Type
	if item.MimeType != "" {
		sb.WriteString(fmt.Sprintf("• <b>MIME Type</b>: <code>%s</code>\n", core.EscapeHTML(item.MimeType)))
	}

	// Size
	if item.Size > 0 {
		sb.WriteString(fmt.Sprintf("• <b>File Size</b>: <code>%s</code> (%d bytes)\n", formatBytes(item.Size), item.Size))
	}

	// Resolution
	if item.Width > 0 && item.Height > 0 {
		sb.WriteString(fmt.Sprintf("• <b>Resolution</b>: <code>%dx%d</code>\n", item.Width, item.Height))
	}

	// Duration
	if item.Duration > 0 {
		sb.WriteString(fmt.Sprintf("• <b>Duration</b>: <code>%s</code>\n", formatDuration(item.Duration)))
	}

	return ctx.EditOrReply(sb.String())
}

// handleExtractAudio extracts audio from a replied video or audio file using the media service.
func (p *Plugin) handleExtractAudio(ctx *core.Context) error {
	item := findMedia(ctx)
	if item == nil {
		return ctx.EditOrReply("⚠️ <b>No media found!</b> Reply to a video, audio, or document to extract audio.")
	}

	if item.Type == "photo" || item.Type == "sticker" {
		return ctx.EditOrReply("⚠️ Cannot extract audio from a photo or sticker.")
	}

	if item.Size > 0 {
		if err := core.ValidateMediaSize(item.Size, core.DefaultMaxExtractAudioSize); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("⚠️ <b>Media too large!</b> File size (%s) exceeds extraction limit (150MB).", formatBytes(item.Size)))
		}
	}

	if p.mediaService == nil {
		_ = p.Init()
	}

	_ = ctx.EditOrReply("⏳ <i>Downloading and extracting audio...</i>")

	files := p.getFiles()
	tmpDir, err := files.CreateTempDir("goultroid-audio-*")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer files.RemoveTempDir(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download media: %v", err))
	}

	stat, _ := os.Stat(downloadedPath)
	fileSize := item.Size
	if stat != nil && stat.Size() > 0 {
		fileSize = stat.Size()
	}

	inAsset := &storage.Asset{
		ID:        filepath.Base(downloadedPath),
		Name:      item.FileName,
		Path:      downloadedPath,
		Size:      fileSize,
		Duration:  time.Duration(item.Duration) * time.Second,
		Width:     item.Width,
		Height:    item.Height,
		CreatedAt: time.Now(),
	}

	outAsset, err := p.mediaService.ExtractAudio(ctx.Ctx, inAsset, "mp3")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Audio extraction failed: %v", err))
	}

	cleanFileName := core.SanitizeFileName(item.FileName)
	caption := fmt.Sprintf("🎵 Extracted from: <code>%s</code>", core.EscapeHTML(cleanFileName))
	if err := ctx.SendAudio(outAsset.Path, caption); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send audio: %v", err))
	}

	_ = ctx.Delete()
	return nil
}

// handleConvert transcodes replied media into another format.
func (p *Plugin) handleConvert(ctx *core.Context) error {
	item := findMedia(ctx)
	if item == nil {
		return ctx.EditOrReply("⚠️ <b>No media found!</b> Reply to a video or audio file to convert.")
	}

	targetFormat := "mp4"
	if len(ctx.Args) > 0 {
		targetFormat = strings.ToLower(strings.TrimPrefix(ctx.Args[0], "."))
	}

	if p.mediaService == nil {
		_ = p.Init()
	}

	_ = ctx.EditOrReply(fmt.Sprintf("⏳ <i>Converting media to %s...</i>", core.EscapeHTML(targetFormat)))

	files := p.getFiles()
	tmpDir, err := files.CreateTempDir("goultroid-convert-*")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer files.RemoveTempDir(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download media: %v", err))
	}

	stat, _ := os.Stat(downloadedPath)
	fileSize := item.Size
	if stat != nil && stat.Size() > 0 {
		fileSize = stat.Size()
	}

	inAsset := &storage.Asset{
		ID:        filepath.Base(downloadedPath),
		Name:      item.FileName,
		Path:      downloadedPath,
		Size:      fileSize,
		Duration:  time.Duration(item.Duration) * time.Second,
		Width:     item.Width,
		Height:    item.Height,
		CreatedAt: time.Now(),
	}

	outAsset, err := p.mediaService.ConvertVideo(ctx.Ctx, inAsset, media.TranscodeOptions{
		TargetFormat: targetFormat,
	})
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Conversion failed: %v", err))
	}

	caption := fmt.Sprintf("🎬 Converted to: <code>%s</code>", core.EscapeHTML(targetFormat))
	mediaType := "document"
	if targetFormat == "mp4" {
		mediaType = "video"
	} else if targetFormat == "mp3" || targetFormat == "m4a" || targetFormat == "aac" {
		mediaType = "audio"
	}

	if _, err := ctx.SendMedia(outAsset.Path, mediaType, caption); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send converted media: %v", err))
	}

	_ = ctx.Delete()
	return nil
}

// handleConvertToGIF converts a video to an animated GIF.
func (p *Plugin) handleConvertToGIF(ctx *core.Context) error {
	item := findMedia(ctx)
	if item == nil {
		return ctx.EditOrReply("⚠️ <b>No media found!</b> Reply to a video to convert to GIF.")
	}

	if p.mediaService == nil {
		_ = p.Init()
	}

	_ = ctx.EditOrReply("⏳ <i>Converting video to GIF...</i>")

	files := p.getFiles()
	tmpDir, err := files.CreateTempDir("goultroid-gif-*")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer files.RemoveTempDir(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download media: %v", err))
	}

	inAsset := &storage.Asset{
		ID:        filepath.Base(downloadedPath),
		Name:      item.FileName,
		Path:      downloadedPath,
		Size:      item.Size,
		Duration:  time.Duration(item.Duration) * time.Second,
		Width:     item.Width,
		Height:    item.Height,
		CreatedAt: time.Now(),
	}

	outAsset, err := p.mediaService.ConvertToGIF(ctx.Ctx, inAsset, media.TranscodeOptions{})
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ GIF conversion failed: %v", err))
	}

	if _, err := ctx.SendMedia(outAsset.Path, "document", "🎞️ Converted to GIF"); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send GIF: %v", err))
	}

	_ = ctx.Delete()
	return nil
}

// handleConvertToSticker converts replied video or photo into a Telegram video sticker.
func (p *Plugin) handleConvertToSticker(ctx *core.Context) error {
	item := findMedia(ctx)
	if item == nil {
		return ctx.EditOrReply("⚠️ <b>No media found!</b> Reply to a video or animation to make a video sticker.")
	}

	if p.mediaService == nil {
		_ = p.Init()
	}

	_ = ctx.EditOrReply("⏳ <i>Generating video sticker (WebM 512x512)...</i>")

	files := p.getFiles()
	tmpDir, err := files.CreateTempDir("goultroid-vstick-*")
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer files.RemoveTempDir(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download media: %v", err))
	}

	inAsset := &storage.Asset{
		ID:        filepath.Base(downloadedPath),
		Name:      item.FileName,
		Path:      downloadedPath,
		Size:      item.Size,
		Duration:  time.Duration(item.Duration) * time.Second,
		Width:     item.Width,
		Height:    item.Height,
		CreatedAt: time.Now(),
	}

	outAsset, err := p.mediaService.ConvertToSticker(ctx.Ctx, inAsset)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Video sticker generation failed: %v", err))
	}

	if _, err := ctx.SendMedia(outAsset.Path, "sticker", "🎭 Video Sticker"); err != nil {
		// Fallback to document if sticker sender fails
		_, _ = ctx.SendMedia(outAsset.Path, "document", "🎭 Video Sticker (WebM)")
	}

	_ = ctx.Delete()
	return nil
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

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
