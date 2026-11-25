package download

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestIsPrivateOrProhibitedIP(t *testing.T) {
	tests := []struct {
		ip       string
		prohibit bool
	}{
		{"127.0.0.1", true},
		{"127.0.1.1", true},
		{"10.0.0.1", true},
		{"10.255.255.254", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.1.100", true},
		{"169.254.169.254", true}, // AWS metadata
		{"::1", true},
		{"fc00::1", true},
		{"fe80::1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"142.250.190.46", false}, // Google public IP
	}

	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("failed to parse test ip: %s", tc.ip)
		}
		got := IsPrivateOrProhibitedIP(ip)
		if got != tc.prohibit {
			t.Errorf("IsPrivateOrProhibitedIP(%s) = %v, want %v", tc.ip, got, tc.prohibit)
		}
	}
}

func TestValidateURL(t *testing.T) {
	cases := []struct {
		rawURL  string
		wantErr bool
	}{
		{"https://example.com/media.mp4", false},
		{"http://example.com/image.jpg", false},
		{"file:///etc/passwd", true},
		{"ftp://example.com/file", true},
		{"gopher://example.com", true},
		{"", true},
		{"not-a-url", true},
	}

	for _, tc := range cases {
		_, err := ValidateURL(tc.rawURL)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateURL(%q) err = %v, wantErr = %v", tc.rawURL, err, tc.wantErr)
		}
	}
}

func TestSafeDownloader_BlocksLoopback(t *testing.T) {
	// Exercise the exact guard used by the production transport before a socket
	// is opened, making the SSRF test independent of network permissions.
	err := validateDialAddress("127.0.0.1:80")
	if err == nil {
		t.Fatal("expected loopback dial to be blocked by SSRF guard, but it succeeded")
	}
	if !errors.Is(err, ErrBlockedSSRF) {
		t.Errorf("expected ErrBlockedSSRF, got: %v", err)
	}
}

func TestSafeDownloader_SizeLimitExceeded(t *testing.T) {
	// Construct a client with a mock transport for size testing without network
	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "large.txt")

	dl := NewSafeDownloader(5*time.Second, 100)

	// Mock round tripper serving 500 bytes
	dl.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		data := make([]byte, 500)
		rec.Write(data)
		res := rec.Result()
		res.Request = req
		return res, nil
	})

	_, err := dl.Download(context.Background(), "https://example.com/large.bin", dst, 100)
	if err == nil {
		t.Fatalf("expected error exceeding 100 bytes limit, got nil")
	}
	if !errors.Is(err, core.ErrResourceLimit) {
		t.Errorf("expected ErrResourceLimit, got: %v", err)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSafeDownloader_MultiHopRedirectToPrivateIPBlocked(t *testing.T) {
	// Start a local listener on 127.0.0.1; the production transport's
	// Dialer.Control hook must block the redirect target before connecting.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	privURL := "http://" + ln.Addr().String() + "/secret"
	dl := NewSafeDownloader(5*time.Second, 1024)
	realTransport := dl.client.Transport

	hops := 0
	dl.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "public.example" {
			hops++
			rec := httptest.NewRecorder()
			if hops == 1 {
				rec.Header().Set("Location", "https://public.example/hop2")
			} else {
				rec.Header().Set("Location", privURL)
			}
			rec.WriteHeader(http.StatusFound)
			res := rec.Result()
			res.Request = req
			return res, nil
		}
		return realTransport.RoundTrip(req)
	})

	dst := filepath.Join(t.TempDir(), "ssrf.bin")
	_, dlErr := dl.Download(context.Background(), "https://public.example/start", dst, 1024)
	if !errors.Is(dlErr, ErrBlockedSSRF) {
		t.Fatalf("expected ErrBlockedSSRF after multi-hop redirect to %s, got: %v", privURL, dlErr)
	}
	if hops != 2 {
		t.Fatalf("expected 2 redirect hops before SSRF block, got %d", hops)
	}
}

func TestSafeDownloader_MismatchedContentLengthStillEnforcesMaxBytes(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "mismatch.bin")
	dl := NewSafeDownloader(5*time.Second, 100)
	dl.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Length", "10")
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.Write(make([]byte, 250))
		res := rec.Result()
		res.Request = req
		res.ContentLength = 10 // Lie about Content-Length while body has 250 bytes.
		return res, nil
	})

	_, err := dl.Download(context.Background(), "https://example.com/mismatch.bin", dst, 100)
	if !errors.Is(err, core.ErrResourceLimit) {
		t.Fatalf("expected ErrResourceLimit for mismatched Content-Length body, got: %v", err)
	}
}
