package media

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestResourceGuard_Validation(t *testing.T) {
	guard := NewResourceGuard(2, 50*1024*1024)

	// Nil asset
	if err := guard.ValidateInput(nil); !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs on nil asset, got %v", err)
	}

	// Over-sized asset
	largeAsset := &storage.Asset{
		Size: guard.MaxInputSize + 1,
	}
	if err := guard.ValidateInput(largeAsset); !errors.Is(err, core.ErrResourceLimit) {
		t.Errorf("expected ErrResourceLimit on large asset, got %v", err)
	}

	// Over-long duration asset
	longAsset := &storage.Asset{
		Duration: guard.MaxDuration + time.Minute,
	}
	if err := guard.ValidateInput(longAsset); !errors.Is(err, core.ErrResourceLimit) {
		t.Errorf("expected ErrResourceLimit on long duration, got %v", err)
	}

	// Over-resolution asset
	hugeResAsset := &storage.Asset{
		Width:  guard.MaxWidth + 100,
		Height: 100,
	}
	if err := guard.ValidateInput(hugeResAsset); !errors.Is(err, core.ErrResourceLimit) {
		t.Errorf("expected ErrResourceLimit on high width, got %v", err)
	}

	// Valid asset
	validAsset := &storage.Asset{
		Size:     1024 * 1024,
		Duration: 30 * time.Second,
		Width:    1920,
		Height:   1080,
	}
	if err := guard.ValidateInput(validAsset); err != nil {
		t.Errorf("expected valid asset to pass, got %v", err)
	}
}

func TestResourceGuard_Concurrency(t *testing.T) {
	guard := NewResourceGuard(2, 50*1024*1024)
	ctx := context.Background()

	rel1, err := guard.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 1 failed: %v", err)
	}

	rel2, err := guard.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 2 failed: %v", err)
	}

	// 3rd acquire with canceled context should fail with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()

	_, err = guard.Acquire(timeoutCtx)
	if !errors.Is(err, core.ErrTimeout) {
		t.Errorf("expected ErrTimeout when semaphore full, got %v", err)
	}

	rel1()
	rel2()

	// Should be able to acquire again after release
	rel3, err := guard.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 3 failed: %v", err)
	}
	rel3()
}

func TestBuildFFmpegArgs(t *testing.T) {
	tests := []struct {
		name     string
		op       Operation
		opts     TranscodeOptions
		wantExt  string
		mustHave []string
	}{
		{
			name:     "extract audio mp3",
			op:       OpExtractAudio,
			opts:     TranscodeOptions{TargetFormat: "mp3"},
			wantExt:  "mp3",
			mustHave: []string{"-vn", "-acodec", "libmp3lame"},
		},
		{
			name:     "extract audio aac",
			op:       OpExtractAudio,
			opts:     TranscodeOptions{TargetFormat: "aac"},
			wantExt:  "aac",
			mustHave: []string{"-vn", "-acodec", "aac"},
		},
		{
			name:     "convert video",
			op:       OpConvertVideo,
			opts:     TranscodeOptions{TargetFormat: "mp4", Width: 1280, Height: 720},
			wantExt:  "mp4",
			mustHave: []string{"-vcodec", "libx264", "scale=1280:720"},
		},
		{
			name:     "compress video",
			op:       OpCompress,
			opts:     TranscodeOptions{},
			wantExt:  "mp4",
			mustHave: []string{"-crf", "28"},
		},
		{
			name:     "convert to gif",
			op:       OpConvertToGIF,
			opts:     TranscodeOptions{FPS: 20},
			wantExt:  "gif",
			mustHave: []string{"fps=20"},
		},
		{
			name:     "convert to sticker",
			op:       OpConvertToSticker,
			opts:     TranscodeOptions{},
			wantExt:  "webm",
			mustHave: []string{"libvpx-vp9", "-an", "scale=512:512"},
		},
		{
			name:     "thumbnail",
			op:       OpGenerateThumbnail,
			opts:     TranscodeOptions{StartTime: 5 * time.Second},
			wantExt:  "jpg",
			mustHave: []string{"-ss", "5.00", "-vframes", "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, args, err := buildFFmpegArgs("/input/media.mp4", "/tmp", tt.op, tt.opts)
			if err != nil {
				t.Fatalf("buildFFmpegArgs failed: %v", err)
			}
			if ext != tt.wantExt {
				t.Errorf("expected ext %s, got %s", tt.wantExt, ext)
			}
			fullArgs := strings.Join(args, " ")
			for _, must := range tt.mustHave {
				if !strings.Contains(fullArgs, must) {
					t.Errorf("expected args to contain %q, got: %s", must, fullArgs)
				}
			}
		})
	}

	// Unknown op
	_, _, err := buildFFmpegArgs("/input/media.mp4", "/tmp", "unknown_op", TranscodeOptions{})
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Errorf("expected ErrUnsupportedOperation, got %v", err)
	}
}

