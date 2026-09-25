package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	downloaderDeliveryTimeout = 30 * time.Minute
	downloaderStatusTimeout   = 30 * time.Second
)

func interactiveDeliveryMediaType(mode download.MediaMode, asset *storage.Asset) string {
	switch mode {
	case download.MediaModeAudio:
		return "audio"
	case download.MediaModeVideo:
		return "video"
	}
	if asset != nil {
		mime := strings.ToLower(strings.TrimSpace(asset.MIME))
		switch {
		case strings.HasPrefix(mime, "audio/"):
			return "audio"
		case strings.HasPrefix(mime, "video/"):
			return "video"
		case strings.HasPrefix(mime, "image/"):
			return "photo"
		}
	}
	return "file"
}

func interactiveDeliveryCaption(asset *storage.Asset, mode download.MediaMode, format download.MediaFormat) string {
	if asset == nil {
		return "✅ <b>Download complete</b>"
	}
	selection := "file"
	if mode != download.MediaModeDefault {
		selection = string(mode) + " / " + string(format)
	}
	return fmt.Sprintf(
		"✅ <b>Download complete</b>\n\n<b>Title:</b> <code>%s</code>\n<b>Size:</b> <code>%s</code>\n<b>Format:</b> <code>%s</code>",
		core.EscapeHTML(asset.Name),
		formatBytes(asset.Size),
		core.EscapeHTML(selection),
	)
}

