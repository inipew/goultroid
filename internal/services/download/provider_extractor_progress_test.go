package download

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestParseExtractorProgressLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		downloaded int64
		total      int64
		ok         bool
	}{
		{name: "exact total", line: "goultroid-progress:25\t100\tNA", downloaded: 25, total: 100, ok: true},
		{name: "estimated total", line: "goultroid-progress:12.0\tNA\t120.0", downloaded: 12, total: 120, ok: true},
		{name: "unknown total", line: "goultroid-progress:8\tNA\tNA", downloaded: 8, total: 0, ok: true},
		{name: "noise", line: "[download] 50%", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			downloaded, total, ok := parseExtractorProgressLine(tt.line)
			if ok != tt.ok || downloaded != tt.downloaded || total != tt.total {
				t.Fatalf("parse = (%d,%d,%v), want (%d,%d,%v)", downloaded, total, ok, tt.downloaded, tt.total, tt.ok)
			}
		})
	}
}

type extractorProgressFakeRunner struct {
	request process.Request
}

func (r *extractorProgressFakeRunner) Run(_ context.Context, req process.Request) (*process.Result, error) {
	r.request = req
	if req.StdoutObserver != nil {
		req.StdoutObserver([]byte("goultroid-progress:50\t100\tNA\n"))
	}
	if err := os.WriteFile(filepath.Join(req.WorkingDir, "sample.mp4"), []byte("media"), 0o600); err != nil {
		return nil, err
	}
	return &process.Result{ExitCode: 0}, nil
}

func TestExtractorDownloadStreamsYTDLPProgress(t *testing.T) {
	runner := &extractorProgressFakeRunner{}
	provider := NewExtractorProvider(runner, 500*1024*1024)
	provider.lookPath = func(string) (string, error) { return "/usr/bin/yt-dlp", nil }

	var downloaded, total int64
	asset, err := provider.Download(
		tasks.WithHeldResource(context.Background(), "process"),
		"https://www.youtube.com/watch?v=abcdefghijk",
		storage.NewMemoryStorage(),
		DownloadOptions{Progress: func(current, expected int64) {
			downloaded = current
			total = expected
		}},
	)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if asset == nil {
		t.Fatal("Download() returned nil asset")
	}
	if downloaded != 50 || total != 100 {
		t.Fatalf("progress = %d/%d, want 50/100", downloaded, total)
	}
	if runner.request.StdoutObserver == nil || runner.request.StderrObserver == nil {
		t.Fatal("yt-dlp progress output observers were not installed")
	}

	args := strings.Join(runner.request.Args, "\x00")
	for _, want := range []string{
		"--newline",
		"--progress",
		"--progress-delta\x000.5",
		"--progress-template\x00" + extractorProgressTemplate,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("yt-dlp args %q missing %q", runner.request.Args, want)
		}
	}
}
