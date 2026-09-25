package download

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	extractorSearchMaxOutput        int64 = 1024 * 1024
	extractorSearchTitleBytes             = 256
	extractorSearchDescriptionBytes       = 1024
	extractorSearchChannelBytes           = 128
)

var extractorDomains = []string{
	"youtube.com", "youtu.be",
	"instagram.com",
	"twitter.com", "x.com",
	"tiktok.com",
	"pinterest.com", "pin.it",
	"reddit.com",
	"facebook.com", "fb.watch",
	"vimeo.com",
	"soundcloud.com",
	"twitch.tv",
}

// ExtractorProvider handles video and social media downloads using yt-dlp.
type ExtractorProvider struct {
	runner        process.Runner
	lookPath      func(string) (string, error)
	defaultMaxCap int64
	tasks         tasks.Client
	mu            sync.Mutex
	activeTemps   map[string]time.Time
}

// Ensure ExtractorProvider implements the download and optional search capabilities.
var _ Provider = (*ExtractorProvider)(nil)
var _ SearchProvider = (*ExtractorProvider)(nil)

// SetTasks attaches the tasks client used to acquire execution resources.
func (p *ExtractorProvider) SetTasks(client tasks.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks = client
}

// NewExtractorProvider creates a new ExtractorProvider.
func NewExtractorProvider(runner process.Runner, defaultMaxCap int64) *ExtractorProvider {
	if runner == nil {
		runner = process.NewOSRunner(2, 5*time.Minute, 4*1024*1024)
	}
	if defaultMaxCap <= 0 {
		defaultMaxCap = 500 * 1024 * 1024
	}
	return &ExtractorProvider{
		runner:        runner,
		lookPath:      exec.LookPath,
		defaultMaxCap: defaultMaxCap,
		activeTemps:   make(map[string]time.Time),
	}
}

// ActiveTempDirectories returns a snapshot of extractor-owned temporary
// directories currently awaiting cleanup.
func (p *ExtractorProvider) ActiveTempDirectories() map[string]time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]time.Time, len(p.activeTemps))
	for path, createdAt := range p.activeTemps {
		result[path] = createdAt
	}
	return result
}

// Name returns the provider identifier.
func (p *ExtractorProvider) Name() string {
	return "extractor"
}

