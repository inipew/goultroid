package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

// Plugin provides media download capabilities for Telegram media and external URLs.
type Plugin struct {
	registry *download.Registry
	storage  storage.Storage
	jobs     *jobs.Manager
	jobUI    sync.Map // job ID -> *core.Context; transient notification only
}

type downloadJobPayload struct {
	URL string `json:"url"`
}

type mediaJobPayload struct {
	SaveDir   string `json:"save_dir"`
	MediaSize int64  `json:"media_size"`
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
		case *jobs.Manager:
			p.jobs = v
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

// SetJobsManager sets the jobs manager and registers typed download handlers.
func (p *Plugin) SetJobsManager(jm *jobs.Manager) {
	p.jobs = jm
	p.registerJobHandlers()
}

// InitPlugin initializes the plugin using PluginContext.
func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	jobsMgr, err := pctx.Jobs()
	if err == nil && jobsMgr != nil {
		p.jobs = jobsMgr
		p.registerJobHandlers()
	}
	return nil
}

func (p *Plugin) registerJobHandlers() {
	if p.jobs == nil {
		return
	}
	_ = p.jobs.RegisterHandler("downloader.url", func(ctx context.Context, j jobs.JobDefinition) error {
		var payload downloadJobPayload
		if err := json.Unmarshal(j.Payload, &payload); err != nil {
			return fmt.Errorf("decode downloader job payload: %w", err)
		}
		if strings.TrimSpace(payload.URL) == "" {
			return errors.New("downloader job URL is empty")
		}
		var uiCtx *core.Context
		if value, ok := p.jobUI.LoadAndDelete(j.ID); ok {
			uiCtx, _ = value.(*core.Context)
		}
		reqCtx := ctx
		for _, r := range j.Resources {
			if r.Amount > 0 {
				reqCtx = download.WithResource(reqCtx, r.Name)
			}
		}
		return p.executeURLDownload(reqCtx, uiCtx, payload.URL)
	})
	_ = p.jobs.RegisterHandler("downloader.telegram_media", func(ctx context.Context, j jobs.JobDefinition) error {
		var payload mediaJobPayload
		if err := json.Unmarshal(j.Payload, &payload); err != nil {
			return fmt.Errorf("decode downloader media payload: %w", err)
		}
		value, ok := p.jobUI.LoadAndDelete(j.ID)
		if !ok {
			return errors.New("downloader media context expired")
		}
		uiCtx, _ := value.(*core.Context)
		if uiCtx == nil {
			return errors.New("downloader media context missing")
		}
		return p.executeMediaDownload(ctx, uiCtx, payload.SaveDir, payload.MediaSize)
	})
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
			Resources:   []tasks.ResourceRequirement{{Name: "download", Amount: 1}},
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
		return ctx.EditOrReply("⚠️ <b>No media or URL found!</b> Reply to a media message or provide a valid download URL.")
	}

	saveDir := filepath.Join("data", "downloads")
	if p.storage != nil && p.storage.BasePath() != "" {
		saveDir = p.storage.BasePath()
	}
	_ = core.EnforceDirectoryQuota(saveDir, core.DefaultDirectoryQuota, core.DefaultMaxFileAge)

	if mediaSize > 0 {
		if err := core.ValidateMediaSize(mediaSize, core.DefaultMaxDownloadSize); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("⚠️ <b>Media too large!</b> File size (%s) exceeds download limit (500MB).", formatBytes(mediaSize)))
		}
	}

	requiredSpace := mediaSize
	if requiredSpace <= 0 {
		requiredSpace = 50 * 1024 * 1024
	}
	if err := core.CheckDiskSpace(saveDir, requiredSpace); err != nil {
		return ctx.EditOrReply("❌ <b>Insufficient disk space</b> on host machine to complete download.")
	}

	if err := ctx.EditOrReply("⏳ Downloading media..."); err != nil {
		return err
	}

	if p.jobs != nil {
		jobID := fmt.Sprintf("dl-media-%d", time.Now().UnixNano())
		idempKey := ""
		if ctx.Message != nil {
			idempKey = fmt.Sprintf("dl:msg:%d:%d", ctx.ChatID(), ctx.Message.ID)
		}
		payload, err := json.Marshal(mediaJobPayload{SaveDir: saveDir, MediaSize: mediaSize})
		if err != nil {
			return fmt.Errorf("encode media download job: %w", err)
		}
		job := jobs.JobDefinition{ID: jobID, ScopeOwner: "plugin:downloader", QuotaOwner: "telegram:download", HandlerType: "downloader.telegram_media", Payload: payload, Pool: "download", Class: "normal", Timeout: 10 * time.Minute, Resources: []tasks.ResourceRequirement{{Name: "download", Amount: 1}}, Enabled: true}
		_ = idempKey
		if err := p.jobs.Register(job); err == nil {
			p.jobUI.Store(jobID, ctx)
			return p.jobs.Trigger(ctx.Ctx, jobID)
		}
	}

	return p.executeMediaDownload(ctx.Ctx, ctx, saveDir, mediaSize)
}