func TestParseFFProbeOutput(t *testing.T) {
	rawJSON := `{
		"streams": [
			{
				"codec_type": "video",
				"codec_name": "h264",
				"width": 1920,
				"height": 1080
			},
			{
				"codec_type": "audio",
				"codec_name": "aac"
			}
		],
		"format": {
			"format_name": "mov,mp4,m4a,3gp,3g2,mj2",
			"duration": "42.500000",
			"size": "1048576",
			"bit_rate": "197378"
		}
	}`

	parsed, err := parseFFProbeOutput(rawJSON)
	if err != nil {
		t.Fatalf("parseFFProbeOutput failed: %v", err)
	}
	if !parsed.HasVideo || !parsed.HasAudio {
		t.Errorf("expected video and audio true")
	}
	if parsed.Width != 1920 || parsed.Height != 1080 {
		t.Errorf("dimensions mismatch: %dx%d", parsed.Width, parsed.Height)
	}
	if parsed.VideoCodec != "h264" || parsed.AudioCodec != "aac" {
		t.Errorf("codecs mismatch: %s, %s", parsed.VideoCodec, parsed.AudioCodec)
	}
	if parsed.Duration != 42500*time.Millisecond {
		t.Errorf("duration mismatch: %v", parsed.Duration)
	}
}

func TestProbeNativeImage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "goultroid-img-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	imgPath := filepath.Join(tmpDir, "test.png")
	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatalf("encode failed: %v", err)
	}
	_ = f.Close()

	probe, err := probeNativeImage(imgPath)
	if err != nil {
		t.Fatalf("probeNativeImage failed: %v", err)
	}
	if probe.Width != 100 || probe.Height != 200 {
		t.Errorf("expected 100x200, got %dx%d", probe.Width, probe.Height)
	}
	if probe.Format != "png" {
		t.Errorf("expected png format, got %s", probe.Format)
	}
}

type mockTranscoder struct {
	resAsset *storage.Asset
	err      error
}

func (m *mockTranscoder) Run(ctx context.Context, input *storage.Asset, op Operation, opts TranscodeOptions) (*storage.Asset, error) {
	return m.resAsset, m.err
}

func TestService_Delegation(t *testing.T) {
	store := storage.NewMemoryStorage()
	guard := NewResourceGuard(2, 50*1024*1024)
	mockAsset := &storage.Asset{ID: "transcoded1", Name: "audio.mp3", Size: 500}

	svc := NewService(nil, store, guard)
	svc.transcoder = &mockTranscoder{resAsset: mockAsset}

	ctx := context.Background()
	inAsset := &storage.Asset{ID: "in1", Name: "video.mp4", Size: 1000}

	out, err := svc.ExtractAudio(ctx, inAsset, "mp3")
	if err != nil {
		t.Fatalf("ExtractAudio failed: %v", err)
	}
	if out.ID != "transcoded1" {
		t.Errorf("expected ID transcoded1, got %s", out.ID)
	}
}
