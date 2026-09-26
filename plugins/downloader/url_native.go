package downloader

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
)

func (p *Plugin) startNativeURLDownload(ctx *core.Context, normalizedURL, providerName string) error {
	if ctx == nil || ctx.Svc == nil || ctx.PeerID == nil {
		return core.ErrInvalidArgs
	}
	if p == nil || p.tasks == nil {
		return fmt.Errorf("%w: downloader TaskEngine client is not configured", core.ErrUnavailable)
	}
	if err := ctx.Progress("Preparing URL download..."); err != nil {
		return err
	}

	anchorID := ctx.LastResponseID
	if anchorID <= 0 && ctx.Message != nil {
		anchorID = ctx.Message.ID
	}
	if anchorID <= 0 {
		return fmt.Errorf("%w: downloader status message is unavailable", core.ErrUnavailable)
	}
	topicID := 0
	deliveryReplyID := anchorID
	if ctx.Message != nil {
		topicID = ctx.Message.TopicID
		if ctx.Message.ReplyToID > 0 {
			deliveryReplyID = ctx.Message.ReplyToID
		}
	}

	svc := ctx.Svc
	peer := ctx.PeerID
	edit := func(editCtx context.Context, text string) error {
		if editCtx == nil {
			editCtx = context.Background()
		}
		return svc.EditMessage(editCtx, peer, anchorID, text)
	}
	deliveryBase := &core.Context{
		Svc:    svc,
		PeerID: peer,
		Message: &core.Message{
			ID:      deliveryReplyID,
			TopicID: topicID,
		},
	}
	delivery := func(deliveryCtx context.Context, media presentation.Media) error {
		if deliveryCtx == nil {
			deliveryCtx = context.Background()
		}
		callCtx := *deliveryBase
		callCtx.Ctx = deliveryCtx
		_, err := callCtx.SendMedia(media.Type, media.Path, media.Caption)
		return err
	}

	request := urlDownloadRequest{
		URL:       normalizedURL,
		Provider:  providerName,
		TaskRoot:  string(p.nextTaskID("native-url")),
		Mode:      download.MediaModeDefault,
		Format:    download.MediaFormatDefault,
		MaxHeight: 0,
	}
	hooks := urlPipelineHooks{
		Delivery:     delivery,
		ProgressEdit: edit,
		DownloadFailure: func(editCtx context.Context) error {
			return edit(editCtx, "❌ <b>Download failed.</b> Retry the command to start again.")
		},
		DeliveryFailure: func(editCtx context.Context) error {
			return edit(editCtx, deliveryFailedView().Text)
		},
		Delivered: func(editCtx context.Context) error {
			return edit(editCtx, deliveredView().Text)
		},
		TargetKind: "message",
	}
	if err := p.submitURLPipeline(ctx.Ctx, request, hooks); err != nil {
		_ = edit(context.Background(), "❌ <b>Unable to start download.</b> Please retry the command.")
		return err
	}
	return nil
}
