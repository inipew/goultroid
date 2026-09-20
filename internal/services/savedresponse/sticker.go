package savedresponse

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/inipew/goultroid/internal/services/imageguard"
)

const (
	StickerFormatStatic   = "static"
	StickerFormatAnimated = "animated"
	StickerFormatVideo    = "video"

	staticStickerMaxBytes   int64 = 512 << 10
	animatedStickerMaxBytes int64 = 64 << 10
	videoStickerMaxBytes    int64 = 256 << 10
	maxTGSDecodedBytes      int64 = 2 << 20
)

var (
	ErrInvalidSticker           = errors.New("saved response: invalid sticker")
	ErrStickerTooLarge          = errors.New("saved response: sticker exceeds format limit")
	ErrUnsupportedStickerFormat = errors.New("saved response: unsupported sticker format")
)

func stickerFormat(media *MediaRef) string {
	if media == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(media.MIMEType)) {
	case "image/webp", "image/png":
		return StickerFormatStatic
	case "application/x-tgsticker", "application/x-tgs":
		return StickerFormatAnimated
	case "video/webm":
		return StickerFormatVideo
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(media.Name))) {
	case ".webp", ".png":
		return StickerFormatStatic
	case ".tgs":
		return StickerFormatAnimated
	case ".webm":
		return StickerFormatVideo
	default:
		return ""
	}
}

func validateStickerFile(path string, media *MediaRef) error {
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSticker, err)
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 {
		return fmt.Errorf("%w: sticker file is empty or non-regular", ErrInvalidSticker)
	}

	switch stickerFormat(media) {
	case StickerFormatStatic:
		return validateStaticSticker(path, stat.Size())
	case StickerFormatAnimated:
		return validateAnimatedSticker(path, stat.Size())
	case StickerFormatVideo:
		return validateVideoSticker(path, stat.Size())
	default:
		return fmt.Errorf("%w: MIME=%q name=%q", ErrUnsupportedStickerFormat, media.MIMEType, media.Name)
	}
}

func validateStaticSticker(path string, size int64) error {
	if size > staticStickerMaxBytes {
		return fmt.Errorf("%w: static sticker is %d bytes; max %d", ErrStickerTooLarge, size, staticStickerMaxBytes)
	}
	info, err := imageguard.Inspect(path, imageguard.Policy{
		MaxInputBytes:   staticStickerMaxBytes,
		MaxWidth:        512,
		MaxHeight:       512,
		MaxPixels:       512 * 512,
		MaxDecodedBytes: 4 * 512 * 512,
		AllowedFormats:  []string{"png", "webp"},
	})
	if err != nil {
		if errors.Is(err, imageguard.ErrInputTooLarge) {
			return fmt.Errorf("%w: %v", ErrStickerTooLarge, err)
		}
		return fmt.Errorf("%w: %v", ErrInvalidSticker, err)
	}
	if info.Width != 512 && info.Height != 512 {
		return fmt.Errorf("%w: static sticker must have one 512px side, got %dx%d", ErrInvalidSticker, info.Width, info.Height)
	}
	return nil
}

func validateAnimatedSticker(path string, size int64) error {
	if size > animatedStickerMaxBytes {
		return fmt.Errorf("%w: animated sticker is %d bytes; max %d", ErrStickerTooLarge, size, animatedStickerMaxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSticker, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: invalid TGS gzip stream: %v", ErrInvalidSticker, err)
	}
	defer zr.Close()

	body, err := io.ReadAll(io.LimitReader(zr, maxTGSDecodedBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read TGS payload: %v", ErrInvalidSticker, err)
	}
	if int64(len(body)) > maxTGSDecodedBytes {
		return fmt.Errorf("%w: decompressed TGS exceeds %d bytes", ErrInvalidSticker, maxTGSDecodedBytes)
	}
	var meta struct {
		Width  int `json:"w"`
		Height int `json:"h"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return fmt.Errorf("%w: invalid TGS JSON: %v", ErrInvalidSticker, err)
	}
	if meta.Width != 512 || meta.Height != 512 {
		return fmt.Errorf("%w: animated sticker canvas must be 512x512, got %dx%d", ErrInvalidSticker, meta.Width, meta.Height)
	}
	return nil
}

func validateVideoSticker(path string, size int64) error {
	if size > videoStickerMaxBytes {
		return fmt.Errorf("%w: video sticker is %d bytes; max %d", ErrStickerTooLarge, size, videoStickerMaxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSticker, err)
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return fmt.Errorf("%w: short WebM sticker: %v", ErrInvalidSticker, err)
	}
	want := [4]byte{0x1a, 0x45, 0xdf, 0xa3}
	if magic != want {
		return fmt.Errorf("%w: video sticker is not a WebM/EBML stream", ErrInvalidSticker)
	}
	return nil
}
