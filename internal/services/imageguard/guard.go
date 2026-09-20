package imageguard

import (
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"

	_ "golang.org/x/image/webp"
)

var (
	// ErrInputTooLarge indicates that compressed input exceeds the configured byte budget.
	ErrInputTooLarge = errors.New("image input exceeds safety limit")
	// ErrDimensionsExceeded indicates that width or height exceeds the configured bound.
	ErrDimensionsExceeded = errors.New("image dimensions exceed safety limit")
	// ErrPixelBudgetExceeded indicates that total decoded pixels exceed the configured bound.
	ErrPixelBudgetExceeded = errors.New("image pixel count exceeds safety limit")
	// ErrDecodedBudgetExceeded indicates that the estimated RGBA footprint exceeds the configured bound.
	ErrDecodedBudgetExceeded = errors.New("estimated decoded image exceeds safety limit")
	// ErrUnsupportedFormat indicates that the detected image format is not allowed by policy.
	ErrUnsupportedFormat = errors.New("unsupported image format")
	// ErrInvalidImage indicates malformed, empty, or otherwise undecodable image input.
	ErrInvalidImage = errors.New("invalid image")
)

const (
	defaultMaxInputBytes   int64 = 32 << 20
	defaultMaxWidth              = 8192
	defaultMaxHeight             = 8192
	defaultMaxPixels       int64 = 40_000_000
	defaultMaxDecodedBytes int64 = 160 << 20
	decodedBytesPerPixel   int64 = 4
	maxInt64               int64 = 1<<63 - 1
)

// Policy defines compressed-input and decoded-image safety budgets.
type Policy struct {
	MaxInputBytes   int64
	MaxWidth        int
	MaxHeight       int
	MaxPixels       int64
	MaxDecodedBytes int64
	AllowedFormats  []string
}

// Info describes validated image metadata and its estimated decoded footprint.
type Info struct {
	Format                string
	Width                 int
	Height                int
	FileSize              int64
	Pixels                int64
	EstimatedDecodedBytes int64
}

// DefaultPolicy returns the conservative shared policy used for unspecified limits.
func DefaultPolicy() Policy {
	return Policy{
		MaxInputBytes:   defaultMaxInputBytes,
		MaxWidth:        defaultMaxWidth,
		MaxHeight:       defaultMaxHeight,
		MaxPixels:       defaultMaxPixels,
		MaxDecodedBytes: defaultMaxDecodedBytes,
		AllowedFormats:  []string{"jpeg", "png", "gif", "webp"},
	}
}

// ValidateKnown cheaply checks trustworthy metadata before a download or decode.
// Zero size/dimensions are treated as unknown and validated again by Inspect/Decode.
func ValidateKnown(fileSize int64, width, height int, policy Policy) error {
	policy = normalizePolicy(policy)
	if fileSize > 0 && fileSize > policy.MaxInputBytes {
		return fmt.Errorf("%w: %d bytes > %d bytes", ErrInputTooLarge, fileSize, policy.MaxInputBytes)
	}
	if width <= 0 || height <= 0 {
		return nil
	}
	_, _, err := validateGeometry(width, height, policy)
	return err
}

// Inspect validates image metadata without performing a full pixel decode.
func Inspect(path string, policy Policy) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	return inspectOpenFile(f, policy)
}

