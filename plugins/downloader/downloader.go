package downloader

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides media download capabilities.
type Plugin struct{}

// New creates a new downloader Plugin instance.
func New() *Plugin {
	return &Plugin{}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "downloader"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the list of commands provided by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "download",
			Aliases:     []string{"dl"},
			Description: "Download media from replied message",
			Usage:       ".download",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			ReplyOnly:   true,
			Cooldown:    3 * time.Second,
			Timeout:     5 * time.Minute,
			Handler:     p.handleDownload,
		},
	}
}

func (p *Plugin) handleDownload(ctx *core.Context) error {
	saveDir := filepath.Join("data", "downloads")

	var mediaSize int64
	if ctx.Message != nil && ctx.Message.Media != nil {
		mediaSize = ctx.Message.Media.Size
	} else if reply, err := ctx.GetReply(); err == nil && reply != nil && reply.Media != nil {
		mediaSize = reply.Media.Size
	}

	if mediaSize > 0 {
		if err := core.ValidateMediaSize(mediaSize, core.DefaultMaxDownloadSize); err != nil {
			return ctx.Reply(fmt.Sprintf("⚠️ <b>Media too large!</b> File size (%s) exceeds download limit (500MB).", formatBytes(mediaSize)))
		}
	}

	requiredSpace := mediaSize
	if requiredSpace <= 0 {
		requiredSpace = 50 * 1024 * 1024
	}
	if err := core.CheckDiskSpace(saveDir, requiredSpace); err != nil {
		return ctx.Reply("❌ <b>Insufficient disk space</b> on host machine to complete download.")
	}

	if err := ctx.Reply("⏳ Downloading media..."); err != nil {
		return err
	}

	start := time.Now()

	filePath, err := ctx.DownloadMedia(saveDir)
	if err != nil {
		return ctx.Edit(fmt.Sprintf("❌ Download failed: %v", err))
	}

	duration := time.Since(start)

	var sizeBytes int64
	if stat, err := os.Stat(filePath); err == nil {
		sizeBytes = stat.Size()
	}

	fileName := filepath.Base(filePath)
	sizeStr := formatBytes(sizeBytes)

	var speedStr string
	if duration.Seconds() > 0 && sizeBytes > 0 {
		mbps := (float64(sizeBytes) / 1024 / 1024) / duration.Seconds()
		speedStr = fmt.Sprintf("%.2f MB/s", mbps)
	} else {
		speedStr = "fast"
	}

	text := fmt.Sprintf(
		"📥 **Download Complete!**\n\n"+
			"📁 **File:** `%s`\n"+
			"📦 **Size:** `%s`\n"+
			"⏱️ **Time:** `%.2fs` (%s)\n"+
			"📍 **Saved to:** `%s`",
		fileName,
		sizeStr,
		duration.Seconds(),
		speedStr,
		filePath,
	)

	return ctx.Edit(text)
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
