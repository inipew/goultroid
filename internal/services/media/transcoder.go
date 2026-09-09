package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
)

// FFmpegTranscoder executes media conversions using the system FFmpeg binary.
type FFmpegTranscoder struct {
	runner      process.Runner
	store       storage.Storage
	guard       *ResourceGuard
	prober      Prober
	timeout     time.Duration
	mu          sync.Mutex
	activeTemps map[string]time.Time
}

// Ensure FFmpegTranscoder implements Transcoder.
var _ Transcoder = (*FFmpegTranscoder)(nil)

// NewFFmpegTranscoder creates a new FFmpeg transcoder service.
func NewFFmpegTranscoder(runner process.Runner, store storage.Storage, guard *ResourceGuard, prober Prober) *FFmpegTranscoder {
	if runner == nil {
		runner = process.NewOSRunner(2, 5*time.Minute, 4*1024*1024)
	}
	if guard == nil {
		guard = NewResourceGuard(2, 100*1024*1024)
	}
	if prober == nil {
		prober = NewFFProber(runner)
	}
	return &FFmpegTranscoder{
		runner:      runner,
		store:       store,
		guard:       guard,
		prober:      prober,
		timeout:     5 * time.Minute,
		activeTemps: make(map[string]time.Time),
	}
}

// ActiveTempDirectories returns a snapshot of transcoder-owned temporary
// directories currently awaiting cleanup.
func (t *FFmpegTranscoder) ActiveTempDirectories() map[string]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make(map[string]time.Time, len(t.activeTemps))
	for path, createdAt := range t.activeTemps {
		result[path] = createdAt
	}
	return result
}

// IsAvailable checks if the ffmpeg binary is present on the host system.
func (t *FFmpegTranscoder) IsAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// Run performs the specified media transformation.
func (t *FFmpegTranscoder) Run(ctx context.Context, input *storage.Asset, op Operation, opts TranscodeOptions) (*storage.Asset, error) {
	if err := t.guard.ValidateInput(input); err != nil {
		return nil, err
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("%w: ffmpeg is required for this operation", ErrFFmpegUnavailable)
	}

	// Throttle concurrent execution
	release, err := t.guard.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	tmpDir, err := os.MkdirTemp("", "goultroid-transcode-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary working directory: %w", err)
	}
	t.mu.Lock()
	t.activeTemps[tmpDir] = time.Now().UTC()
	t.mu.Unlock()
	defer func() {
		_ = os.RemoveAll(tmpDir)
		t.mu.Lock()
		delete(t.activeTemps, tmpDir)
		t.mu.Unlock()
	}()

	// Pre-flight disk space verification (requires 2x input size)
	requiredSpace := input.Size * 2
	if requiredSpace <= 0 {
		requiredSpace = 50 * 1024 * 1024
	}
	if err := t.guard.CheckDisk(tmpDir, requiredSpace); err != nil {
		return nil, fmt.Errorf("%w: %v", core.ErrResourceLimit, err)
	}

	outExt, args, err := buildFFmpegArgs(input.Path, tmpDir, op, opts)
	if err != nil {
		return nil, err
	}

	outPath := filepath.Join(tmpDir, fmt.Sprintf("output.%s", outExt))
	args = append(args, outPath)

	req := process.Request{
		Command:    ffmpegPath,
		Args:       args,
		Timeout:    t.timeout,
		WorkingDir: tmpDir,
	}

	res, runErr := t.runner.Run(ctx, req)
	if runErr != nil {
		outStr := res.Stderr
		if len(outStr) > 500 {
			outStr = outStr[len(outStr)-500:]
		}
		return nil, fmt.Errorf("%w: exit code %d: %s (%v)", ErrTranscodingFailed, res.ExitCode, outStr, runErr)
	}

	outFile, err := os.Open(outPath)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot open output file: %v", ErrTranscodingFailed, err)
	}
	defer outFile.Close()

	outStat, err := outFile.Stat()
	if err != nil {
		return nil, err
	}

	if outStat.Size() > t.guard.MaxOutputSize {
		return nil, fmt.Errorf("%w: transformed media size %d exceeded limit %d", core.ErrResourceLimit, outStat.Size(), t.guard.MaxOutputSize)
	}

	// Probe output file for accurate metadata
	probe, _ := t.prober.Probe(ctx, outPath)

	baseInputName := strings.TrimSuffix(input.Name, filepath.Ext(input.Name))
	newAssetName := fmt.Sprintf("%s.%s", baseInputName, outExt)

	var duration time.Duration
	var width, height int
	if probe != nil {
		duration = probe.Duration
		width = probe.Width
		height = probe.Height
	}

	return t.store.Put(ctx, outFile, storage.Metadata{
		Name:     newAssetName,
		Duration: duration,
		Width:    width,
		Height:   height,
	})
}

