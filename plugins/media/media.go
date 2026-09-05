package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides media inspection and conversion utilities.
type Plugin struct{}

// New creates a new Media plugin.
func New() *Plugin {
	return &Plugin{}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "media"
}

// Description returns a short summary of the plugin.
func (p *Plugin) Description() string {
	return "Inspect media metadata and extract audio tracks"
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
			Name:        "mediainfo",
			Aliases:     []string{"media", "minfo"},
			Description: "Inspect metadata, dimensions, duration, and file size of media attachments",
			Usage:       ".mediainfo (or reply to media)",
			Category:    "Media",
			Permission:  core.PermissionEveryone,
			Handler:     p.handleMediaInfo,
		},
		{
			Name:        "extractaudio",
			Aliases:     []string{"extaudio"},
			Description: "Extract audio track from video or media into MP3",
			Usage:       ".extractaudio (reply to video/audio/document)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			Timeout:     3 * time.Minute,
			Handler:     p.handleExtractAudio,
		},
	}
}

// handleMediaInfo displays technical metadata of an attached or replied media item.
func (p *Plugin) handleMediaInfo(ctx *core.Context) error {
	media := findMedia(ctx)
	if media == nil {
		return ctx.Reply("⚠️ <b>No media found!</b> Please reply to a photo, video, audio, voice, sticker, or document.")
	}

	var sb strings.Builder
	sb.WriteString("📊 <b>Media Information</b>\n\n")

	// Type
	mediaTypeDisplay := media.Type
	if len(mediaTypeDisplay) > 0 {
		mediaTypeDisplay = strings.ToUpper(mediaTypeDisplay[:1]) + mediaTypeDisplay[1:]
	} else {
		mediaTypeDisplay = "Unknown"
	}
	sb.WriteString(fmt.Sprintf("• <b>Type</b>: <code>%s</code>\n", core.EscapeHTML(mediaTypeDisplay)))

	// File Name
	if media.FileName != "" {
		sb.WriteString(fmt.Sprintf("• <b>File Name</b>: <code>%s</code>\n", core.EscapeHTML(media.FileName)))
	}

	// MIME Type
	if media.MimeType != "" {
		sb.WriteString(fmt.Sprintf("• <b>MIME Type</b>: <code>%s</code>\n", core.EscapeHTML(media.MimeType)))
	}

	// Size
	if media.Size > 0 {
		sb.WriteString(fmt.Sprintf("• <b>File Size</b>: <code>%s</code> (%d bytes)\n", formatBytes(media.Size), media.Size))
	}

	// Resolution
	if media.Width > 0 && media.Height > 0 {
		sb.WriteString(fmt.Sprintf("• <b>Resolution</b>: <code>%dx%d</code>\n", media.Width, media.Height))
	}

	// Duration
	if media.Duration > 0 {
		sb.WriteString(fmt.Sprintf("• <b>Duration</b>: <code>%s</code>\n", formatDuration(media.Duration)))
	}

	return ctx.Reply(sb.String())
}

// handleExtractAudio extracts audio from a replied video or audio file using ffmpeg.
func (p *Plugin) handleExtractAudio(ctx *core.Context) error {
	media := findMedia(ctx)
	if media == nil {
		return ctx.Reply("⚠️ <b>No media found!</b> Reply to a video, audio, or document to extract audio.")
	}

	if media.Type == "photo" || media.Type == "sticker" {
		return ctx.Reply("⚠️ Cannot extract audio from a photo or sticker.")
	}

	// Verify ffmpeg is available
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ctx.Reply("❌ <b>ffmpeg is not installed on this system.</b> Please install ffmpeg to use <code>.extractaudio</code>.")
	}

	_ = ctx.Reply("⏳ <i>Downloading and extracting audio...</i>")

	tmpDir, err := os.MkdirTemp("", "goultroid-audio-*")
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to create temp directory: %v", err))
	}
	defer os.RemoveAll(tmpDir)

	downloadedPath, err := ctx.DownloadMedia(tmpDir)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to download media: %v", err))
	}

	outPath := filepath.Join(tmpDir, "extracted_audio.mp3")
	cmd := exec.CommandContext(ctx.Ctx, ffmpegPath, "-y", "-i", downloadedPath, "-vn", "-acodec", "libmp3lame", "-q:a", "2", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to extract audio: %v\nOutput: %s", err, string(out)))
	}

	caption := fmt.Sprintf("🎵 Extracted from: <code>%s</code>", media.FileName)
	if err := ctx.SendAudio(outPath, caption); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to send audio: %v", err))
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
