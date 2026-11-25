package downloader

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
)

func TestGeneralDownloader_ProviderSelectionHTTPvsExtractor(t *testing.T) {
	reg := download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	p := New(reg)

	cases := []struct {
		url         string
		wantProv    string
		wantProcess bool
	}{
		{"https://www.tiktok.com/@u/video/1", "extractor", true},
		{"https://www.instagram.com/reel/abc/", "extractor", true},
		{"https://x.com/u/status/1", "extractor", true},
		{"https://twitter.com/u/status/1", "extractor", true},
		{"https://www.reddit.com/r/golang/comments/1/", "extractor", true},
		{"https://www.facebook.com/watch/?v=1", "extractor", true},
		{"https://vimeo.com/123", "extractor", true},
		{"https://soundcloud.com/a/b", "extractor", true},
		{"https://www.twitch.tv/videos/1", "extractor", true},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "extractor", true},
		{"https://cdn.example.com/media/clip.mp4", "http", false},
	}

	for _, tc := range cases {
		prov := reg.Resolve(tc.url)
		if prov == nil || prov.Name() != tc.wantProv {
			t.Fatalf("Resolve(%q) = %v, want %q", tc.url, prov, tc.wantProv)
		}
		res := p.urlResources(tc.url)
		if !hasResource(res, "download") || hasResource(res, "process") != tc.wantProcess {
			t.Fatalf("urlResources(%q) = %+v, wantProcess=%v", tc.url, res, tc.wantProcess)
		}
	}
}

func TestGeneralDownloader_CommandRejectsMalformedURLArgsEvenWithReply(t *testing.T) {
	reg := download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	taskClient := &capturedClient{}
	p := New(reg, taskClient)

	tgSvc := &mockTelegramService{
		replyMessage: &tg.Message{
			ID:      10,
			Message: "replied text",
		},
	}

	badArgs := []string{
		"ftp://example.com/file.mp4",
		"example.com/file.mp4",
		"//example.com/file.mp4",
		"not-a-url",
	}
	for _, arg := range badArgs {
		ctx := &core.Context{
			Ctx:     context.Background(),
			Svc:     tgSvc,
			PeerID:  &tg.InputPeerSelf{},
			Message: &core.Message{ID: 11, ReplyToID: 10, IsOutgoing: true},
			Args:    []string{arg},
		}
		err := p.handleDownload(ctx)
		if !errors.Is(err, core.ErrInvalidArgs) {
			t.Fatalf("handleDownload(%q) error = %v, want ErrInvalidArgs", arg, err)
		}
	}
	if taskClient.Count() != 0 {
		t.Fatalf("bad URL args submitted %d tasks", taskClient.Count())
	}
}

func TestGeneralDownloader_CommandAcceptsUppercaseSchemeAndGeneralURLs(t *testing.T) {
	reg := download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(5*time.Minute, 500*1024*1024),
	)
	renderer := &downloaderFakeRenderer{}
	taskClient := &capturedClient{}
	p := New(reg, taskClient)
	p.SetSelfInlineRenderer(renderer)

	urls := []struct {
		raw  string
		want string
	}{
		{"HTTPS://cdn.example.com/file.mp4", "dl https://cdn.example.com/file.mp4"},
		{"https://www.tiktok.com/@u/video/123", "dl https://www.tiktok.com/@u/video/123"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dl https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	}

	for i, tc := range urls {
		if err := p.handleDownload(newDLDTestContext(&mockTelegramService{}, tc.raw)); err != nil {
			t.Fatalf("handleDownload(%q) error = %v", tc.raw, err)
		}
		if renderer.calls != i+1 || renderer.request.Query != tc.want {
			t.Fatalf("renderer calls=%d query=%q, want %q", renderer.calls, renderer.request.Query, tc.want)
		}
	}
}
