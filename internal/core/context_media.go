package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// downloadSemaphore enforces global maximum concurrent downloads (default: 3 concurrent jobs).
var downloadSemaphore = make(chan struct{}, 3)

// MediaFacade provides a dedicated namespace for downloading and sending media assets.
type MediaFacade struct {
	ctx *Context
}

// DownloadMedia downloads the media attached to the current message or replied message.
func (m *MediaFacade) DownloadMedia(destDir string) (string, error) {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return "", errors.New("telegram service not initialized")
	}

	var media *MediaInfo
	if c.Message != nil && c.Message.Media != nil && c.Message.Media.Location != nil {
		media = c.Message.Media
	}

	if media == nil {
		if c.Message != nil && c.Message.ReplyToID != 0 {
			replied, err := c.GetReply()
			if err != nil {
				return "", fmt.Errorf("failed to fetch replied message: %w", err)
			}
			if replied != nil && replied.Media != nil && replied.Media.Location != nil {
				media = replied.Media
			}
		}
	}

	if media == nil || media.Location == nil {
		return "", errors.New("no media found in message or reply")
	}

	// Enforce global maximum download size (default: 500 MB)
	const MaxMediaDownloadSize = DefaultMaxDownloadSize
	if media.Size > MaxMediaDownloadSize {
		return "", fmt.Errorf("%w: file size (%d bytes) exceeds maximum allowed limit (500MB)", ErrMedia, media.Size)
	}

	// 1. Quota check & rotation on destination directory (FIFO cleanup if full or expired)
	_ = EnforceDirectoryQuota(destDir, DefaultDirectoryQuota, DefaultMaxFileAge)

	// 2. Pre-flight disk space check (reserves 50MB if metadata size is 0/unknown)
	requiredSpace := media.Size
	if requiredSpace <= 0 {
		requiredSpace = 50 * 1024 * 1024
	}
	if err := CheckDiskSpace(destDir, requiredSpace); err != nil {
		return "", err
	}

	// TaskEngine resource admission is authoritative when present. The local
	// semaphore remains a safety net for direct/legacy callers.
	releaseDownload, err := acquireDownloadSlot(c.Ctx)
	if err != nil {
		return "", err
	}
	defer releaseDownload()

	if err := os.MkdirAll(destDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	fileName := filepath.Base(filepath.Clean(media.FileName))
	if fileName == "." || fileName == ".." || fileName == "/" || fileName == "" {
		fileName = fmt.Sprintf("media_%d%s", time.Now().UnixNano(), mediaFileExtension(media))
	} else if filepath.Ext(fileName) == "" {
		fileName += mediaFileExtension(media)
	}

	fileName = SanitizeFileName(fileName)
	filePath := filepath.Join(destDir, fileName)

	if err := c.Svc.DownloadFile(c.Ctx, media.Location, filePath); err != nil {
		_ = os.Remove(filePath)
		return "", fmt.Errorf("download failed: %w", err)
	}

	// Verify actual downloaded file size against hard limit
	if stat, err := os.Stat(filePath); err == nil {
		if stat.Size() > MaxMediaDownloadSize {
			_ = os.Remove(filePath)
			return "", fmt.Errorf("%w: downloaded file size (%d bytes) exceeds maximum limit (500MB)", ErrMedia, stat.Size())
		}
	}

	return filePath, nil
}

func mediaFileExtension(media *MediaInfo) string {
	if media == nil {
		return ".bin"
	}
	switch media.Type {
	case "photo":
		return ".jpg"
	case "video":
		return ".mp4"
	case "audio":
		return ".mp3"
	case "voice":
		return ".ogg"
	case "sticker":
		switch strings.ToLower(strings.TrimSpace(media.MimeType)) {
		case "application/x-tgsticker", "application/x-tgs":
			return ".tgs"
		case "video/webm":
			return ".webm"
		case "image/png":
			return ".png"
		case "image/webp":
			return ".webp"
		default:
			return ".webp"
		}
	default:
		return ".bin"
	}
}

// SendMedia sends a media file with the specified mediaType ("file", "photo", "sticker", "audio", "video").
func (m *MediaFacade) SendMedia(mediaType string, filePath string, caption string) (*Message, error) {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return nil, errors.New("peer is nil")
	}
	if err := ValidateUploadSize(filePath, DefaultMaxUploadSize); err != nil {
		return nil, err
	}
	msg, err := c.Svc.SendMedia(c.Ctx, c.PeerID, mediaType, filePath, caption)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, nil
	}
	return &Message{
		ID:        msg.ID,
		Text:      msg.Message,
		Date:      time.Unix(int64(msg.Date), 0),
		MediaType: mediaType,
	}, nil
}

// SendFile uploads and sends a file/document to the chat.
func (m *MediaFacade) SendFile(filePath, caption string) error {
	_, err := m.SendMedia("file", filePath, caption)
	return err
}

// SendPhoto uploads and sends a photo to the chat.
func (m *MediaFacade) SendPhoto(filePath, caption string) error {
	_, err := m.SendMedia("photo", filePath, caption)
	return err
}

// SendSticker uploads and sends a sticker to the chat.
func (m *MediaFacade) SendSticker(filePath string) error {
	_, err := m.SendMedia("sticker", filePath, "")
	return err
}

// SendAudio uploads and sends an audio file to the chat.
func (m *MediaFacade) SendAudio(filePath, caption string) error {
	_, err := m.SendMedia("audio", filePath, caption)
	return err
}

func acquireDownloadSlot(ctx context.Context) (func(), error) {
	if tasks.HasHeldResource(ctx, "download") {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case downloadSemaphore <- struct{}{}:
		return func() { <-downloadSemaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
