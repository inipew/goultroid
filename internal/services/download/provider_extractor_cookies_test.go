package download

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type extractorCookieArgRecorder struct {
	requests []process.Request
	mode     string // "search", "probe", "download"
}

func (r *extractorCookieArgRecorder) Run(_ context.Context, req process.Request) (*process.Result, error) {
	r.requests = append(r.requests, req)
	switch r.mode {
	case "search":
		return &process.Result{Stdout: `{"entries":[{"id":"abcdefghijk","title":"Lofi Beats","url":"https://www.youtube.com/watch?v=abcdefghijk"}]}`}, nil
	case "probe":
		return &process.Result{Stdout: `{"title":"T","formats":[]}`}, nil
	case "download":
		outPath := filepath.Join(req.WorkingDir, "video.mp4")
		if err := os.WriteFile(outPath, []byte("video-bytes"), 0600); err != nil {
			return nil, err
		}
		return &process.Result{Stdout: outPath + "\n"}, nil
	default:
		return &process.Result{}, nil
	}
}

func assertCookiesArg(t *testing.T, args []string, wantPath string) {
	t.Helper()
	idx := slices.Index(args, "--cookies")
	if wantPath == "" {
		if idx != -1 {
			t.Fatalf("unexpected --cookies in args %v", args)
		}
		return
	}
	if idx == -1 || idx+1 >= len(args) || args[idx+1] != wantPath {
		t.Fatalf("args %v missing [--cookies %q]", args, wantPath)
	}
}

func TestExtractorProviderCookiesOption(t *testing.T) {
	ctx := tasks.WithHeldResource(context.Background(), "process")
	cookiePath := "/tmp/ytdlp-cookies-test.txt"

	t.Run("search_with_and_without_cookies", func(t *testing.T) {
		rec := &extractorCookieArgRecorder{mode: "search"}
		p := NewExtractorProvider(rec, 500*1024*1024)
		p.lookPath = func(string) (string, error) { return "/usr/bin/yt-dlp", nil }

		if _, err := p.Search(ctx, "lofi", SearchOptions{Limit: 1}); err != nil {
			t.Fatalf("Search() without cookies error = %v", err)
		}
		assertCookiesArg(t, rec.requests[0].Args, "")

		if _, err := p.Search(ctx, "lofi", SearchOptions{Limit: 1, CookiesFile: cookiePath}); err != nil {
			t.Fatalf("Search() with cookies error = %v", err)
		}
		assertCookiesArg(t, rec.requests[1].Args, cookiePath)
	})

	t.Run("probe_with_and_without_cookies", func(t *testing.T) {
		rec := &extractorCookieArgRecorder{mode: "probe"}
		p := NewExtractorProvider(rec, 500*1024*1024)
		p.lookPath = func(string) (string, error) { return "/usr/bin/yt-dlp", nil }

		if _, err := p.Probe(ctx, "https://www.youtube.com/watch?v=abcdefghijk", ProbeOptions{}); err != nil {
			t.Fatalf("Probe() without cookies error = %v", err)
		}
		assertCookiesArg(t, rec.requests[0].Args, "")

		if _, err := p.Probe(ctx, "https://www.youtube.com/watch?v=abcdefghijk", ProbeOptions{CookiesFile: cookiePath}); err != nil {
			t.Fatalf("Probe() with cookies error = %v", err)
		}
		assertCookiesArg(t, rec.requests[1].Args, cookiePath)
	})

	t.Run("download_with_and_without_cookies", func(t *testing.T) {
		rec := &extractorCookieArgRecorder{mode: "download"}
		p := NewExtractorProvider(rec, 500*1024*1024)
		p.lookPath = func(string) (string, error) { return "/usr/bin/yt-dlp", nil }
		store := storage.NewMemoryStorage()

		if _, err := p.Download(ctx, "https://www.youtube.com/watch?v=abcdefghijk", store, DownloadOptions{}); err != nil {
			t.Fatalf("Download() without cookies error = %v", err)
		}
		assertCookiesArg(t, rec.requests[0].Args, "")

		if _, err := p.Download(ctx, "https://www.youtube.com/watch?v=abcdefghijk", store, DownloadOptions{CookiesFile: cookiePath}); err != nil {
			t.Fatalf("Download() with cookies error = %v", err)
		}
		assertCookiesArg(t, rec.requests[1].Args, cookiePath)
	})
}
