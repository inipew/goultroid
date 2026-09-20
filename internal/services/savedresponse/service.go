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

const MaxPersistentMediaBytes int64 = 32 << 20

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
		return Response{}, errors.New("saved response: context is nil")
	}
	reply, err := ctx.GetReply()
	if err != nil {
		return Response{}, err
	}
	if reply == nil {
		return Response{}, errors.New("saved response: replied message not found")
	}
	response := NewText(reply.Text)
	if reply.Media != nil && reply.Media.Location != nil {
		media, err := s.captureMedia(ctx, reply.Media)
		if err != nil {
			return Response{}, err
		}
		response.Media = media
	}
	if response.Empty() {
		return Response{}, errors.New("saved response: replied message has no text or media")
	}
	return response, nil
}

func (s *Service) captureMedia(ctx *core.Context, media *core.MediaInfo) (*MediaRef, error) {
	if s == nil || s.store == nil || s.files == nil {
		return nil, errors.New("saved response: media persistence is not configured")
	}
	if media.Size > MaxPersistentMediaBytes {
		return nil, fmt.Errorf("saved response media exceeds %d bytes", MaxPersistentMediaBytes)
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
		return nil, fmt.Errorf("saved response media exceeds %d bytes", MaxPersistentMediaBytes)
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
		AssetID: asset.ID,
		MediaType: deliveryMediaType(media.Type),
		Name: asset.Name,
		MIMEType: asset.MIME,
	}, nil
}

func (s *Service) CommitReplacement(ctx context.Context, previous, next Response, persist func() error) error {
	if persist == nil {
		return errors.New("saved response: persist callback is nil")
	}
	if err := persist(); err != nil {
		_ = s.DeleteMedia(context.Background(), next)
		return err
	}
	if previous.MediaAssetID() != "" && previous.MediaAssetID() != next.MediaAssetID() {
		_ = s.DeleteMedia(ctx, previous)
	}
	return nil
}

func (s *Service) CommitDelete(ctx context.Context, response Response, remove func() error) error {
	if remove == nil {
		return errors.New("saved response: delete callback is nil")
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

func (s *Service) Materialize(ctx context.Context, response Response) (string, func(), error) {
	id := response.MediaAssetID()
	if id == "" {
		return "", func() {}, nil
	}
	if s == nil || s.store == nil || s.files == nil {
		return "", nil, errors.New("saved response: media delivery is not configured")
	}
	reader, err := s.store.Open(ctx, id)
	if err != nil {
		return "", nil, err
	}
	defer reader.Close()

	ext := filepath.Ext(response.Media.Name)
	pattern := "saved-response-*"
	if ext != "" && len(ext) <= 16 {
		pattern += ext
	}
	file, err := s.files.CreateTempFile(pattern)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = s.files.RemoveTempFile(file.Name()) }
	if _, err := io.Copy(file, reader); err != nil {
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
