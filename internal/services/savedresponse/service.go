package savedresponse

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
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/storage"
)

const (
	MaxPersistentMediaBytes int64 = 32 << 20
	MaxCaptionRunes               = 1024
)

var (
	ErrNilContext                  = errors.New("saved response: context is nil")
	ErrReplyNotFound               = errors.New("saved response: replied message not found")
	ErrEmptyResponse               = errors.New("saved response: response is empty")
	ErrMediaPersistenceUnavailable = errors.New("saved response: media persistence is not configured")
	ErrMediaDeliveryUnavailable    = errors.New("saved response: media delivery is not configured")
	ErrMediaTooLarge               = errors.New("saved response: media exceeds persistent limit")
	ErrNilPersist                  = errors.New("saved response: persist callback is nil")
	ErrNilDelete                   = errors.New("saved response: delete callback is nil")
)

type Service struct {
	store storage.Storage
	files *filesystem.Scope
}

func NewService(store storage.Storage) *Service {
	return &Service{store: store}
}

func (s *Service) SetFiles(files *filesystem.Scope) {
	if s != nil {
		s.files = files
	}
}

func (s *Service) CaptureReply(ctx *core.Context) (Response, error) {
	if ctx == nil {
		return Response{}, ErrNilContext
	}
	reply, err := ctx.GetReply()
	if err != nil {
		return Response{}, err
	}
	if reply == nil {
		return Response{}, ErrReplyNotFound
	}
	response := NewPlainText(reply.Text)
	if reply.Media != nil && reply.Media.Location != nil {
		media, err := s.captureMedia(ctx, reply.Media)
		if err != nil {
			return Response{}, err
		}
		response.Media = media
	}
	if response.Empty() {
		return Response{}, ErrEmptyResponse
	}
	if err := Validate(response); err != nil {
		_ = s.DeleteMedia(ctx.Ctx, response)
		return Response{}, err
	}
	return response, nil
}

func (s *Service) captureMedia(ctx *core.Context, media *core.MediaInfo) (*MediaRef, error) {
	if s == nil || s.store == nil || s.files == nil {
		return nil, ErrMediaPersistenceUnavailable
	}
	if media.Size > MaxPersistentMediaBytes {
		return nil, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, MaxPersistentMediaBytes)
	}
	workspace, err := s.files.CreateTempDir("saved-response-capture-*")
	if err != nil {
		return nil, err
	}
	defer s.files.RemoveTempDir(workspace)

	path, err := ctx.DownloadMedia(workspace)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if stat.Size() > MaxPersistentMediaBytes {
		return nil, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, MaxPersistentMediaBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	asset, err := s.store.Put(ctx.Ctx, f, storage.Metadata{
		Name: media.FileName,
		MIME: media.MimeType,
		Width: media.Width,
		Height: media.Height,
		Duration: time.Duration(media.Duration) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &MediaRef{
		AssetID:   asset.ID,
		MediaType: deliveryMediaType(media.Type),
		Name:      asset.Name,
		MIMEType:  asset.MIME,
	}, nil
}

func (s *Service) CommitReplacement(ctx context.Context, previous, next Response, persist func() error) error {
	if persist == nil {
		return ErrNilPersist
	}
	if err := persist(); err != nil {
		// Only the next response owns a disposable asset when it differs from the
		// previously committed one. Never delete the still-valid previous asset.
		if next.MediaAssetID() != "" && next.MediaAssetID() != previous.MediaAssetID() {
			_ = s.DeleteMedia(context.Background(), next)
		}
		return err
	}
	if previous.MediaAssetID() != "" && previous.MediaAssetID() != next.MediaAssetID() {
		_ = s.DeleteMedia(ctx, previous)
	}
	return nil
}

func (s *Service) CommitDelete(ctx context.Context, response Response, remove func() error) error {
	if remove == nil {
		return ErrNilDelete
	}
	if err := remove(); err != nil {
		return err
	}
	return s.DeleteMedia(ctx, response)
}

func (s *Service) DeleteMedia(parent context.Context, response Response) error {
	id := response.MediaAssetID()
	if id == "" || s == nil || s.store == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	err := s.store.Delete(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

type Prepared struct {
	Text      string
	Caption   string
	MediaType string
	MediaPath string
	cleanup   func()
}

func (p *Prepared) Cleanup() {
	if p == nil || p.cleanup == nil {
		return
	}
	p.cleanup()
	p.cleanup = nil
}

func (s *Service) Prepare(ctx context.Context, response Response, vars TemplateVars) (*Prepared, error) {
	if response.Empty() {
		return nil, ErrEmptyResponse
	}
	if err := Validate(response); err != nil {
		return nil, err
	}

	mediaID := response.MediaAssetID()
	if mediaID == "" {
		text, err := Render(response, vars, DefaultMaxOutputRunes)
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
		caption, err := Render(response, vars, MaxCaptionRunes)
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

	text, err := Render(response, vars, DefaultMaxOutputRunes)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := s.Materialize(ctx, response)
	if err != nil {
		return nil, err
	}
	return &Prepared{
		Text: text, MediaType: mediaType, MediaPath: path, cleanup: cleanup,
	}, nil
}

func (s *Service) Materialize(ctx context.Context, response Response) (string, func(), error) {
	id := response.MediaAssetID()
	if id == "" {
		return "", func() {}, nil
	}
	if s == nil || s.store == nil || s.files == nil {
		return "", nil, ErrMediaDeliveryUnavailable
	}
	asset, err := s.store.Stat(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if asset.Size > MaxPersistentMediaBytes {
		return "", nil, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, MaxPersistentMediaBytes)
	}

	reader, err := s.store.Open(ctx, id)
	if err != nil {
		return "", nil, err
	}
	defer reader.Close()

	ext := safeTempExtension(response.Media.Name)
	pattern := "saved-response-*"
	if ext != "" {
		pattern += ext
	}
	file, err := s.files.CreateTempFile(pattern)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = s.files.RemoveTempFile(file.Name()) }
	if _, err := copyBounded(file, reader, MaxPersistentMediaBytes); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}

func copyBounded(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	if maxBytes < 0 {
		return 0, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, maxBytes)
	}
	limited := &io.LimitedReader{R: src, N: maxBytes + 1}
	written, err := io.Copy(dst, limited)
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, maxBytes)
	}
	return written, nil
}

func safeTempExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == "" || len(ext) > 16 {
		return ""
	}
	for i, r := range ext {
		if i == 0 {
			if r != '.' {
				return ""
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

func deliveryMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "photo":
		return "photo"
	case "sticker":
		return "sticker"
	case "audio", "voice":
		return "audio"
	case "video":
		return "video"
	default:
		return "file"
	}
}
