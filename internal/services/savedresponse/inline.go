package savedresponse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
)

var ErrInlineMediaNotRepresentable = errors.New("saved response: inline media response cannot preserve delivery semantics")

// InlineSource exposes persistent SurfaceInline bindings through Inline vNext
// without registering one handler per alias.
type InlineSource struct {
	bindings  *BindingService
	responses *Service
}

func NewInlineSource(bindings *BindingService, responses *Service) *InlineSource {
	return &InlineSource{bindings: bindings, responses: responses}
}

func (s *InlineSource) Prepare(ctx context.Context, rawQuery string) (inlineservice.DynamicPrepared, bool, error) {
	if s == nil || s.bindings == nil {
		return inlineservice.DynamicPrepared{}, false, nil
	}
	fields := strings.Fields(strings.TrimSpace(rawQuery))
	if len(fields) == 0 {
		return inlineservice.DynamicPrepared{}, false, nil
	}
	alias := strings.ToLower(fields[0])
	prepared, err := s.bindings.Prepare(ctx, SurfaceInline, alias)
	switch {
	case err == nil:
	case errors.Is(err, ErrBindingNotFound),
		errors.Is(err, ErrBindingDisabled),
		errors.Is(err, ErrInvalidBinding):
		return inlineservice.DynamicPrepared{}, false, nil
	default:
		return inlineservice.DynamicPrepared{}, true, err
	}

	resources := make([]tasks.ResourceRequirement, 0, 1)
	if prepared.HasMedia() {
		resources = append(resources, tasks.ResourceRequirement{Name: "media", Amount: 1})
	}
	return inlineservice.DynamicPrepared{
		Pattern:   alias,
		Args:      append([]string(nil), fields[1:]...),
		Scope:     prepared.Scope(),
		Resources: resources,
		State:     prepared,
	}, true, nil
}

func (s *InlineSource) Execute(
	ctx context.Context,
	prepared inlineservice.DynamicPrepared,
	inlineCtx *inlineservice.InlineContext,
) (*inlineservice.InlineResponse, error) {
	if s == nil || s.bindings == nil || s.responses == nil {
		return nil, ErrResponseDeliveryUnavailable
	}
	binding, ok := prepared.State.(PreparedBinding)
	if !ok {
		return nil, ErrBindingStale
	}
	resolved, err := s.bindings.ResolvePrepared(ctx, binding)
	if err != nil {
		return nil, err
	}

	responseValue := resolved.Resolved.Response
	if responseValue.Kind() == "sticker" && strings.TrimSpace(responseValue.Text) != "" {
		return nil, fmt.Errorf("%w: alias %q requires separate text", ErrInlineMediaNotRepresentable, resolved.Binding.Alias)
	}

	vars := TemplateVars{Now: time.Now()}
	if inlineCtx != nil {
		vars.UserID = inlineCtx.UserID
	}
	rendered, err := s.responses.Prepare(ctx, responseValue, vars)
	if err != nil {
		return nil, err
	}

	alias := resolved.Binding.Alias
	result := inlineservice.InlineResult{
		ID:          inlineResultID(resolved.Binding),
		Title:       alias,
		Description: "Saved response · " + resolved.Resolved.Response.Kind(),
	}
	response := &inlineservice.InlineResponse{
		Results:   []inlineservice.InlineResult{result},
		Cache:     inlineservice.CacheNone,
		CacheTime: 0,
		Private:   true,
	}

	if rendered.MediaPath == "" {
		response.Results[0].Type = inlineservice.ResultArticle
		response.Results[0].Text = rendered.Text
		rendered.Cleanup()
		return response, nil
	}

	// Inline selection can emit only one media message. Preserve the exact
	// SavedResponse contract: if ordinary delivery requires media plus a
	// separate standalone text message (long caption or sticker text), refuse
	// rather than silently dropping that text.
	if strings.TrimSpace(rendered.Text) != "" {
		rendered.Cleanup()
		return nil, fmt.Errorf("%w: alias %q requires separate text", ErrInlineMediaNotRepresentable, alias)
	}

	response.Results[0].Type = inlineResultType(rendered.MediaType)
	response.Results[0].Text = rendered.Caption
	response.Results[0].MediaMimeType = mediaMIME(resolved.Resolved.Response)
	response.Results[0].LocalMedia = &inlineservice.LocalMedia{
		Path:      rendered.MediaPath,
		MediaType: rendered.MediaType,
		FileName:  mediaName(resolved.Resolved.Response),
		MIMEType:  mediaMIME(resolved.Resolved.Response),
	}
	response.Finalize = rendered.Cleanup
	return response, nil
}

func inlineResultID(binding SurfaceBinding) string {
	id := strings.TrimSpace(binding.Incarnation)
	if id == "" {
		id = fmt.Sprintf("%d", binding.Revision)
	}
	return "saved_" + id
}

func inlineResultType(mediaType string) inlineservice.InlineResultType {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "photo":
		return inlineservice.ResultPhoto
	case "video":
		return inlineservice.ResultVideo
	case "audio":
		return inlineservice.ResultAudio
	case "sticker":
		return inlineservice.ResultSticker
	default:
		return inlineservice.ResultDocument
	}
}

func mediaName(response Response) string {
	if response.Media == nil {
		return ""
	}
	return strings.TrimSpace(response.Media.Name)
}

func mediaMIME(response Response) string {
	if response.Media == nil {
		return ""
	}
	return strings.TrimSpace(response.Media.MIMEType)
}

var _ inlineservice.DynamicSource = (*InlineSource)(nil)