func sanitizeFormat(format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(format, ".")))
	if format == "" {
		return "", nil
	}
	if len(format) > 10 {
		return "", fmt.Errorf("format extension too long: %q", format)
	}
	for _, r := range format {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "", fmt.Errorf("invalid format character in %q", format)
		}
	}
	return format, nil
}

func buildFFmpegArgs(inputPath string, tmpDir string, op Operation, opts TranscodeOptions) (string, []string, error) {
	args := []string{"-y", "-i", inputPath}
	var ext string

	targetFmt, err := sanitizeFormat(opts.TargetFormat)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", core.ErrInvalidArgs, err)
	}

	switch op {
	case OpExtractAudio:
		ext = targetFmt
		if ext == "" {
			ext = "mp3"
		}
		args = append(args, "-vn")
		switch ext {
		case "mp3":
			args = append(args, "-acodec", "libmp3lame", "-q:a", "2")
		case "aac", "m4a":
			args = append(args, "-acodec", "aac", "-b:a", "192k")
		case "ogg", "opus":
			args = append(args, "-acodec", "libopus", "-b:a", "64k")
		case "flac":
			args = append(args, "-acodec", "flac")
		case "wav":
			args = append(args, "-acodec", "pcm_s16le")
		default:
			return "", nil, fmt.Errorf("%w: unsupported audio format %q", ErrUnsupportedOperation, ext)
		}

	case OpConvertVideo:
		ext = targetFmt
		if ext == "" {
			ext = "mp4"
		}
		switch ext {
		case "mp4", "mkv", "mov":
			args = append(args, "-vcodec", "libx264", "-preset", "fast", "-crf", "23", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart")
		case "webm":
			args = append(args, "-c:v", "libvpx-vp9", "-crf", "30", "-b:v", "0", "-c:a", "libopus")
		case "avi":
			args = append(args, "-vcodec", "mpeg4", "-q:v", "3", "-c:a", "libmp3lame", "-b:a", "128k")
		default:
			return "", nil, fmt.Errorf("%w: unsupported video format %q", ErrUnsupportedOperation, ext)
		}
		if opts.Width > 0 && opts.Height > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", opts.Width, opts.Height))
		}

	case OpCompress:
		ext = "mp4"
		args = append(args, "-vcodec", "libx264", "-preset", "faster", "-crf", "28", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart")

	case OpConvertToGIF:
		ext = "gif"
		filter := "fps=15,scale=480:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse"
		if opts.FPS > 0 {
			filter = fmt.Sprintf("fps=%d,scale=480:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse", opts.FPS)
		}
		args = append(args, "-vf", filter)

	case OpConvertToSticker:
		// Telegram video stickers require WebM VP9, max 512x512, <= 3s, <= 30 fps, no audio
		ext = "webm"
		args = append(args,
			"-vf", "scale=512:512:force_original_aspect_ratio=decrease,fps=30",
			"-c:v", "libvpx-vp9",
			"-crf", "30",
			"-b:v", "256k",
			"-an",
			"-t", "3",
		)

	case OpGenerateThumbnail:
		ext = "jpg"
		offset := opts.StartTime
		if offset <= 0 {
			offset = time.Second
		}
		args = append(args, "-ss", fmt.Sprintf("%.2f", offset.Seconds()), "-vframes", "1")

	default:
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedOperation, op)
	}

	if len(opts.ExtraArgs) > 0 {
		args = append(args, opts.ExtraArgs...)
	}

	return ext, args, nil
}
