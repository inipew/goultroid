package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/inipew/goultroid/internal/services/process"
	_ "golang.org/x/image/webp"
)

type ffprobeJSON struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		BitRate   string `json:"bit_rate"`
		Duration  string `json:"duration"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// FFProber implements Prober using ffprobe with native Go image decoding fallback.
type FFProber struct {
	runner process.Runner
}

// Ensure FFProber implements Prober.
var _ Prober = (*FFProber)(nil)

// NewFFProber creates a new media Prober.
func NewFFProber(runner process.Runner) *FFProber {
	if runner == nil {
		runner = process.NewOSRunner(2, 30*time.Second, 2*1024*1024)
	}
	return &FFProber{runner: runner}
}

// Probe inspects filePath and returns structured ProbeResult.
func (p *FFProber) Probe(ctx context.Context, filePath string) (*ProbeResult, error) {
	if _, err := os.Stat(filePath); err != nil {
		return nil, fmt.Errorf("%w: file not found: %v", ErrMediaProbingFailed, err)
	}

	ffprobePath, err := exec.LookPath("ffprobe")
	if err == nil {
		// Run ffprobe via process runner
		req := process.Request{
			Command: ffprobePath,
			Args: []string{
				"-v", "quiet",
				"-print_format", "json",
				"-show_format",
				"-show_streams",
				filePath,
			},
			Timeout: 15 * time.Second,
		}

		res, runErr := p.runner.Run(ctx, req)
		if runErr == nil && res.ExitCode == 0 && res.Stdout != "" {
			parsed, parseErr := parseFFProbeOutput(res.Stdout)
			if parseErr == nil && (parsed.HasAudio || parsed.HasVideo || parsed.Width > 0) {
				return parsed, nil
			}
		}
	}

	// Native Go fallback for images
	return probeNativeImage(filePath)
}

func parseFFProbeOutput(rawJSON string) (*ProbeResult, error) {
	var data ffprobeJSON
	if err := json.Unmarshal([]byte(rawJSON), &data); err != nil {
		return nil, err
	}

	res := &ProbeResult{
		Format: data.Format.FormatName,
	}

	if data.Format.Duration != "" {
		if dSec, err := strconv.ParseFloat(data.Format.Duration, 64); err == nil {
			res.Duration = time.Duration(dSec * float64(time.Second))
		}
	}
	if data.Format.Size != "" {
		if s, err := strconv.ParseInt(data.Format.Size, 10, 64); err == nil {
			res.Size = s
		}
	}
	if data.Format.BitRate != "" {
		if b, err := strconv.ParseInt(data.Format.BitRate, 10, 64); err == nil {
			res.Bitrate = b
		}
	}

	for _, s := range data.Streams {
		if s.CodecType == "video" {
			res.HasVideo = true
			if res.VideoCodec == "" {
				res.VideoCodec = s.CodecName
			}
			if s.Width > 0 && res.Width == 0 {
				res.Width = s.Width
			}
			if s.Height > 0 && res.Height == 0 {
				res.Height = s.Height
			}
		} else if s.CodecType == "audio" {
			res.HasAudio = true
			if res.AudioCodec == "" {
				res.AudioCodec = s.CodecName
			}
		}
	}

	return res, nil
}

func probeNativeImage(filePath string) (*ProbeResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to open image: %v", ErrMediaProbingFailed, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, errors.Join(ErrMediaProbingFailed, err)
	}

	return &ProbeResult{
		Format:   format,
		Width:    cfg.Width,
		Height:   cfg.Height,
		Size:     stat.Size(),
		HasVideo: false,
		HasAudio: false,
	}, nil
}
