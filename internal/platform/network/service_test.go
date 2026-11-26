package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/resource"
)

func TestNetworkService_RedactsResourceMetadataURL(t *testing.T) {
	var seenPathQuery string
	var captured []resource.Resource
	rm := resource.NewManager()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPathQuery = r.URL.RequestURI()
		captured = rm.ByOwner("plugin:test")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	svc := NewService(ts.Client(), rm)
	fullURL := ts.URL + "/download/secret-token?api_key=supersecret#access_token=fragsecret"
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := svc.Do(context.Background(), "plugin:test", req)
	if err != nil {
		t.Fatalf("unexpected Do error: %v", err)
	}
	defer resp.Body.Close()

	if seenPathQuery != "/download/secret-token?api_key=supersecret" {
		t.Fatalf("network operation must receive original URL, got %q", seenPathQuery)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 tracked resource during request, got %d", len(captured))
	}
	storedURL := captured[0].Metadata["url"]
	for _, secret := range []string{"secret-token", "supersecret", "fragsecret", "127.0.0.1"} {
		if strings.Contains(storedURL, secret) {
			t.Errorf("ResourceManager metadata leaked %q in %q", secret, storedURL)
		}
	}
}

func TestNetworkService_DoAndTracking(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	rm := resource.NewManager()
	svc := NewService(ts.Client(), rm)

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := svc.Do(context.Background(), "plugin:test", req)
	if err != nil {
		t.Fatalf("unexpected Do error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got: %d", resp.StatusCode)
	}

	// Should be released from manager upon return
	active := rm.ByOwner("plugin:test")
	if len(active) != 0 {
		t.Errorf("expected connection released from manager, got: %+v", active)
	}
}

func TestNetworkService_TimeoutAndCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	rm := resource.NewManager()
	svc := NewService(ts.Client(), rm)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := svc.Get(ctx, "plugin:test", ts.URL, nil)
	if err == nil {
		t.Fatal("expected error on timed out network call, got nil")
	}

	// Resource should still be cleaned up properly on timeout
	active := rm.ByOwner("plugin:test")
	if len(active) != 0 {
		t.Errorf("expected connection released from manager on timeout, got: %+v", active)
	}
}
