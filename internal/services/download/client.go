package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

var (
	// ErrBlockedSSRF indicates that a destination IP is within a prohibited private or loopback range.
	ErrBlockedSSRF = errors.New("connection blocked: destination ip address is private, loopback, or invalid")
	// ErrInvalidScheme indicates that the URL scheme is not allowed (only http and https are permitted).
	ErrInvalidScheme = errors.New("invalid url scheme: only http and https are allowed")
	// ErrTooManyRedirects indicates excessive redirects during download.
	ErrTooManyRedirects = errors.New("too many redirects encountered")
)

// Private, loopback, and reserved IP CIDR blocks to block for SSRF prevention.
var blockedCIDRs = []*net.IPNet{
	mustParseCIDR("127.0.0.0/8"),    // IPv4 Loopback
	mustParseCIDR("10.0.0.0/8"),     // RFC 1918 Private
	mustParseCIDR("172.16.0.0/12"),  // RFC 1918 Private
	mustParseCIDR("192.168.0.0/16"), // RFC 1918 Private
	mustParseCIDR("169.254.0.0/16"), // IPv4 Link-Local
	mustParseCIDR("0.0.0.0/8"),      // Broadcast / Current network
	mustParseCIDR("224.0.0.0/4"),    // IPv4 Multicast
	mustParseCIDR("240.0.0.0/4"),    // IPv4 Reserved
	mustParseCIDR("::1/128"),        // IPv6 Loopback
	mustParseCIDR("fc00::/7"),       // IPv6 Unique Local Address
	mustParseCIDR("fe80::/10"),      // IPv6 Link-Local
	mustParseCIDR("ff00::/8"),       // IPv6 Multicast
}

func mustParseCIDR(s string) *net.IPNet {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("invalid CIDR %s: %v", s, err))
	}
	return ipnet
}

// IsPrivateOrProhibitedIP returns true if the specified IP is loopback, private, link-local, or reserved.
func IsPrivateOrProhibitedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, block := range blockedCIDRs {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateURL verifies that the provided string is a valid HTTP/HTTPS URL with non-empty host.
func ValidateURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("%w: url cannot be empty", core.ErrInvalidArgs)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to parse url: %v", core.ErrInvalidArgs, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q", ErrInvalidScheme, u.Scheme)
	}

	if u.Hostname() == "" {
		return nil, fmt.Errorf("%w: missing host in url", core.ErrInvalidArgs)
	}

	return u, nil
}

// NewSafeTransport constructs an http.Transport with DNS resolution verification against SSRF.
func NewSafeTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}

			ip := net.ParseIP(host)
			if ip != nil && IsPrivateOrProhibitedIP(ip) {
				return fmt.Errorf("%w: ip %s", ErrBlockedSSRF, ip.String())
			}
			return nil
		},
	}

	return &http.Transport{
		Proxy:                 nil, // Direct connections only, preventing SSRF bypass via environment proxies
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// NewSafeHTTPClient creates a secure HTTP client with SSRF protection, timeout, and redirect guard.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: NewSafeTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return ErrTooManyRedirects
			}
			// Validate redirect destination
			if _, err := ValidateURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}

	return client
}

// Downloader provides methods to download files from URLs safely.
type Downloader interface {
	Download(ctx context.Context, rawURL string, dstPath string, maxBytes int64) (int64, error)
}

// SafeDownloader implements Downloader using SafeHTTPClient.
type SafeDownloader struct {
	client         *http.Client
	defaultMaxCap  int64
	defaultTimeout time.Duration
}

// Ensure SafeDownloader implements Downloader.
var _ Downloader = (*SafeDownloader)(nil)

// NewSafeDownloader creates a new SafeDownloader.
func NewSafeDownloader(timeout time.Duration, defaultMaxCap int64) *SafeDownloader {
	if defaultMaxCap <= 0 {
		defaultMaxCap = 500 * 1024 * 1024 // 500 MB default
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &SafeDownloader{
		client:         NewSafeHTTPClient(timeout),
		defaultMaxCap:  defaultMaxCap,
		defaultTimeout: timeout,
	}
}

// Download downloads content from rawURL into dstPath with SSRF guard and size limits.
func (d *SafeDownloader) Download(ctx context.Context, rawURL string, dstPath string, maxBytes int64) (int64, error) {
	if _, err := ValidateURL(rawURL); err != nil {
		return 0, err
	}

	if maxBytes <= 0 {
		maxBytes = d.defaultMaxCap
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build download request: %w", err)
	}
	req.Header.Set("User-Agent", "GoUltroid/1.0")

	resp, err := d.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedSSRF) || strings.Contains(err.Error(), ErrBlockedSSRF.Error()) {
			return 0, ErrBlockedSSRF
		}
		return 0, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("server responded with status %d: %s", resp.StatusCode, resp.Status)
	}

	// Content-Length early validation
	if resp.ContentLength > maxBytes {
		return 0, fmt.Errorf("%w: content length %d exceeds max allowed %d", core.ErrResourceLimit, resp.ContentLength, maxBytes)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return 0, fmt.Errorf("failed to create destination directory: %w", err)
	}

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return 0, fmt.Errorf("failed to create output file: %w", err)
	}
	defer out.Close()

	// Enforce streaming max output boundary
	limitedReader := io.LimitReader(resp.Body, maxBytes+1)
	written, err := io.Copy(out, limitedReader)
	if err != nil {
		_ = os.Remove(dstPath)
		return 0, fmt.Errorf("failed during data transfer: %w", err)
	}

	if written > maxBytes {
		_ = os.Remove(dstPath)
		return 0, fmt.Errorf("%w: downloaded bytes %d exceeded limit %d", core.ErrResourceLimit, written, maxBytes)
	}

	return written, nil
}