func (p *Plugin) executeMediaDownload(taskCtx context.Context, ctx *core.Context, saveDir string, mediaSize int64) error {
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
		"📥 <b>Download Complete!</b>\n\n"+
			"📁 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n"+
			"⏱️ <b>Time:</b> <code>%.2fs</code> (%s)\n"+
			"📍 <b>Saved to:</b> <code>%s</code>",
		core.EscapeHTML(fileName),
		sizeStr,
		duration.Seconds(),
		speedStr,
		core.EscapeHTML(filePath),
	)

	return ctx.Edit(text)
}

func (p *Plugin) handleURLDownload(ctx *core.Context, rawURL string) error {
	if err := ctx.EditOrReply("⏳ <i>Downloading media from URL...</i>"); err != nil {
		return err
	}

	if p.jobs != nil {
		jobID := fmt.Sprintf("dl-url-%d", time.Now().UnixNano())
		idempKey := fmt.Sprintf("dl:url:%s", rawURL)
		payload, err := json.Marshal(downloadJobPayload{URL: rawURL})
		if err != nil {
			return fmt.Errorf("encode download job: %w", err)
		}
		reqResources := []tasks.ResourceRequirement{{Name: "download", Amount: 1}}
		if p.registry != nil {
			if prov := p.registry.Resolve(rawURL); prov != nil && prov.Name() == "extractor" {
				reqResources = append(reqResources, tasks.ResourceRequirement{Name: "process", Amount: 1})
			}
		}
		job := jobs.JobDefinition{ID: jobID, ScopeOwner: "plugin:downloader", QuotaOwner: "telegram:download", HandlerType: "downloader.url", Payload: payload, Pool: "download", Class: "normal", Timeout: 10 * time.Minute, Resources: reqResources, Enabled: true}
		_ = idempKey
		if err := p.jobs.Register(job); err == nil {
			p.jobUI.Store(jobID, ctx)
			if err := p.jobs.Trigger(ctx.Ctx, jobID); err != nil {
				p.jobUI.Delete(jobID)
				return err
			}
			return nil
		}
	}

	return p.executeURLDownload(ctx.Ctx, ctx, rawURL)
}

func (p *Plugin) executeURLDownload(taskCtx context.Context, ctx *core.Context, rawURL string) error {
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

	asset, err := p.registry.Download(taskCtx, rawURL, targetStore, opts)
	if err != nil {
		if ctx != nil {
			return ctx.Edit(fmt.Sprintf("❌ <b>URL Download Failed</b>: %v", err))
		}
		return fmt.Errorf("URL download failed: %w", err)
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
		"📥 <b>URL Download Complete!</b>\n\n"+
			"📁 <b>File:</b> <code>%s</code>\n"+
			"📦 <b>Size:</b> <code>%s</code>\n"+
			"⏱️ <b>Time:</b> <code>%.2fs</code> (%s)\n"+
			"📍 <b>Path:</b> <code>%s</code>",
		core.EscapeHTML(asset.Name),
		sizeStr,
		duration.Seconds(),
		speedStr,
		core.EscapeHTML(asset.Path),
	)

	if ctx != nil {
		return ctx.Edit(text)
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
