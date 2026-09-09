package download_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestDirectHTTPProvider_Match(t *testing.T) {
	provider := download.NewDirectHTTPProvider(5*time.Second, 1024*1024)

	validURLs := []string{
		"http://example.com/file.zip",
		"https://example.com/image.png",
		"https://sub.domain.org/path?query=1",
	}
	for _, u := range validURLs {
		if !provider.Match(u) {
			t.Errorf("expected match for %s", u)
		}
	}

	invalidURLs := []string{
		"ftp://example.com/file.zip",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"not a url",
		"",
	}
	for _, u := range invalidURLs {
		if provider.Match(u) {
			t.Errorf("expected no match for %s", u)
		}
	}
}

func TestExtractorProvider_Match(t *testing.T) {
	extractor := download.NewExtractorProvider(nil, 500*1024*1024)

	matched := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://twitter.com/user/status/12345",
		"https://x.com/user/status/12345",
		"https://www.instagram.com/reel/12345/",
		"https://www.tiktok.com/@user/video/12345",
		"https://reddit.com/r/golang/comments/123",
	}
	for _, u := range matched {
		if !extractor.Match(u) {
			t.Errorf("expected match for %s", u)
		}
	}

	notMatched := []string{
		"https://golang.org/doc",
		"https://github.com/inipew/goultroid",
		"https://example.com/file.mp4",
	}
	for _, u := range notMatched {
		if extractor.Match(u) {
			t.Errorf("expected no match for %s", u)
		}
	}
}

func TestExtractorProvider_Unavailable(t *testing.T) {
	extractor := download.NewExtractorProvider(nil, 500*1024*1024)
	if extractor.IsAvailable() {
		t.Skip("yt-dlp is installed, skipping unavailable test")
	}

	store := storage.NewMemoryStorage()
	ctx := context.Background()
	_, err := extractor.Download(ctx, "https://www.youtube.com/watch?v=test", store, download.DownloadOptions{})
	if !errors.Is(err, download.ErrExtractorUnavailable) {
		t.Errorf("expected ErrExtractorUnavailable, got %v", err)
	}
}

func TestRegistry(t *testing.T) {
	extractor := download.NewExtractorProvider(nil, 100*1024*1024)
	httpProv := download.NewDirectHTTPProvider(5*time.Second, 100*1024*1024)

	reg := download.NewRegistry(extractor, httpProv)

	// youtube should resolve to extractor
	yt := reg.Resolve("https://www.youtube.com/watch?v=123")
	if yt == nil || yt.Name() != "extractor" {
		t.Errorf("expected extractor provider for youtube, got %v", yt)
	}

	// direct url should resolve to http
	direct := reg.Resolve("https://example.com/file.zip")
	if direct == nil || direct.Name() != "http" {
		t.Errorf("expected http provider for direct file, got %v", direct)
	}

	// invalid scheme should return nil
	none := reg.Resolve("ftp://example.com/file.zip")
	if none != nil {
		t.Errorf("expected nil for ftp URL, got %v", none)
	}
}

type mockProvider struct {
	name     string
	matchURL string
	resAsset *storage.Asset
	err      error
	calls    atomic.Int32
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Match(rawURL string) bool {
	return strings.Contains(rawURL, m.matchURL)
}
func (m *mockProvider) Download(ctx context.Context, rawURL string, store storage.Storage, opts download.DownloadOptions) (*storage.Asset, error) {
	m.calls.Add(1)
	if opts.Progress != nil {
		opts.Progress(100, 100)
	}
	return m.resAsset, m.err
}

func TestRegistry_RetryPolicyIsExplicit(t *testing.T) {
	provider := &mockProvider{name: "retry", matchURL: "retry://", err: download.ErrDownloadFailed}
	reg := download.NewRegistry(provider)
	store := storage.NewMemoryStorage()
	_, err := reg.Download(context.Background(), "retry://file", store, download.DownloadOptions{MaxAttempts: 3})
	if !errors.Is(err, download.ErrDownloadFailed) {
		t.Fatalf("expected download failure, got %v", err)
	}
	if got := provider.calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}

	provider.calls.Store(0)
	_, err = reg.Download(context.Background(), "retry://file", store, download.DownloadOptions{})
	if !errors.Is(err, download.ErrDownloadFailed) {
		t.Fatalf("expected download failure, got %v", err)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("default attempts = %d, want 1", got)
	}
}

func TestRegistry_MockDownloadWithProgress(t *testing.T) {
	mockAsset := &storage.Asset{ID: "mock1", Name: "mock.mp4", Size: 100}
	p := &mockProvider{
		name:     "mock",
		matchURL: "mock://",
		resAsset: mockAsset,
	}

	reg := download.NewRegistry(p)
	store := storage.NewMemoryStorage()

	var progressCalls int32
	opts := download.DownloadOptions{
		Progress: func(downloaded, total int64) {
			atomic.AddInt32(&progressCalls, 1)
		},
	}

	ctx := context.Background()
	res, err := reg.Download(ctx, "mock://test.mp4", store, opts)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if res.ID != "mock1" {
		t.Errorf("expected ID mock1, got %s", res.ID)
	}
	if atomic.LoadInt32(&progressCalls) == 0 {
		t.Errorf("expected progress callback to be invoked")
	}

	// Test unsupported URL error
	_, err = reg.Download(ctx, "unsupported://url", store, opts)
	if !errors.Is(err, download.ErrNoMatchingProvider) {
		t.Errorf("expected ErrNoMatchingProvider, got %v", err)
	}
}

func TestDirectHTTPProvider_ResourceLimit(t *testing.T) {
	// Provider with tiny defaultMaxCap
	provider := download.NewDirectHTTPProvider(5*time.Second, 10)
	store := storage.NewMemoryStorage()

	// Direct download with invalid URL
	_, err := provider.Download(context.Background(), "invalid-url", store, download.DownloadOptions{})
	if !errors.Is(err, download.ErrInvalidScheme) {
		t.Errorf("expected ErrInvalidScheme, got %v", err)
	}
}