// Match checks if rawURL points to a supported streaming/media platform.
func (p *ExtractorProvider) Match(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range extractorDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func (p *ExtractorProvider) extractorPath() (string, error) {
	lookup := exec.LookPath
	if p != nil && p.lookPath != nil {
		lookup = p.lookPath
	}
	return lookup("yt-dlp")
}

// IsAvailable checks whether the yt-dlp binary is present on the host system.
func (p *ExtractorProvider) IsAvailable() bool {
	_, err := p.extractorPath()
	return err == nil
}

type extractorSearchPayload struct {
	Entries []extractorSearchEntry `json:"entries"`
}

type extractorSearchEntry struct {
	ID          string                     `json:"id"`
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	Thumbnail   string                     `json:"thumbnail"`
	Thumbnails  []extractorSearchThumbnail `json:"thumbnails"`
	Channel     string                     `json:"channel"`
	Uploader    string                     `json:"uploader"`
	Duration    float64                    `json:"duration"`
	ViewCount   int64                      `json:"view_count"`
	UploadDate  string                     `json:"upload_date"`
}

type extractorSearchThumbnail struct {
	URL string `json:"url"`
}

func normalizeSearchRequest(query string, opts SearchOptions) (string, int, time.Duration, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > MaxSearchQueryBytes || strings.IndexByte(query, 0) >= 0 {
		return "", 0, 0, fmt.Errorf("%w: search query must contain 1..%d bytes", core.ErrInvalidArgs, MaxSearchQueryBytes)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultSearchTimeout
	}
	if timeout > MaxSearchTimeout {
		timeout = MaxSearchTimeout
	}
	return query, limit, timeout, nil
}

func validYouTubeVideoID(id string) bool {
	if len(id) != 11 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func truncateSearchText(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && value != "" {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 || size > len(value) {
			value = value[:len(value)-1]
			continue
		}
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

func normalizeSearchThumbnail(entry extractorSearchEntry) string {
	candidates := make([]string, 0, 1+len(entry.Thumbnails))
	candidates = append(candidates, entry.Thumbnail)
	for i := len(entry.Thumbnails) - 1; i >= 0; i-- {
		candidates = append(candidates, entry.Thumbnails[i].URL)
	}
	for _, candidate := range candidates {
		u, err := url.Parse(strings.TrimSpace(candidate))
		if err == nil && u != nil && strings.EqualFold(u.Scheme, "https") && u.Hostname() != "" {
			return u.String()
		}
	}
	return ""
}

func normalizeExtractorSearch(payload extractorSearchPayload, limit int) []SearchResult {
	results := make([]SearchResult, 0, limit)
	for _, entry := range payload.Entries {
		id := strings.TrimSpace(entry.ID)
		if !validYouTubeVideoID(id) {
			continue
		}
		title := truncateSearchText(entry.Title, extractorSearchTitleBytes)
		if title == "" {
			continue
		}
		channel := entry.Channel
		if strings.TrimSpace(channel) == "" {
			channel = entry.Uploader
		}
		duration := int64(entry.Duration)
		if duration < 0 {
			duration = 0
		}
		views := entry.ViewCount
		if views < 0 {
			views = 0
		}
		results = append(results, SearchResult{
			Provider:        "extractor",
			Source:          "youtube",
			SourceID:        id,
			URL:             "https://www.youtube.com/watch?v=" + id,
			Title:           title,
			Description:     truncateSearchText(entry.Description, extractorSearchDescriptionBytes),
			Thumbnail:       normalizeSearchThumbnail(entry),
			Channel:         truncateSearchText(channel, extractorSearchChannelBytes),
			DurationSeconds: duration,
			Views:           views,
			PublishedAt:     strings.TrimSpace(entry.UploadDate),
		})
		if len(results) >= limit {
			break
		}
	}
	return results
}

func parseExtractorSearch(stdout string, limit int) ([]SearchResult, error) {
	var payload extractorSearchPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return nil, fmt.Errorf("%w: decode yt-dlp search output: %v", ErrSearchFailed, err)
	}
	results := normalizeExtractorSearch(payload, limit)
	if len(results) == 0 {
		return nil, ErrSearchNoResults
	}
	return results, nil
}

// Search performs bounded YouTube metadata discovery using yt-dlp without
// downloading media. The process resource is held only for the physical search.
func (p *ExtractorProvider) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	query, limit, timeout, err := normalizeSearchRequest(query, opts)
	if err != nil {
		return nil, err
	}
	ytdlpPath, err := p.extractorPath()
	if err != nil {
		return nil, fmt.Errorf("%w: please install yt-dlp on the host machine to search YouTube", ErrExtractorUnavailable)
	}
	req := process.Request{
		Command: ytdlpPath,
		Args: []string{
			"--ignore-config",
			"--no-warnings",
			"--simulate",
			"--flat-playlist",
			"--dump-single-json",
			fmt.Sprintf("ytsearch%d:%s", limit, query),
		},
		Timeout:   timeout,
		MaxOutput: extractorSearchMaxOutput,
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
			ID:               tasks.TaskID(fmt.Sprintf("extractor-search:%d", time.Now().UnixNano())),
			QuotaOwner:       "download:extractor-search",
			Pool:             "download",
			Class:            tasks.PriorityInteractive,
			ExecutionTimeout: timeout,
			Resources:        []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Handler:          run,
		})
		if taskErr != nil {
			return nil, fmt.Errorf("%w: failed to allocate process resource: %v", ErrSearchFailed, taskErr)
		}
		result, waitErr := ticket.Wait(ctx)
		if waitErr != nil {
			return nil, fmt.Errorf("%w: extractor search process wait failed: %v", ErrSearchFailed, waitErr)
		}
		if !result.IsSuccess() {
			return nil, fmt.Errorf("%w: yt-dlp search task failed: %s", ErrSearchFailed, result.Failure.Message)
		}
	} else if err := run(ctx); err != nil {
		stderr := ""
		if res != nil {
			stderr = strings.TrimSpace(res.Stderr)
		}
		return nil, fmt.Errorf("%w: yt-dlp search failed: %s: %v", ErrSearchFailed, stderr, err)
	}
	if res == nil {
		return nil, fmt.Errorf("%w: yt-dlp search returned no process result", ErrSearchFailed)
	}
	if res.Truncated {
		return nil, fmt.Errorf("%w: yt-dlp search output exceeded %d bytes", core.ErrResourceLimit, extractorSearchMaxOutput)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%w: yt-dlp search exited with code %d: %s", ErrSearchFailed, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return parseExtractorSearch(res.Stdout, limit)
}

func extractorSelectionArgs(opts DownloadOptions) ([]string, error) {
	switch opts.Mode {
	case MediaModeDefault:
		if opts.Format != MediaFormatDefault {
			return nil, fmt.Errorf("%w: extractor format requires a media mode", core.ErrInvalidArgs)
		}
		return nil, nil
	case MediaModeAudio:
		switch opts.Format {
		case MediaFormatM4A:
			return []string{"-f", "bestaudio[ext=m4a]/bestaudio"}, nil
		case MediaFormatMP3:
			return []string{"-f", "bestaudio/best", "-x", "--audio-format", "mp3"}, nil
		default:
			return nil, fmt.Errorf("%w: unsupported audio extractor format %q", core.ErrInvalidArgs, opts.Format)
		}
	case MediaModeVideo:
		switch opts.Format {
		case MediaFormatMP4:
			return []string{"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best", "--merge-output-format", "mp4"}, nil
		case MediaFormatBest:
			return []string{"-f", "bestvideo+bestaudio/best"}, nil
		default:
			return nil, fmt.Errorf("%w: unsupported video extractor format %q", core.ErrInvalidArgs, opts.Format)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported extractor media mode %q", core.ErrInvalidArgs, opts.Mode)
	}
}

// Download extracts media using yt-dlp through the process runner.
func (p *ExtractorProvider) Download(ctx context.Context, rawURL string, store storage.Storage, opts DownloadOptions) (*storage.Asset, error) {
	if store == nil {
		return nil, fmt.Errorf("storage destination cannot be nil")
	}

	ytdlpPath, err := p.extractorPath()
	if err != nil {
		return nil, fmt.Errorf("%w: please install yt-dlp on the host machine to download from this link", ErrExtractorUnavailable)
	}

	// Validate URL scheme
	parsedURL, err := ValidateURL(rawURL)
	if err != nil {
		return nil, err
	}
	selectionArgs, err := extractorSelectionArgs(opts)
	if err != nil {
		return nil, err
	}

	var progressOutputObserver func([]byte)
	if opts.Progress != nil {
		progressObserver := newExtractorProgressObserver(opts.Progress)
		progressOutputObserver = progressObserver.Observe
	}

	tmpDir, err := os.MkdirTemp("", "goultroid-ytdlp-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary extraction folder: %w", err)
	}
	p.mu.Lock()
	p.activeTemps[tmpDir] = time.Now().UTC()
	p.mu.Unlock()
	defer func() {
		_ = os.RemoveAll(tmpDir)
		p.mu.Lock()
		delete(p.activeTemps, tmpDir)
		p.mu.Unlock()
	}()

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	outTemplate := filepath.Join(tmpDir, "%(title).150B.%(ext)s")
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", fmt.Sprintf("%d", p.defaultMaxCap),
	}
	if progressOutputObserver != nil {
		args = append(args,
			"--newline",
			"--progress",
			"--progress-delta", "0.5",
			"--progress-template", extractorProgressTemplate,
		)
	}
	args = append(args, selectionArgs...)
	args = append(args,
		"-o", outTemplate,
		parsedURL.String(),
	)

	req := process.Request{
		Command:        ytdlpPath,
		Args:           args,
		Timeout:        timeout,
		WorkingDir:     tmpDir,
		StdoutObserver: progressOutputObserver,
		StderrObserver: progressOutputObserver,
	}

	var res *process.Result
	p.mu.Lock()
	taskClient := p.tasks
	p.mu.Unlock()

	if !tasks.HasHeldResource(ctx, "process") && taskClient != nil {
		ticket, taskErr := taskClient.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("extractor:%d", time.Now().UnixNano())),
			QuotaOwner:       "download:extractor",
			Pool:             "download",
			Class:            tasks.PriorityNormal,
			ExecutionTimeout: timeout,
			Resources:        []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Handler: func(taskCtx context.Context) error {
				var runErr error
				res, runErr = p.runner.Run(taskCtx, req)
				return runErr
			},
		})
		if taskErr != nil {
			return nil, fmt.Errorf("%w: failed to allocate process resource: %v", ErrDownloadFailed, taskErr)
		}
		result, waitErr := ticket.Wait(ctx)
		if waitErr != nil {
			return nil, fmt.Errorf("%w: extractor process wait failed: %v", ErrDownloadFailed, waitErr)
		}
		if !result.IsSuccess() {
			exitCode := 1
			stderr := ""
			if res != nil {
				exitCode = res.ExitCode
				stderr = res.Stderr
			}
			return nil, fmt.Errorf("%w: yt-dlp failed (exit %d): %s (%v)", ErrDownloadFailed, exitCode, stderr, result.Failure.Message)
		}
	} else {
		var runErr error
		res, runErr = p.runner.Run(ctx, req)
		if runErr != nil {
			exitCode := 1
			stderr := ""
			if res != nil {
				exitCode = res.ExitCode
				stderr = res.Stderr
			}
			return nil, fmt.Errorf("%w: yt-dlp failed (exit %d): %s (%v)", ErrDownloadFailed, exitCode, stderr, runErr)
		}
	}

	// Find the extracted file in tmpDir
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to scan downloaded files: %w", err)
	}

	var targetFile string
	for _, entry := range entries {
		if !entry.IsDir() {
			targetFile = filepath.Join(tmpDir, entry.Name())
			break
		}
	}

	if targetFile == "" {
		return nil, fmt.Errorf("%w: no file produced by extractor", ErrDownloadFailed)
	}

	f, err := os.Open(targetFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open extracted file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect extracted file: %w", err)
	}

	if opts.MaxBytes > 0 && stat.Size() > opts.MaxBytes {
		return nil, fmt.Errorf("%w: extracted file size %d exceeds limit %d", core.ErrResourceLimit, stat.Size(), opts.MaxBytes)
	}

	fileName := filepath.Base(targetFile)
	asset, err := store.Put(ctx, f, storage.Metadata{
		Name: fileName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to persist extracted file to storage: %w", err)
	}

	return asset, nil
}
