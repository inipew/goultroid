package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/resource"
)

// HTTP method constants to prevent plugins from importing net/http.
const (
	MethodGet     = http.MethodGet
	MethodPost    = http.MethodPost
	MethodPut     = http.MethodPut
	MethodDelete  = http.MethodDelete
	MethodHead    = http.MethodHead
	MethodPatch   = http.MethodPatch
	MethodOptions = http.MethodOptions
)

// HTTP status code constants to prevent plugins from importing net/http.
const (
	StatusOK                  = http.StatusOK
	StatusCreated             = http.StatusCreated
	StatusAccepted            = http.StatusAccepted
	StatusNoContent           = http.StatusNoContent
	StatusBadRequest          = http.StatusBadRequest
	StatusUnauthorized        = http.StatusUnauthorized
	StatusForbidden           = http.StatusForbidden
	StatusNotFound            = http.StatusNotFound
	StatusTooManyRequests     = http.StatusTooManyRequests
	StatusInternalServerError = http.StatusInternalServerError
	StatusBadGateway          = http.StatusBadGateway
	StatusServiceUnavailable  = http.StatusServiceUnavailable
	StatusGatewayTimeout      = http.StatusGatewayTimeout
)

var (
	ErrNetworkTimeout = errors.New("network request timed out")
)

// Response wraps an HTTP response providing convenient decoding and memory safety.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
	raw        *http.Response
}

// Close closes the underlying response body.
func (r *Response) Close() error {
	if r.Body != nil {
		return r.Body.Close()
	}
	return nil
}

// Bytes reads and returns the entire response body, closing it upon completion.
func (r *Response) Bytes() ([]byte, error) {
	defer r.Close()
	return io.ReadAll(r.Body)
}

// JSON decodes the response body into target v, closing it upon completion.
func (r *Response) JSON(v any) error {
	defer r.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

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

// DoRequest executes an HTTP request specified by method, url, body, and headers without requiring net/http in caller.
func (s *Service) DoRequest(ctx context.Context, owner, method, urlStr string, body io.Reader, headers map[string]string) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := s.Do(ctx, owner, req)
	if err != nil {
		return nil, err
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
		raw:        resp,
	}, nil
}

// Get performs a managed GET request.
func (s *Service) Get(ctx context.Context, owner, urlStr string, headers map[string]string) (*Response, error) {
	return s.DoRequest(ctx, owner, MethodGet, urlStr, nil, headers)
}

// Post performs a managed POST request with the given content-type and body.
func (s *Service) Post(ctx context.Context, owner, urlStr, contentType string, body io.Reader, headers map[string]string) (*Response, error) {
	h := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		h[k] = v
	}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	return s.DoRequest(ctx, owner, MethodPost, urlStr, body, h)
}

// GetJSON performs a GET request and decodes the JSON body directly into target.
func (s *Service) GetJSON(ctx context.Context, owner, urlStr string, target any) error {
	resp, err := s.Get(ctx, owner, urlStr, map[string]string{"Accept": "application/json"})
	if err != nil {
		return err
	}
	defer resp.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("http request returned status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	return resp.JSON(target)
}
