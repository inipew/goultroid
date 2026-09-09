package download

import (
	"context"
	"fmt"
	"net/url"
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
	defaultMaxCap int64
	mu            sync.Mutex
	activeTemps   map[string]time.Time
}

// Ensure ExtractorProvider implements Provider.
var _ Provider = (*ExtractorProvider)(nil)

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

// IsAvailable checks whether the yt-dlp binary is present on the host system.
func (p *ExtractorProvider) IsAvailable() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// Download extracts media using yt-dlp through the process runner.
func (p *ExtractorProvider) Download(ctx context.Context, rawURL string, store storage.Storage, opts DownloadOptions) (*storage.Asset, error) {
	if store == nil {
		return nil, fmt.Errorf("storage destination cannot be nil")
	}

	ytdlpPath, err := exec.LookPath("yt-dlp")
	if err != nil {
		return nil, fmt.Errorf("%w: please install yt-dlp on the host machine to download from this link", ErrExtractorUnavailable)
	}

	// Validate URL scheme
	parsedURL, err := ValidateURL(rawURL)
	if err != nil {
		return nil, err
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
		"-o", outTemplate,
		parsedURL.String(),
	}

	req := process.Request{
		Command:    ytdlpPath,
		Args:       args,
		Timeout:    timeout,
		WorkingDir: tmpDir,
	}

	res, err := p.runner.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: yt-dlp failed (exit %d): %s (%v)", ErrDownloadFailed, res.ExitCode, res.Stderr, err)
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
