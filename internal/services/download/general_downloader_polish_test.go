package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

func TestExtractorProvider_MatchRejectsNonHTTPAndSupportsGeneralDomains(t *testing.T) {
	p := NewExtractorProvider(nil, 0)

	supported := []string{
		"https://www.tiktok.com/@user/video/12345",
		"https://www.instagram.com/reel/abc123/",
		"https://x.com/user/status/12345",
		"https://twitter.com/user/status/12345",
		"https://www.reddit.com/r/golang/comments/123/",
		"https://www.facebook.com/watch/?v=123",
		"https://vimeo.com/123456",
		"https://soundcloud.com/artist/track",
		"https://www.twitch.tv/videos/12345",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	}
	for _, raw := range supported {
		if !p.Match(raw) {
			t.Fatalf("ExtractorProvider.Match(%q) = false, want true", raw)
		}
	}

	rejected := []string{
		"ftp://tiktok.com/@user/video/12345",
		"javascript://youtube.com/%0aalert(1)",
		"tiktok.com/@user/video/12345",
		"https://not-tiktok.example/@user/video/12345",
		"",
	}
	for _, raw := range rejected {
		if p.Match(raw) {
			t.Fatalf("ExtractorProvider.Match(%q) = true, want false", raw)
		}
	}
}

func TestDirectHTTPProvider_FilenameAndMIMENormalization(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		contentType string
		disposition string
		wantName    string
		wantMIME    string
	}{
		{
			name:        "content_disposition_preferred",
			path:        "/download?id=1",
			contentType: "video/mp4; charset=utf-8",
			disposition: `attachment; filename="clip.mp4"`,
			wantName:    "clip.mp4",
			wantMIME:    "video/mp4",
		},
		{
			name:        "extensionless_url_uses_mime_extension",
			path:        "/?id=99",
			contentType: "video/mp4; charset=binary",
			wantName:    "downloaded_file.mp4",
			wantMIME:    "video/mp4",
		},
		{
			name:        "url_path_basename_fallback",
			path:        "/files/archive.tar.gz",
			contentType: "application/gzip",
			wantName:    "archive.tar.gz",
			wantMIME:    "application/gzip",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				if tc.disposition != "" {
					w.Header().Set("Content-Disposition", tc.disposition)
				}
				_, _ = w.Write([]byte("payload"))
			}))
			defer srv.Close()

			provider := NewDirectHTTPProvider(5*time.Second, 1024*1024)
			// Override HTTP client in unit test so httptest loopback server is reachable
			// without disabling ValidateURL or production SafeTransport behavior.
			provider.client = srv.Client()

			store := storage.NewMemoryStorage()
			asset, err := provider.Download(context.Background(), srv.URL+tc.path, store, DownloadOptions{})
			if err != nil {
				t.Fatalf("Download() error = %v", err)
			}
			if asset.Name != tc.wantName {
				t.Fatalf("asset.Name = %q, want %q", asset.Name, tc.wantName)
			}
			if asset.MIME != tc.wantMIME {
				t.Fatalf("asset.MIME = %q, want %q", asset.MIME, tc.wantMIME)
			}
		})
	}
}