// Decode performs metadata preflight first, then fully decodes only validated input.
func Decode(path string, policy Policy) (image.Image, Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Info{}, err
	}
	defer f.Close()

	info, err := inspectOpenFile(f, policy)
	if err != nil {
		return nil, Info{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, Info{}, err
	}

	img, format, err := image.Decode(io.LimitReader(f, info.FileSize))
	if err != nil {
		return nil, Info{}, fmt.Errorf("%w: decode failed: %v", ErrInvalidImage, err)
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format != info.Format {
		return nil, Info{}, fmt.Errorf("%w: format changed from %q to %q during decode", ErrInvalidImage, info.Format, format)
	}

	bounds := img.Bounds()
	pixels, decodedBytes, err := validateGeometry(bounds.Dx(), bounds.Dy(), normalizePolicy(policy))
	if err != nil {
		return nil, Info{}, err
	}
	info.Width = bounds.Dx()
	info.Height = bounds.Dy()
	info.Pixels = pixels
	info.EstimatedDecodedBytes = decodedBytes
	return img, info, nil
}

func inspectOpenFile(f *os.File, policy Policy) (Info, error) {
	policy = normalizePolicy(policy)
	stat, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	if !stat.Mode().IsRegular() {
		return Info{}, fmt.Errorf("%w: input is not a regular file", ErrInvalidImage)
	}
	if stat.Size() <= 0 {
		return Info{}, fmt.Errorf("%w: empty image input", ErrInvalidImage)
	}
	if stat.Size() > policy.MaxInputBytes {
		return Info{}, fmt.Errorf("%w: %d bytes > %d bytes", ErrInputTooLarge, stat.Size(), policy.MaxInputBytes)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Info{}, err
	}

	cfg, format, err := image.DecodeConfig(io.LimitReader(f, stat.Size()))
	if err != nil {
		return Info{}, fmt.Errorf("%w: metadata decode failed: %v", ErrInvalidImage, err)
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if !formatAllowed(format, policy.AllowedFormats) {
		return Info{}, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
	pixels, decodedBytes, err := validateGeometry(cfg.Width, cfg.Height, policy)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Format:                format,
		Width:                 cfg.Width,
		Height:                cfg.Height,
		FileSize:              stat.Size(),
		Pixels:                pixels,
		EstimatedDecodedBytes: decodedBytes,
	}, nil
}

func validateGeometry(width, height int, policy Policy) (int64, int64, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("%w: non-positive dimensions %dx%d", ErrInvalidImage, width, height)
	}
	if width > policy.MaxWidth || height > policy.MaxHeight {
		return 0, 0, fmt.Errorf("%w: %dx%d exceeds %dx%d", ErrDimensionsExceeded, width, height, policy.MaxWidth, policy.MaxHeight)
	}
	w, h := int64(width), int64(height)
	if h != 0 && w > maxInt64/h {
		return 0, 0, fmt.Errorf("%w: dimension multiplication overflow", ErrPixelBudgetExceeded)
	}
	pixels := w * h
	if pixels > policy.MaxPixels {
		return 0, 0, fmt.Errorf("%w: %d pixels > %d pixels", ErrPixelBudgetExceeded, pixels, policy.MaxPixels)
	}
	if pixels > maxInt64/decodedBytesPerPixel {
		return 0, 0, fmt.Errorf("%w: decoded size multiplication overflow", ErrDecodedBudgetExceeded)
	}
	decodedBytes := pixels * decodedBytesPerPixel
	if decodedBytes > policy.MaxDecodedBytes {
		return 0, 0, fmt.Errorf("%w: about %d bytes > %d bytes", ErrDecodedBudgetExceeded, decodedBytes, policy.MaxDecodedBytes)
	}
	return pixels, decodedBytes, nil
}

func normalizePolicy(policy Policy) Policy {
	defaults := DefaultPolicy()
	if policy.MaxInputBytes <= 0 {
		policy.MaxInputBytes = defaults.MaxInputBytes
	}
	if policy.MaxWidth <= 0 {
		policy.MaxWidth = defaults.MaxWidth
	}
	if policy.MaxHeight <= 0 {
		policy.MaxHeight = defaults.MaxHeight
	}
	if policy.MaxPixels <= 0 {
		policy.MaxPixels = defaults.MaxPixels
	}
	if policy.MaxDecodedBytes <= 0 {
		policy.MaxDecodedBytes = defaults.MaxDecodedBytes
	}
	if len(policy.AllowedFormats) == 0 {
		policy.AllowedFormats = defaults.AllowedFormats
	}
	return policy
}

func formatAllowed(format string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), format) {
			return true
		}
	}
	return false
}
