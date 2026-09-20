package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

const downloaderExecutionTimeout = 10 * time.Minute

var downloaderTaskSequence atomic.Uint64

// Plugin provides media download capabilities for Telegram media and external URLs.
type Plugin struct {
	registry *download.Registry
	storage  storage.Storage
	tasks    tasks.Client
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
		case tasks.Client:
			p.tasks = v
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

// SetTaskClient sets the scoped TaskEngine client used for download continuations.
func (p *Plugin) SetTaskClient(client tasks.Client) {
	p.tasks = client
}

// InitPlugin initializes the plugin using capability-gated runtime services.
func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("initialize downloader task client: %w", err)
	}
	p.tasks = client
	return nil
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
			Timeout:     downloaderExecutionTimeout,
			// The interactive command only performs planning and admission.
			// Resource ownership belongs to the continuation task that performs I/O.
			Handler: p.handleDownload,
		},
	}
}

func (p *Plugin) nextTaskID(kind string) tasks.TaskID {
	return tasks.TaskID(fmt.Sprintf(
		"downloader:%s:%d:%d",
		kind,
		time.Now().UnixNano(),
		downloaderTaskSequence.Add(1),
	))
}

func detachDownloadContext(ctx *core.Context) *core.Context {
	if ctx == nil {
		return nil
	}
	cp := *ctx
	// The continuation receives its TaskEngine context immediately before use.
	// Drop command-only references so the queued closure does not retain the
	// entire invocation graph after admission.
	cp.Ctx = nil
	cp.Args = nil
	cp.RawArgs = ""
	cp.Album = nil
	cp.Chat = nil
	cp.Sender = nil
	cp.Perms = nil
	cp.Principal = nil
	cp.Resolver = nil
	cp.Localizer = nil
	cp.EventBus = nil
	cp.DelayedActions = nil
	return &cp
}

func markHeldResources(ctx context.Context, resources []tasks.ResourceRequirement) context.Context {
	for _, requirement := range resources {
		if requirement.Amount > 0 {
			ctx = core.WithHeldResource(ctx, requirement.Name)
			ctx = download.WithResource(ctx, requirement.Name)
		}
	}
	return ctx
}

func (p *Plugin) submitContinuation(
	admissionCtx context.Context,
	kind string,
	input []byte,
	resources []tasks.ResourceRequirement,
	handler func(context.Context) error,
) error {
	if p.tasks == nil {
		return fmt.Errorf("%w: downloader TaskEngine client is not configured", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}
	resources = append([]tasks.ResourceRequirement(nil), resources...)
	input = append([]byte(nil), input...)

	_, err := p.tasks.Submit(admissionCtx, tasks.WorkSpec{
		ID:               p.nextTaskID(kind),
		QuotaOwner:       tasks.OwnerID("plugin:downloader"),
		Pool:             tasks.PoolID("download"),
		Class:            tasks.PriorityNormal,
		ExecutionTimeout: downloaderExecutionTimeout,
		Input:            input,
		Resources:        resources,
		Handler: func(taskCtx context.Context) error {
			return handler(markHeldResources(taskCtx, resources))
		},
	})
	if err != nil {
		return fmt.Errorf("submit %s download task: %w", kind, err)
	}
	return nil
}

func (p *Plugin) ensureRegistry() {
	if p.registry == nil {
		_ = p.Init()
	}
}

func (p *Plugin) urlResources(rawURL string) []tasks.ResourceRequirement {
	resources := []tasks.ResourceRequirement{{Name: "download", Amount: 1}}
	p.ensureRegistry()
	if p.registry != nil {
		if provider := p.registry.Resolve(rawURL); provider != nil && provider.Name() == "extractor" {
			resources = append(resources, tasks.ResourceRequirement{Name: "process", Amount: 1})
		}
	}
	return resources
}

func (p *Plugin) handleDownload(ctx *core.Context) error {
	// 1. Check if a URL was provided as an argument.
	if len(ctx.Args) > 0 {
		targetURL := strings.TrimSpace(ctx.Args[0])
		if strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://") {
			return p.handleURLDownload(ctx, targetURL)
		}
	}

	// 2. Otherwise, check for media in replied or current message.
	var targetMedia *core.MediaInfo
	var mediaSize int64

	if ctx.Message != nil && ctx.Message.Media != nil && ctx.Message.Media.Location != nil {
		targetMedia = ctx.Message.Media
		mediaSize = targetMedia.Size
	} else {
		reply, err := ctx.GetReply()
		if err != nil {
			return fmt.Errorf("resolve replied message: %w", err)
		}
		if reply != nil {
			if reply.Media != nil && reply.Media.Location != nil {
				targetMedia = reply.Media
				mediaSize = targetMedia.Size
			} else if urls := reply.URLs(); len(urls) > 0 {
				return p.handleURLDownload(ctx, urls[0])
			}
		}
	}

	if targetMedia == nil {
		return ctx.EditOrReply("⚠️ <b>No media or URL found!</b> Reply to a media message or provide a valid download URL.")
	}
	if p.tasks == nil {
		return fmt.Errorf("%w: downloader TaskEngine client is not configured", core.ErrUnavailable)
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

	uiCtx := ctx.WithMedia(targetMedia)
	uiCtx = detachDownloadContext(uiCtx)
	resources := []tasks.ResourceRequirement{{Name: "download", Amount: 1}}
	input := []byte(fmt.Sprintf("%s\x00%d", saveDir, mediaSize))
	return p.submitContinuation(ctx.Ctx, "media", input, resources, func(taskCtx context.Context) error {
		return p.executeMediaDownload(taskCtx, uiCtx, saveDir, mediaSize)
	})
}

func (p *Plugin) executeMediaDownload(taskCtx context.Context, ctx *core.Context, saveDir string, mediaSize int64) error {
	if taskCtx != nil && ctx != nil {
		ctx = ctx.WithContext(taskCtx)
	}
	start := time.Now()

	filePath, err := ctx.DownloadMedia(saveDir)
	if err != nil {
		if ctx != nil {
			return ctx.Edit(fmt.Sprintf("❌ Download failed: %v", err))
		}
		return fmt.Errorf("download failed: %w", err)
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
	if p.tasks == nil {
		return fmt.Errorf("%w: downloader TaskEngine client is not configured", core.ErrUnavailable)
	}
	p.ensureRegistry()

	resources := p.urlResources(rawURL)
	if err := ctx.EditOrReply("⏳ <i>Downloading media from URL...</i>"); err != nil {
		return err
	}
	uiCtx := detachDownloadContext(ctx)
	return p.submitContinuation(ctx.Ctx, "url", []byte(rawURL), resources, func(taskCtx context.Context) error {
		return p.executeURLDownload(taskCtx, uiCtx, rawURL)
	})
}

func (p *Plugin) executeURLDownload(taskCtx context.Context, ctx *core.Context, rawURL string) error {
	if taskCtx != nil && ctx != nil {
		ctx = ctx.WithContext(taskCtx)
	}
	p.ensureRegistry()

	targetStore := p.storage
	if targetStore == nil {
		targetStore = storage.NewMemoryStorage()
	}

	start := time.Now()
	opts := download.DownloadOptions{
		Timeout:  downloaderExecutionTimeout,
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
