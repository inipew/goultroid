package savedresponse

import (
	"context"
	"errors"
	"strings"
)

// PrepareCompiled prepares a response using a template compiled from the same
// immutable Response. Cache the Response and CompiledTemplate as one pair.
func (s *Service) PrepareCompiled(
	ctx context.Context,
	response Response,
	compiled *CompiledTemplate,
	vars TemplateVars,
) (*Prepared, error) {
	if response.Empty() {
		return nil, ErrEmptyResponse
	}
	if compiled == nil {
		return nil, ErrNilTemplate
	}

	mediaID := response.MediaAssetID()
	if mediaID == "" {
		text, err := compiled.Render(vars, DefaultMaxOutputRunes)
		if err != nil {
			return nil, err
		}
		return &Prepared{Text: text}, nil
	}

	mediaType := "file"
	if response.Media != nil && strings.TrimSpace(response.Media.MediaType) != "" {
		mediaType = deliveryMediaType(response.Media.MediaType)
	}

	captionAllowed := mediaType != "sticker"
	if captionAllowed {
		caption, err := compiled.Render(vars, MaxCaptionRunes)
		if err == nil {
			path, cleanup, err := s.Materialize(ctx, response)
			if err != nil {
				return nil, err
			}
			return &Prepared{
				Caption: caption, MediaType: mediaType, MediaPath: path, cleanup: cleanup,
			}, nil
		}
		if !errors.Is(err, ErrRenderedTooLarge) {
			return nil, err
		}
	}

	text, err := compiled.Render(vars, DefaultMaxOutputRunes)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := s.Materialize(ctx, response)
	if err != nil {
		return nil, err
	}
	if mediaType == "sticker" {
		if err := validateStickerFile(path, response.Media); err != nil {
			cleanup()
			return nil, err
		}
	}
	return &Prepared{
		Text: text, MediaType: mediaType, MediaPath: path, cleanup: cleanup,
	}, nil
}
