package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/resource"
)

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
