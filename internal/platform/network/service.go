package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/resource"
)

var (
	ErrNetworkTimeout = errors.New("network request timed out")
)

// Service coordinates managed network and HTTP requests for plugins,
// tracking active outbound connections and enforcing default timeouts.
type Service struct {
	client      *http.Client
	resourceMgr *resource.Manager
	reqCounter  atomic.Uint64
}

// NewService creates a managed network service with custom or default HTTP client.
func NewService(client *http.Client, rm *resource.Manager) *Service {
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}

	return &Service{
		client:      client,
		resourceMgr: rm,
	}
}

// Do executes an HTTP request on behalf of an owner, tracking the connection
// in the ResourceManager until completion.
func (s *Service) Do(ctx context.Context, owner string, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("http request cannot be nil")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	req = req.WithContext(ctx)

	reqID := fmt.Sprintf("http:%s:%d", owner, s.reqCounter.Add(1))
	if s.resourceMgr != nil {
		_ = s.resourceMgr.Register(resource.Resource{
			ID:        reqID,
			Owner:     owner,
			Type:      resource.TypeHTTPSession,
			CreatedAt: time.Now().UTC(),
			Metadata: map[string]string{
				"url":    req.URL.String(),
				"method": req.Method,
			},
		})
		defer func() {
			_ = s.resourceMgr.Release(reqID)
		}()
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrNetworkTimeout
		}
		return nil, err
	}

	return resp, nil
}
