package download

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/tasks"
)

const extractorProbeMaxOutput int64 = 2 * 1024 * 1024

var extractorQualityHeights = [...]int{360, 480, 720, 1080, 1440, 2160}

type extractorProbePayload struct {
	Title    string                 `json:"title"`
	Artist   string                 `json:"artist"`
	Uploader string                 `json:"uploader"`
	Duration float64                `json:"duration"`
	Formats  []extractorProbeFormat `json:"formats"`
}

type extractorProbeFormat struct {
	Ext            string  `json:"ext"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	Height         int     `json:"height"`
	TBR            float64 `json:"tbr"`
	FileSize       int64   `json:"filesize"`
	FileSizeApprox int64   `json:"filesize_approx"`
}

func (f extractorProbeFormat) size() int64 {
	if f.FileSize > 0 {
		return f.FileSize
	}
	if f.FileSizeApprox > 0 {
		return f.FileSizeApprox
	}
	return 0
}

func normalizeExtractorProbe(payload extractorProbePayload) ProbeResult {
	result := ProbeResult{
		Title:           truncateSearchText(payload.Title, extractorSearchTitleBytes),
		Performer:       truncateSearchText(payload.Artist, extractorSearchChannelBytes),
		DurationSeconds: int64(payload.Duration),
	}
	if result.Performer == "" {
		result.Performer = truncateSearchText(payload.Uploader, extractorSearchChannelBytes)
	}
	if result.DurationSeconds < 0 {
		result.DurationSeconds = 0
	}

	var bestAudioSize int64
	for _, format := range payload.Formats {
		if strings.EqualFold(format.Ext, "m4a") &&
			strings.EqualFold(format.VCodec, "none") &&
			!strings.EqualFold(format.ACodec, "none") &&
			format.size() > bestAudioSize {
			bestAudioSize = format.size()
		}
	}

	type candidate struct {
		tbr  float64
		size int64
	}
	byHeight := make(map[int]candidate, len(extractorQualityHeights))
	for _, format := range payload.Formats {
		if !strings.EqualFold(format.Ext, "mp4") ||
			format.Height <= 0 ||
			strings.EqualFold(format.VCodec, "none") {
			continue
		}
		if !validVideoMaxHeight(format.Height) || format.Height == 0 {
			continue
		}
		size := format.size()
		if strings.EqualFold(format.ACodec, "none") && size > 0 && bestAudioSize > 0 {
			size += bestAudioSize
		}
		current, ok := byHeight[format.Height]
		if !ok || format.TBR > current.tbr || (format.TBR == current.tbr && size > current.size) {
			byHeight[format.Height] = candidate{tbr: format.TBR, size: size}
		}
	}
	for _, height := range extractorQualityHeights {
		if candidate, ok := byHeight[height]; ok {
			result.VideoQualities = append(result.VideoQualities, VideoQuality{
				Height: height,
				Size:   candidate.size,
			})
		}
	}
	return result
}

func parseExtractorProbe(stdout string) (ProbeResult, error) {
	var payload extractorProbePayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return ProbeResult{}, fmt.Errorf("%w: decode yt-dlp probe output: %v", ErrProbeFailed, err)
	}
	return normalizeExtractorProbe(payload), nil
}

// Probe inspects metadata and concrete MP4 qualities without downloading media.
// The physical yt-dlp process is charged only to the shared process resource.
func (p *ExtractorProvider) Probe(ctx context.Context, rawURL string, opts ProbeOptions) (ProbeResult, error) {
	parsedURL, err := ValidateURL(rawURL)
	if err != nil {
		return ProbeResult{}, err
	}
	ytdlpPath, err := p.extractorPath()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%w: please install yt-dlp on the host machine to inspect media formats", ErrExtractorUnavailable)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	if timeout > MaxProbeTimeout {
		timeout = MaxProbeTimeout
	}
	req := process.Request{
		Command: ytdlpPath,
		Args: []string{
			"--ignore-config",
			"--no-warnings",
			"--simulate",
			"--dump-single-json",
			parsedURL.String(),
		},
		Timeout:   timeout,
		MaxOutput: extractorProbeMaxOutput,
	}

	var res *process.Result
	run := func(runCtx context.Context) error {
		var runErr error
		res, runErr = p.runner.Run(runCtx, req)
		return runErr
	}

	p.mu.Lock()
	taskClient := p.tasks
	p.mu.Unlock()
	if !tasks.HasHeldResource(ctx, "process") && taskClient != nil {
		ticket, taskErr := taskClient.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("extractor-probe:%d", time.Now().UnixNano())),
			QuotaOwner:       "download:extractor-probe",
			Pool:             "download",
			Class:            tasks.PriorityInteractive,
			ExecutionTimeout: timeout,
			Resources:        []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Handler:          run,
		})
		if taskErr != nil {
			return ProbeResult{}, fmt.Errorf("%w: failed to allocate process resource: %v", ErrProbeFailed, taskErr)
		}
		result, waitErr := ticket.Wait(ctx)
		if waitErr != nil {
			return ProbeResult{}, fmt.Errorf("%w: extractor probe process wait failed: %v", ErrProbeFailed, waitErr)
		}
		if !result.IsSuccess() {
			return ProbeResult{}, fmt.Errorf("%w: yt-dlp probe task failed: %s", ErrProbeFailed, result.Failure.Message)
		}
	} else if err := run(ctx); err != nil {
		stderr := ""
		if res != nil {
			stderr = strings.TrimSpace(res.Stderr)
		}
		return ProbeResult{}, fmt.Errorf("%w: yt-dlp probe failed: %s: %v", ErrProbeFailed, stderr, err)
	}

	if res == nil {
		return ProbeResult{}, fmt.Errorf("%w: yt-dlp probe returned no process result", ErrProbeFailed)
	}
	if res.Truncated {
		return ProbeResult{}, fmt.Errorf("%w: yt-dlp probe output exceeded %d bytes", core.ErrResourceLimit, extractorProbeMaxOutput)
	}
	if res.ExitCode != 0 {
		return ProbeResult{}, fmt.Errorf("%w: yt-dlp probe exited with code %d: %s", ErrProbeFailed, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return parseExtractorProbe(res.Stdout)
}

var _ ProbeProvider = (*ExtractorProvider)(nil)
