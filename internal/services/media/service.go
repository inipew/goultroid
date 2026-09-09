package media

import (
	"context"
	"sort"
	"time"

	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
)

// DiagnosticsSnapshot describes media resources that are expected to be
// transient and therefore must be empty after an operation or shutdown.
type DiagnosticsSnapshot struct {
	ActiveTempDirectories []string
}

// Service provides a high-level API coordinating media inspection, transformation, and storage.
type Service struct {
	store      storage.Storage
	prober     Prober
	transcoder Transcoder
	guard      *ResourceGuard
}

// NewService creates a new media service platform.
func NewService(runner process.Runner, store storage.Storage, guard *ResourceGuard) *Service {
	if runner == nil {
		runner = process.NewOSRunner(2, 5*time.Minute, 4*1024*1024)
	}
	if guard == nil {
		guard = NewResourceGuard(2, 100*1024*1024)
	}
	prober := NewFFProber(runner)
	transcoder := NewFFmpegTranscoder(runner, store, guard, prober)

	return &Service{
		store:      store,
		prober:     prober,
		transcoder: transcoder,
		guard:      guard,
	}
}

// Storage returns the underlying storage manager.
func (s *Service) Storage() storage.Storage {
	return s.store
}

// Prober returns the underlying prober.
func (s *Service) Prober() Prober {
	return s.prober
}

// Transcoder returns the underlying transcoder.
func (s *Service) Transcoder() Transcoder {
	return s.transcoder
}

// Guard returns the resource guard.
func (s *Service) Guard() *ResourceGuard {
	return s.guard
}

// Diagnostics returns a stable snapshot of active transcoder temporary
// directories without exposing the concrete transcoder implementation.
func (s *Service) Diagnostics() DiagnosticsSnapshot {
	snapshot := DiagnosticsSnapshot{}
	if transcoder, ok := s.transcoder.(*FFmpegTranscoder); ok {
		for path := range transcoder.ActiveTempDirectories() {
			snapshot.ActiveTempDirectories = append(snapshot.ActiveTempDirectories, path)
		}
		sort.Strings(snapshot.ActiveTempDirectories)
	}
	return snapshot
}

// Probe inspects an asset and returns its technical metadata.
func (s *Service) Probe(ctx context.Context, asset *storage.Asset) (*ProbeResult, error) {
	if err := s.guard.ValidateInput(asset); err != nil {
		return nil, err
	}
	return s.prober.Probe(ctx, asset.Path)
}

// ExtractAudio extracts an audio track from a media asset into format (e.g. mp3, aac, ogg).
func (s *Service) ExtractAudio(ctx context.Context, asset *storage.Asset, format string) (*storage.Asset, error) {
	return s.transcoder.Run(ctx, asset, OpExtractAudio, TranscodeOptions{
		TargetFormat: format,
	})
}

// ConvertVideo converts or scales a video file.
func (s *Service) ConvertVideo(ctx context.Context, asset *storage.Asset, opts TranscodeOptions) (*storage.Asset, error) {
	return s.transcoder.Run(ctx, asset, OpConvertVideo, opts)
}

// ConvertToGIF converts a video or animation into an animated GIF.
func (s *Service) ConvertToGIF(ctx context.Context, asset *storage.Asset, opts TranscodeOptions) (*storage.Asset, error) {
	return s.transcoder.Run(ctx, asset, OpConvertToGIF, opts)
}

// ConvertToSticker converts media to a Telegram video sticker (WebM VP9, 512x512).
func (s *Service) ConvertToSticker(ctx context.Context, asset *storage.Asset) (*storage.Asset, error) {
	return s.transcoder.Run(ctx, asset, OpConvertToSticker, TranscodeOptions{})
}

// GenerateThumbnail generates a frame thumbnail at the specified offset.
func (s *Service) GenerateThumbnail(ctx context.Context, asset *storage.Asset, at time.Duration) (*storage.Asset, error) {
	return s.transcoder.Run(ctx, asset, OpGenerateThumbnail, TranscodeOptions{
		StartTime: at,
	})
}
