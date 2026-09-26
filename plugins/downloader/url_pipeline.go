package downloader

import (
	"context"
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type urlDownloadRequest struct {
	URL       string
	Provider  string
	TaskRoot  string
	Mode      download.MediaMode
	Format    download.MediaFormat
	MaxHeight int
}

type urlPipelineHooks struct {
	Delivery        orchestration.MediaDelivery
	ProgressEdit    func(context.Context, string) error
	DownloadFailure func(context.Context) error
	DeliveryFailure func(context.Context) error
	Delivered       func(context.Context) error
	Cancel          func() bool
	TargetKind      string
}

func (p *Plugin) submitURLPipeline(admissionCtx context.Context, request urlDownloadRequest, hooks urlPipelineHooks) error {
	if p == nil || p.tasks == nil || hooks.Delivery == nil {
		return fmt.Errorf("%w: downloader URL pipeline is unavailable", core.ErrUnavailable)
	}
	p.ensureRegistry()
	if p.registry == nil {
		return fmt.Errorf("%w: downloader registry unavailable", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}

	normalized, err := normalizeInteractiveURL(request.URL)
	if err != nil {
		return err
	}
	request.URL = normalized
	provider := p.registry.Resolve(request.URL)
	if provider == nil {
		return download.ErrNoMatchingProvider
	}
	if request.Provider == "" {
		request.Provider = provider.Name()
	} else if request.Provider != provider.Name() {
		return fmt.Errorf("%w: downloader provider changed or disappeared", core.ErrUnavailable)
	}
	if strings.TrimSpace(request.TaskRoot) == "" {
		request.TaskRoot = string(p.nextTaskID("url"))
	}

	downloadTaskID := urlPipelineTaskID(request.TaskRoot, "download")
	deliveryTaskID := urlPipelineTaskID(request.TaskRoot, "delivery")
	continuationCtx := context.Background()

	var (
		asset       *storage.Asset
		targetStore storage.Storage
	)
	spec := tasks.WorkSpec{
		ID:               downloadTaskID,
		QuotaOwner:       tasks.OwnerID("plugin:downloader"),
		Pool:             tasks.PoolID("download"),
		Class:            tasks.PriorityNormal,
		ExecutionTimeout: downloaderExecutionTimeout,
		Input:            []byte(request.URL),
		Resources:        p.urlResources(request.URL),
		Handler: func(taskCtx context.Context) error {
			targetStore = p.storage
			if targetStore == nil {
				targetStore = storage.NewMemoryStorage()
			}
			provider := p.registry.Resolve(request.URL)
			reporter := newDownloadProgressReporter(
				taskCtx,
				hooks.ProgressEdit,
				request.Provider,
				request.Mode,
				request.Format,
				request.MaxHeight,
			)
			var progress download.ProgressCallback
			if reporter != nil {
				progress = reporter.Callback
			}

			var downloadErr error
			asset, downloadErr = p.registry.Download(taskCtx, request.URL, targetStore, download.DownloadOptions{
				Timeout:   downloaderExecutionTimeout,
				MaxBytes:  500 * 1024 * 1024,
				Progress:  progress,
				Mode:      request.Mode,
				Format:    request.Format,
				MaxHeight: request.MaxHeight,
			})
			if reporter != nil {
				reporter.Close()
			}
			if downloadErr != nil {
				return downloadErr
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
			if result.Outcome == tasks.OutcomeCancelled {
				if hooks.Cancel != nil {
					hooks.Cancel()
				}
				return
			}
			if hooks.DownloadFailure != nil {
				if err := p.submitTerminalEdit(continuationCtx, "download-failed", func(editCtx context.Context) error {
					if err := hooks.DownloadFailure(editCtx); err != nil {
						if hooks.Cancel != nil {
							hooks.Cancel()
						}
						return err
					}
					return nil
				}); err != nil && hooks.Cancel != nil {
					hooks.Cancel()
				}
			}
			return
		}
		if asset == nil || targetStore == nil {
			if hooks.DownloadFailure != nil {
				if err := p.submitTerminalEdit(continuationCtx, "download-invalid-result", hooks.DownloadFailure); err != nil && hooks.Cancel != nil {
					hooks.Cancel()
				}
			}
			return
		}
		if err := p.submitRetainedDelivery(
			continuationCtx,
			deliveryTaskID,
			targetStore,
			asset,
			request.Mode,
			request.Format,
			hooks.Delivery,
			hooks.DeliveryFailure,
			hooks.Delivered,
			hooks.Cancel,
			hooks.TargetKind,
		); err != nil {
			if !isDeliveryLifecycleCancellation(err) && hooks.DeliveryFailure != nil {
				_ = p.submitTerminalEdit(continuationCtx, "delivery-submit-failed", hooks.DeliveryFailure)
			}
			if hooks.Cancel != nil {
				hooks.Cancel()
			}
		}
	}
	if _, err := p.tasks.Submit(admissionCtx, spec); err != nil {
		return fmt.Errorf("submit URL downloader task: %w", err)
	}
	return nil
}

func urlPipelineTaskID(root, stage string) tasks.TaskID {
	root = strings.TrimSpace(root)
	stage = strings.TrimSpace(stage)
	return tasks.TaskID(root + ":" + stage)
}
