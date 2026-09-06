package media

import (
	"context"
	"errors"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

var (
	// ErrUnsupportedOperation indicates an unknown or unimplemented media transformation.
	ErrUnsupportedOperation = errors.New("media: unsupported operation")
	// ErrMediaProbingFailed indicates that ffprobe or native inspection could not parse the media file.
	ErrMediaProbingFailed = errors.New("media: failed to probe media metadata")
	// ErrTranscodingFailed indicates that FFmpeg failed during processing.
	ErrTranscodingFailed = errors.New("media: transcoding failed")
	// ErrFFmpegUnavailable indicates that ffmpeg/ffprobe binary was not found in PATH.
	ErrFFmpegUnavailable = errors.New("media: ffmpeg or ffprobe is not installed on the system")
)

// Operation specifies the target transformation to perform on an asset.
type Operation string

const (
	OpExtractAudio      Operation = "extract_audio"
	OpConvertVideo      Operation = "convert_video"
	OpCompress          Operation = "compress"
	OpResize            Operation = "resize"
	OpGenerateThumbnail Operation = "thumbnail"
	OpConvertToGIF      Operation = "gif"
	OpConvertToSticker  Operation = "sticker"
)

// TranscodeOptions parameters for media transcoding operations.
type TranscodeOptions struct {
	TargetFormat string
	AudioCodec   string
	VideoCodec   string
	Bitrate      string
	Quality      int
	Width        int
	Height       int
	StartTime    time.Duration
	Duration     time.Duration
	FPS          int
	ExtraArgs    []string
}

// ProbeResult holds extracted media metadata.
type ProbeResult struct {
	Format     string        `json:"format"`
	Duration   time.Duration `json:"duration"`
	Size       int64         `json:"size"`
	Bitrate    int64         `json:"bitrate"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	VideoCodec string        `json:"video_codec,omitempty"`
	AudioCodec string        `json:"audio_codec,omitempty"`
	HasVideo   bool          `json:"has_video"`
	HasAudio   bool          `json:"has_audio"`
}

// Prober inspects media files to extract audio/video streams, codecs, and dimensions.
type Prober interface {
	Probe(ctx context.Context, filePath string) (*ProbeResult, error)
}

// Transcoder defines the contract for transforming media assets.
type Transcoder interface {
	Run(ctx context.Context, input *storage.Asset, op Operation, opts TranscodeOptions) (*storage.Asset, error)
}
