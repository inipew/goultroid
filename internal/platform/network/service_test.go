package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
