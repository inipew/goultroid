package telegram

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

type mediaCountingLimiter struct {
	mu      sync.Mutex
	calls   int
	methods map[string]int
}

func (l *mediaCountingLimiter) Reserve(_ time.Time, dimensions []LimitKey, _ int) Reservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.methods == nil {
		l.methods = make(map[string]int)
	}
	for _, dimension := range dimensions {
		if dimension.Scope == "method" {
			l.methods[dimension.Key]++
		}
	}
	return Reservation{Allowed: true}
}

func (*mediaCountingLimiter) Penalize(time.Time, []LimitKey, time.Duration) {}

func (l *mediaCountingLimiter) methodCalls(method string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.methods[method]
}

type fakeManagedUploadRaw struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeManagedUploadRaw) UploadSaveFilePart(context.Context, *tg.UploadSaveFilePartRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return true, nil
}

func (f *fakeManagedUploadRaw) UploadSaveBigFilePart(context.Context, *tg.UploadSaveBigFilePartRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return true, nil
}

type fakeManagedDownloadRaw struct {
	mu       sync.Mutex
	getCalls int
}

func (f *fakeManagedDownloadRaw) UploadGetFile(context.Context, *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	f.mu.Lock()
	f.getCalls++
	f.mu.Unlock()
	return &tg.UploadFile{Bytes: []byte("chunk")}, nil
}
func (*fakeManagedDownloadRaw) UploadGetFileHashes(context.Context, *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}
func (*fakeManagedDownloadRaw) UploadReuploadCDNFile(context.Context, *tg.UploadReuploadCDNFileRequest) ([]tg.FileHash, error) {
	return nil, nil
}
func (*fakeManagedDownloadRaw) UploadGetCDNFileHashes(context.Context, *tg.UploadGetCDNFileHashesRequest) ([]tg.FileHash, error) {
	return nil, nil
}
func (*fakeManagedDownloadRaw) UploadGetWebFile(context.Context, *tg.UploadGetWebFileRequest) (*tg.UploadWebFile, error) {
	return &tg.UploadWebFile{}, nil
}

func newMediaTestExecutor(t *testing.T, limiter RPCRequestLimiter, metrics RPCMetrics) *RPCExecutor {
	t.Helper()
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter: limiter,
		Metrics: metrics,
		DefaultPolicy: RetryPolicy{
			MaxAttempts:        1,
			BaseDelay:          time.Millisecond,
			MaxDelay:           time.Millisecond,
			MaxElapsed:         time.Second,
			InlineFloodWaitMax: time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func TestManagedUploadRPCCountsEveryPhysicalPart(t *testing.T) {
	limiter := &mediaCountingLimiter{}
	metrics := NewInMemoryRPCMetrics()
	exec := newMediaTestExecutor(t, limiter, metrics)
	raw := &fakeManagedUploadRaw{}
	client := &managedUploadRPCClient{raw: raw, executor: func() *RPCExecutor { return exec }}

	for part := 0; part < 2; part++ {
		ok, err := client.UploadSaveFilePart(context.Background(), &tg.UploadSaveFilePartRequest{FileID: 1, FilePart: part, Bytes: []byte("part")})
		if err != nil || !ok {
			t.Fatalf("part %d: ok=%v err=%v", part, ok, err)
		}
	}

	if got := limiter.methodCalls("upload.saveFilePart"); got != 2 {
		t.Fatalf("physical upload parts reserved %d times, want 2", got)
	}
	if got := metrics.Snapshot().RequestsByMethod["upload.saveFilePart"]; got != 2 {
		t.Fatalf("physical upload metrics=%d, want 2", got)
	}
}

func TestManagedDownloadRPCCountsEveryPhysicalChunk(t *testing.T) {
	limiter := &mediaCountingLimiter{}
	metrics := NewInMemoryRPCMetrics()
	exec := newMediaTestExecutor(t, limiter, metrics)
	raw := &fakeManagedDownloadRaw{}
	client := &managedDownloadRPCClient{raw: raw, executor: func() *RPCExecutor { return exec }}

	for i := 0; i < 2; i++ {
		if _, err := client.UploadGetFile(context.Background(), &tg.UploadGetFileRequest{Offset: int64(i * 4), Limit: 4}); err != nil {
			t.Fatal(err)
		}
	}

	if got := limiter.methodCalls("upload.getFile"); got != 2 {
		t.Fatalf("physical download chunks reserved %d times, want 2", got)
	}
	if got := metrics.Snapshot().RequestsByMethod["upload.getFile"]; got != 2 {
		t.Fatalf("physical download metrics=%d, want 2", got)
	}
}

type mediaTimeoutError struct{}

func (mediaTimeoutError) Error() string   { return "network timeout" }
func (mediaTimeoutError) Timeout() bool   { return true }
func (mediaTimeoutError) Temporary() bool { return true }

func TestMediaRPCBoundaryHidesFinalRetryCauseFromGotd(t *testing.T) {
	exec := newMediaTestExecutor(t, nil, nil)
	raw := &fakeManagedUploadRaw{err: mediaTimeoutError{}}
	client := &managedUploadRPCClient{raw: raw, executor: func() *RPCExecutor { return exec }}

	_, err := client.UploadSaveFilePart(context.Background(), &tg.UploadSaveFilePartRequest{FileID: 1, FilePart: 0, Bytes: []byte("part")})
	if err == nil {
		t.Fatal("expected error")
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		t.Fatalf("managed boundary leaked net.Error to gotd retry loop: %v", err)
	}
	cause := unwrapMediaRPCBoundary(err)
	if !errors.As(cause, &netErr) {
		t.Fatalf("Goultroid boundary could not recover original timeout cause: %v", cause)
	}
}
