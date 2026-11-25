package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/storage"
)

// DirectHTTPProvider handles standard HTTP and HTTPS downloads with SSRF validation.
type DirectHTTPProvider struct {
	client         *http.Client
	defaultTimeout time.Duration
	defaultMaxCap  int64
}

// Ensure DirectHTTPProvider implements Provider.
var _ Provider = (*DirectHTTPProvider)(nil)

// NewDirectHTTPProvider creates a new DirectHTTPProvider.
func NewDirectHTTPProvider(timeout time.Duration, defaultMaxCap int64) *DirectHTTPProvider {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if defaultMaxCap <= 0 {
		defaultMaxCap = 500 * 1024 * 1024 // 500 MB default
	}

	return &DirectHTTPProvider{
		client:         NewSafeHTTPClient(timeout),
		defaultTimeout: timeout,
		defaultMaxCap:  defaultMaxCap,
	}
}

// Name returns the provider identifier.
func (p *DirectHTTPProvider) Name() string {
	return "http"
}

// Match returns true if rawURL is a valid HTTP or HTTPS address.
func (p *DirectHTTPProvider) Match(rawURL string) bool {
	u, err := ValidateURL(rawURL)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// progressReader wraps an io.Reader and reports byte progress.
type progressReader struct {
	reader io.Reader
	total  int64
	read   int64
	cb     ProgressCallback
}

func (pr *progressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	if n > 0 {
		pr.read += int64(n)
		if pr.cb != nil {
			pr.cb(pr.read, pr.total)
		}
	}
	return n, err
}

// Download fetches the file via HTTP and saves it to storage.
func (p *DirectHTTPProvider) Download(ctx context.Context, rawURL string, store storage.Storage, opts DownloadOptions) (*storage.Asset, error) {
	if store == nil {
		return nil, fmt.Errorf("storage destination cannot be nil")
	}

	parsedURL, err := ValidateURL(rawURL)
	if err != nil {
		return nil, err
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = p.defaultMaxCap
	}

	reqTimeout := opts.Timeout
	if reqTimeout <= 0 {
		reqTimeout = p.defaultTimeout
	}

	reqCtx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("User-Agent", "GoUltroid/1.0 (Media Downloader)")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedSSRF) || strings.Contains(err.Error(), ErrBlockedSSRF.Error()) {
			return nil, ErrBlockedSSRF
		}
		return nil, fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: server responded with %s", ErrDownloadFailed, resp.Status)
	}

	// Early content length check
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: content length %d exceeds max allowed %d", core.ErrResourceLimit, resp.ContentLength, maxBytes)
	}

	// Determine MIME type before filename fallback so extensionless URLs get a typed filename.
	mimeType := normalizeHTTPMIME(resp.Header.Get("Content-Type"))

	// Resolve filename
	fileName := opts.TargetFilename
	if fileName == "" {
		// Try Content-Disposition
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil {
				if f, ok := params["filename"]; ok && f != "" {
					fileName = f
				}
			}
		}
	}
	if fileName == "" {
		// Fallback to URL path
		if unescaped, err := url.PathUnescape(parsedURL.Path); err == nil && unescaped != "" {
			base := path.Base(unescaped)
			if base != "/" && base != "." {
				fileName = base
			}
		}
	}
	if fileName == "" {
		fileName = defaultHTTPFilename(mimeType)
	}
	fileName = core.SanitizeFileName(fileName)
	if fileName == "" || fileName == "." {
		fileName = defaultHTTPFilename(mimeType)
	}

	// Bounded limit reader to prevent streaming beyond quota
	limitedStream := io.LimitReader(resp.Body, maxBytes+1)

	reader := limitedStream
	if opts.Progress != nil {
		reader = &progressReader{
			reader: limitedStream,
			total:  resp.ContentLength,
			cb:     opts.Progress,
		}
	}

	asset, err := store.Put(ctx, reader, storage.Metadata{
		Name: fileName,
		MIME: mimeType,
	})
	if err != nil {
		return nil, err
	}

	if asset.Size > maxBytes {
		_ = store.Delete(ctx, asset.ID)
		return nil, fmt.Errorf("%w: download exceeded size limit %d", core.ErrResourceLimit, maxBytes)
	}

	return asset, nil
}

func normalizeHTTPMIME(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "application/octet-stream"
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" {
		return strings.ToLower(mediaType)
	}
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}
	if contentType == "" {
		return "application/octet-stream"
	}
	return strings.ToLower(contentType)
}

func defaultHTTPFilename(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "video/mp4":
		return "downloaded_file.mp4"
	case "video/webm":
		return "downloaded_file.webm"
	case "audio/mpeg":
		return "downloaded_file.mp3"
	case "audio/mp4":
		return "downloaded_file.m4a"
	case "audio/ogg":
		return "downloaded_file.ogg"
	case "image/jpeg":
		return "downloaded_file.jpg"
	case "image/png":
		return "downloaded_file.png"
	case "image/webp":
		return "downloaded_file.webp"
	case "image/gif":
		return "downloaded_file.gif"
	case "application/pdf":
		return "downloaded_file.pdf"
	case "application/zip":
		return "downloaded_file.zip"
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return "downloaded_file" + exts[0]
	}
	return "downloaded_file.bin"
}
