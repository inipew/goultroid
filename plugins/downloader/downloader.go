package downloader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
)

// Plugin provides media download capabilities for Telegram media and external URLs.
type Plugin struct {
	registry *download.Registry
	storage  storage.Storage
}

// New creates a new downloader Plugin instance with optional dependencies.
func New(deps ...any) *Plugin {
	p := &Plugin{}
	for _, dep := range deps {
		switch v := dep.(type) {
		case *download.Registry:
			p.registry = v
		case storage.Storage:
			p.storage = v
		}
	}
	return p
}

// SetRegistry sets the download registry.
func (p *Plugin) SetRegistry(reg *download.Registry) {
	p.registry = reg
}

// SetStorage sets the storage manager.
func (p *Plugin) SetStorage(store storage.Storage) {
	p.storage = store
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "downloader"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	if p.registry == nil {
		p.registry = download.NewRegistry(
			download.NewExtractorProvider(nil, 500*1024*1024),
			download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
		)
	}
	if p.storage == nil {
		fs, err := storage.NewFileStorage(filepath.Join("data", "downloads"), 5*1024*1024*1024)
		if err == nil {
			p.storage = fs
		}
	}
	return nil
}

// Commands returns the list of commands provided by this plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "download",
			Aliases:     []string{"dl"},
			Description: "Download media from replied message or URL",
			Usage:       ".download [url] (or reply to media)",
			Category:    "Media",
			Permission:  core.PermissionSudo,
			ReplyOnly:   false,
			Cooldown:    3 * time.Second,
			Timeout:     10 * time.Minute,
			Handler:     p.handleDownload,
		},
	}
}

func (p *Plugin) handleDownload(ctx *core.Context) error {
	// 1. Check if a URL was provided as an argument
	if len(ctx.Args) > 0 {
		targetURL := strings.TrimSpace(ctx.Args[0])
		if strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://") {
			return p.handleURLDownload(ctx, targetURL)
		}
	}

	// 2. Otherwise, check for media in replied or current message
	var mediaSize int64
	if ctx.Message != nil && ctx.Message.Media != nil {
		mediaSize = ctx.Message.Media.Size
	} else if reply, err := ctx.GetReply(); err == nil && reply != nil && reply.Media != nil {
		mediaSize = reply.Media.Size
	} else {
		return ctx.Reply("⚠️ <b>No media or URL found!</b> Reply to a media message or provide a valid download URL.")
	}

	saveDir := filepath.Join("data", "downloads")
	if p.storage != nil && p.storage.BasePath() != "" {
		saveDir = p.storage.BasePath()
	}
	_ = core.EnforceDirectoryQuota(saveDir, core.DefaultDirectoryQuota, core.DefaultMaxFileAge)

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

func (p *Plugin) handleURLDownload(ctx *core.Context, rawURL string) error {
	if err := ctx.Reply("⏳ <i>Downloading media from URL...</i>"); err != nil {
		return err
	}

	if p.registry == nil {
		_ = p.Init()
	}

	targetStore := p.storage
	if targetStore == nil {
		targetStore = storage.NewMemoryStorage()
	}

	start := time.Now()
	opts := download.DownloadOptions{
		Timeout:  10 * time.Minute,
		MaxBytes: 500 * 1024 * 1024,
	}

	asset, err := p.registry.Download(ctx.Ctx, rawURL, targetStore, opts)
	if err != nil {
		return ctx.Edit(fmt.Sprintf("❌ <b>URL Download Failed</b>: %v", err))
	}

	duration := time.Since(start)
	sizeStr := formatBytes(asset.Size)

	var speedStr string
	if duration.Seconds() > 0 && asset.Size > 0 {
		mbps := (float64(asset.Size) / 1024 / 1024) / duration.Seconds()
		speedStr = fmt.Sprintf("%.2f MB/s", mbps)
	} else {
		speedStr = "fast"
	}

	text := fmt.Sprintf(
		"📥 **URL Download Complete!**\n\n"+
			"📁 **File:** `%s`\n"+
			"📦 **Size:** `%s`\n"+
			"⏱️ **Time:** `%.2fs` (%s)\n"+
			"📍 **Path:** `%s`",
		asset.Name,
		sizeStr,
		duration.Seconds(),
		speedStr,
		asset.Path,
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
