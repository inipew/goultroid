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

	title := strings.TrimSpace(asset.Title)
	if title == "" {
		title = strings.TrimSuffix(asset.Name, filepath.Ext(asset.Name))
	}
	selection := strings.TrimPrefix(strings.ToLower(filepath.Ext(asset.Name)), ".")
	if selection == "" {
		selection = "file"
	}
	if mode != download.MediaModeDefault {
		selection = string(mode) + " / " + string(format)
	}

	lines := []string{
		"✅ <b>Download complete</b>",
		"",
		"<b>Title:</b> <code>" + core.EscapeHTML(title) + "</code>",
	}
	if performer := strings.TrimSpace(asset.Performer); performer != "" {
		lines = append(lines, "<b>Artist:</b> <code>"+core.EscapeHTML(performer)+"</code>")
	}
	lines = append(lines,
		"<b>File:</b> <code>"+core.EscapeHTML(asset.Name)+"</code>",
		"<b>Size:</b> <code>"+formatBytes(asset.Size)+"</code>",
		"<b>Format:</b> <code>"+core.EscapeHTML(selection)+"</code>",
	)
	if asset.Width > 0 && asset.Height > 0 {
		lines = append(lines, fmt.Sprintf("<b>Resolution:</b> <code>%dx%d</code>", asset.Width, asset.Height))
	}
	if asset.Duration > 0 {
		lines = append(lines, "<b>Duration:</b> <code>"+formatProgressDuration(asset.Duration)+"</code>")
	}
	return strings.Join(lines, "\n")
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
	cancelSession func() bool,
	targetKind string,
) error {
	return p.submitURLPipeline(admissionCtx, urlDownloadRequest{
		URL:       state.URL,
		Provider:  state.Provider,
		TaskRoot:  state.TaskRoot,
		Mode:      mode,
		Format:    format,
		MaxHeight: state.MaxHeight,
	}, urlPipelineHooks{
		Delivery:        delivery,
		ProgressEdit:    progressEdit,
		DownloadFailure: downloadFailure,
		DeliveryFailure: deliveryFailure,
		Delivered:       delivered,
		Cancel:          cancelSession,
		TargetKind:      targetKind,
	})
}

func (p *Plugin) submitRetainedDelivery(
	admissionCtx context.Context,
	taskID tasks.TaskID,
	store storage.Storage,
	asset *storage.Asset,
	mode download.MediaMode,
	format download.MediaFormat,
	delivery orchestration.MediaDelivery,
	deliveryFailure func(context.Context) error,
	delivered func(context.Context) error,
	cancelSession func() bool,
	targetKind string,
) error {
	if p == nil || p.tasks == nil || store == nil || asset == nil || delivery == nil {
		return fmt.Errorf("%w: downloader media delivery is unavailable", core.ErrUnavailable)
	}
	spec := tasks.WorkSpec{
		ID:               taskID,
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
				Type:      interactiveDeliveryMediaType(mode, asset),
				Path:      path,
				FileName:  asset.Name,
				MIMEType:  asset.MIME,
				Title:     asset.Title,
				Performer: asset.Performer,
				Duration:  asset.Duration,
				Width:     asset.Width,
				Height:    asset.Height,
				Caption:   interactiveDeliveryCaption(asset, mode, format),
			})
		},
	}
	spec.OnComplete = func(result tasks.TaskResult) {
		defer func() {
			if cancelSession != nil {
				cancelSession()
			}
		}()
		if result.IsSuccess() {
			if targetKind == "message" && delivered != nil {
				_ = p.submitTerminalEdit(context.Background(), "delivered", delivered)
			}
			return
		}
		if result.Outcome != tasks.OutcomeCancelled && deliveryFailure != nil {
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
	return presentation.View{Text: "⚠️ <b>Download retained, but Telegram delivery failed.</b>\nReopen the downloader to retry. The retained asset remains safely stored."}
}

func deliveredView() presentation.View {
	return presentation.View{Text: "✅ <b>Download delivered to Telegram.</b>"}
}

func interactivePipelineTaskID(root, stage string) tasks.TaskID {
	root = strings.TrimSpace(root)
	stage = strings.TrimSpace(stage)
	return tasks.TaskID(root + ":" + stage)
}

func (p *Plugin) cancelInteractivePipeline(root string) error {
	if p == nil || p.tasks == nil || strings.TrimSpace(root) == "" {
		return fmt.Errorf("%w: downloader task cancellation is unavailable", core.ErrUnavailable)
	}
	var joined error
	for _, stage := range []string{"download", "delivery"} {
		if _, err := p.tasks.Cancel(interactivePipelineTaskID(root, stage), tasks.CauseUserCancel); err != nil && !errors.Is(err, tasks.ErrTaskNotFound) {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}