func (p *Plugin) materializeDeliveryAsset(ctx context.Context, store storage.Storage, asset *storage.Asset) (string, func(), error) {
	if store == nil || asset == nil || strings.TrimSpace(asset.ID) == "" {
		return "", nil, fmt.Errorf("%w: retained downloader asset is unavailable", core.ErrInvalidArgs)
	}
	if path := strings.TrimSpace(asset.Path); path != "" && !strings.HasPrefix(path, "memory://") {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			if info.Size() > core.DefaultMaxUploadSize {
				return "", nil, fmt.Errorf("%w: retained asset exceeds Telegram upload limit", core.ErrMediaTooLarge)
			}
			return path, func() {}, nil
		}
	}
	if p == nil || p.files == nil {
		return "", nil, fmt.Errorf("%w: downloader temp filesystem is unavailable for retained asset materialization", core.ErrUnavailable)
	}
	src, err := store.Open(ctx, asset.ID)
	if err != nil {
		return "", nil, fmt.Errorf("open retained asset for delivery: %w", err)
	}
	defer src.Close()

	ext := filepath.Ext(core.SanitizeFileName(asset.Name))
	tmp, err := p.files.CreateTempFile("delivery-*" + ext)
	if err != nil {
		return "", nil, fmt.Errorf("create delivery temp file: %w", err)
	}
	cleanup := func() { _ = p.files.RemoveTempFile(tmp.Name()) }
	written, copyErr := io.Copy(tmp, io.LimitReader(src, core.DefaultMaxUploadSize+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("materialize retained asset: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("close delivery temp file: %w", closeErr)
	}
	if written > core.DefaultMaxUploadSize {
		cleanup()
		return "", nil, fmt.Errorf("%w: retained asset exceeds Telegram upload limit", core.ErrMediaTooLarge)
	}
	return tmp.Name(), cleanup, nil
}

func (p *Plugin) submitTerminalEdit(admissionCtx context.Context, kind string, edit func(context.Context) error) error {
	if p == nil || p.tasks == nil || edit == nil {
		return fmt.Errorf("%w: downloader terminal edit is unavailable", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}
	_, err := p.tasks.Submit(admissionCtx, tasks.WorkSpec{
		ID:               p.nextTaskID("terminal-" + strings.TrimSpace(kind)),
		QuotaOwner:       tasks.OwnerID("plugin:downloader"),
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		ExecutionTimeout: downloaderStatusTimeout,
		Handler:          edit,
	})
	if err != nil {
		return fmt.Errorf("submit downloader terminal edit: %w", err)
	}
	return nil
}

func (p *Plugin) submitInteractivePipeline(
	admissionCtx context.Context,
	state interactiveState,
	mode download.MediaMode,
	format download.MediaFormat,
	delivery orchestration.MediaDelivery,
	progressEdit func(context.Context, string) error,
	downloadFailure func(context.Context) error,
	deliveryFailure func(context.Context) error,
	delivered func(context.Context) error,
	targetKind string,
) error {
	if p == nil || p.tasks == nil || delivery == nil {
		return fmt.Errorf("%w: downloader delivery pipeline is unavailable", core.ErrUnavailable)
	}
	p.ensureRegistry()
	if p.registry == nil {
		return fmt.Errorf("%w: downloader registry unavailable", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}

	var (
		asset       *storage.Asset
		targetStore storage.Storage
	)
	spec := tasks.WorkSpec{
		ID:               p.nextTaskID("interactive-download"),
		QuotaOwner:       tasks.OwnerID("plugin:downloader"),
		Pool:             tasks.PoolID("download"),
		Class:            tasks.PriorityNormal,
		ExecutionTimeout: downloaderExecutionTimeout,
		Input:            []byte(state.URL),
		Resources:        p.urlResources(state.URL),
		Handler: func(taskCtx context.Context) error {
			targetStore = p.storage
			if targetStore == nil {
				targetStore = storage.NewMemoryStorage()
			}
			provider := p.registry.Resolve(state.URL)
			reporter := newDownloadProgressReporter(taskCtx, progressEdit, state.Provider, mode, format)
			var progress download.ProgressCallback
			if reporter != nil {
				progress = reporter.Callback
			}

			var err error
			asset, err = p.registry.Download(taskCtx, state.URL, targetStore, download.DownloadOptions{
				Timeout:  downloaderExecutionTimeout,
				MaxBytes: 500 * 1024 * 1024,
				Progress: progress,
				Mode:     mode,
				Format:   format,
			})
			if reporter != nil {
				reporter.Close()
			}
			if err != nil {
				return err
			}
			producer := "downloader.unknown"
			if provider != nil {
				producer = downloaderProviderProducer(provider.Name())
			}
			return p.registerRetainedAsset(taskCtx, targetStore, asset, producer)
		},
	}
	spec.OnComplete = func(result tasks.TaskResult) {
		if !result.IsSuccess() {
			if result.Outcome != tasks.OutcomeCancelled && result.Outcome != tasks.OutcomeTimedOut && downloadFailure != nil {
				_ = p.submitTerminalEdit(context.Background(), "download-failed", downloadFailure)
			}
			return
		}
		if asset == nil || targetStore == nil {
			if downloadFailure != nil {
				_ = p.submitTerminalEdit(context.Background(), "download-invalid-result", downloadFailure)
			}
			return
		}
		if err := p.submitRetainedDelivery(
			context.Background(),
			targetStore,
			asset,
			mode,
			format,
			delivery,
			deliveryFailure,
			delivered,
			targetKind,
		); err != nil && deliveryFailure != nil && !isDeliveryLifecycleCancellation(err) {
			_ = p.submitTerminalEdit(context.Background(), "delivery-submit-failed", deliveryFailure)
		}
	}
	_, err := p.tasks.Submit(admissionCtx, spec)
	if err != nil {
		return fmt.Errorf("submit interactive downloader task: %w", err)
	}
	return nil
}

func (p *Plugin) submitRetainedDelivery(
	admissionCtx context.Context,
	store storage.Storage,
	asset *storage.Asset,
	mode download.MediaMode,
	format download.MediaFormat,
	delivery orchestration.MediaDelivery,
	deliveryFailure func(context.Context) error,
	delivered func(context.Context) error,
	targetKind string,
) error {
	if p == nil || p.tasks == nil || store == nil || asset == nil || delivery == nil {
		return fmt.Errorf("%w: downloader media delivery is unavailable", core.ErrUnavailable)
	}
	spec := tasks.WorkSpec{
		ID:               p.nextTaskID("media-delivery"),
		QuotaOwner:       tasks.OwnerID("plugin:downloader"),
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		ExecutionTimeout: downloaderDeliveryTimeout,
		Input:            []byte(asset.ID),
		Resources:        []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
		Handler: func(taskCtx context.Context) error {
			path, cleanup, err := p.materializeDeliveryAsset(taskCtx, store, asset)
			if err != nil {
				return err
			}
			defer cleanup()
			return delivery(taskCtx, presentation.Media{
				Type:     interactiveDeliveryMediaType(mode, asset),
				Path:     path,
				FileName: asset.Name,
				MIMEType: asset.MIME,
				Caption:  interactiveDeliveryCaption(asset, mode, format),
			})
		},
	}
	spec.OnComplete = func(result tasks.TaskResult) {
		if result.IsSuccess() {
			if targetKind == "message" && delivered != nil {
				_ = p.submitTerminalEdit(context.Background(), "delivered", delivered)
			}
			return
		}
		if result.Outcome != tasks.OutcomeCancelled && result.Outcome != tasks.OutcomeTimedOut && deliveryFailure != nil {
			_ = p.submitTerminalEdit(context.Background(), "delivery-failed", deliveryFailure)
		}
	}
	_, err := p.tasks.Submit(admissionCtx, spec)
	if err != nil {
		return fmt.Errorf("submit retained media delivery task: %w", err)
	}
	return nil
}

func isDeliveryLifecycleCancellation(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, tasks.ErrScopeClosed)
}

func deliveryFailedView() presentation.View {
	return presentation.View{Text: "⚠️ <b>Download retained, but Telegram delivery failed.</b>\nThe retained asset was kept safely for retry."}
}

func deliveredView() presentation.View {
	return presentation.View{Text: "✅ <b>Download delivered to Telegram.</b>"}
}
