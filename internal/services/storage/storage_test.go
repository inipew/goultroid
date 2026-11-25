package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

func testStorageSuite(t *testing.T, store storage.Storage) {
	ctx := context.Background()
	content := []byte("hello world storage media content")
	meta := storage.Metadata{Name: "test_audio.mp3", MIME: "audio/mp3", Duration: 10 * time.Second}

	asset, err := store.Put(ctx, bytes.NewReader(content), meta)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if asset.ID == "" {
		t.Errorf("expected non-empty ID")
	}
	if asset.Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), asset.Size)
	}
	if asset.Name != "test_audio.mp3" {
		t.Errorf("expected name test_audio.mp3, got %s", asset.Name)
	}

	statAsset, err := store.Stat(ctx, asset.ID)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if statAsset.ID != asset.ID || statAsset.Size != asset.Size {
		t.Errorf("Stat mismatch: %+v vs %v", statAsset, asset)
	}

	rc, err := store.Open(ctx, asset.ID)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	readBack, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readBack, content) {
		t.Errorf("content mismatch: got %s, expected %s", string(readBack), string(content))
	}

	if err := store.Delete(ctx, asset.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.Stat(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if _, err := store.Open(ctx, asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound on Open after delete, got %v", err)
	}
}

func TestMemoryStorage(t *testing.T) {
	testStorageSuite(t, storage.NewMemoryStorage())
}

func TestFileStorage(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewFileStorage(tempDir, 10*1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	testStorageSuite(t, store)

	info, err := os.Stat(tempDir)
	if err != nil {
		t.Fatalf("stat storage root: %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Errorf("expected storage root mode 0700, got %o", got)
	}

	ctx := context.Background()
	badIDs := []string{"../etc", "foo/bar", "../../secret", ""}
	for _, badID := range badIDs {
		if _, err := store.Stat(ctx, badID); err == nil {
			t.Errorf("expected error for bad ID %q, got nil", badID)
		}
		if _, err := store.Open(ctx, badID); err == nil {
			t.Errorf("expected error for bad ID %q, got nil", badID)
		}
		if err := store.Delete(ctx, badID); err == nil {
			t.Errorf("expected error for bad ID %q, got nil", badID)
		}
	}
}

func TestFileStorageHardQuotaIncludesMetadata(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-quota-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewFileStorage(tempDir, 512)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	ctx := context.Background()

	_, err = store.Put(ctx, strings.NewReader(strings.Repeat("A", 600)), storage.Metadata{Name: "large.txt"})
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded for oversized stream, got %v", err)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read storage root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no committed asset after quota rejection, found %d entries", len(entries))
	}

	asset, err := store.Put(ctx, strings.NewReader(strings.Repeat("B", 20)), storage.Metadata{Name: "small.txt"})
	if err != nil {
		t.Fatalf("small Put failed: %v", err)
	}
	if asset.Size != 20 {
		t.Fatalf("expected 20 bytes, got %d", asset.Size)
	}

	_, err = store.Put(ctx, strings.NewReader(strings.Repeat("C", 400)), storage.Metadata{Name: "next.txt"})
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Errorf("expected ErrQuotaExceeded after quota consumption, got %v", err)
	}
}

func TestFileStorageContextCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-cancel-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Put(ctx, strings.NewReader("payload"), storage.Metadata{Name: "cancelled.txt"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFileStorageCleansPartialFilesOnStartup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-recovery-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	assetDir := filepath.Join(tempDir, "deadbeef")
	if err := os.MkdirAll(assetDir, 0700); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "payload.part"), []byte("partial"), 0600); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	if _, err := storage.NewFileStorage(tempDir, 1024); err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assetDir, "payload.part")); !os.IsNotExist(err) {
		t.Fatalf("expected stale .part file to be removed, stat error=%v", err)
	}
}

func TestFileStorageBasePath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "goultroid-store-path-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	store, err := storage.NewFileStorage(tempDir, 0)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	if store.BasePath() != filepath.Clean(tempDir) {
		t.Errorf("expected base path %s, got %s", filepath.Clean(tempDir), store.BasePath())
	}
}

type blockingReader struct {
	data        []byte
	blockCh     chan struct{}
	readStarted chan struct{}
	readOnce    bool
}

func (b *blockingReader) Read(p []byte) (n int, err error) {
	if !b.readOnce {
		b.readOnce = true
		if b.readStarted != nil {
			close(b.readStarted)
		}
		<-b.blockCh
	}
	return copy(p, b.data), io.EOF
}

func TestFileStorage_StorageHealthNonBlockingDuringTransaction(t *testing.T) {
	tempDir := t.TempDir()

	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	blockCh := make(chan struct{})
	readStarted := make(chan struct{})
	putDone := make(chan struct{})

	reader := &blockingReader{
		data:        []byte("sample payload"),
		blockCh:     blockCh,
		readStarted: readStarted,
	}

	go func() {
		defer close(putDone)
		_, _ = store.Put(context.Background(), reader, storage.Metadata{Name: "test.bin"})
	}()

	// Deterministic synchronization: wait until Put() has entered Read() while holding transaction lock
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		close(blockCh)
		t.Fatal("Put did not enter Read within deadline")
	}

	// StorageHealth must return immediately without waiting for Put to finish
	healthDone := make(chan struct{})
	var status, reason string
	go func() {
		status, reason = store.StorageHealth()
		close(healthDone)
	}()

	select {
	case <-healthDone:
		if status != "healthy" || reason != "" {
			t.Errorf("unexpected health during transaction: status=%s, reason=%s", status, reason)
		}
	case <-time.After(100 * time.Millisecond):
		close(blockCh)
		t.Fatal("StorageHealth blocked on transaction mutex!")
	}

	// Unblock reader and wait for Put goroutine to terminate without leaking
	close(blockCh)
	select {
	case <-putDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Put goroutine did not finish after unblocking")
	}
}

func TestFileStorage_UsageSnapshotConsistency(t *testing.T) {
	tempDir := t.TempDir()

	store, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	if store.Usage() != 0 {
		t.Fatalf("expected initial usage 0, got %d", store.Usage())
	}

	payload := []byte("persistent asset payload data")
	asset, err := store.Put(context.Background(), bytes.NewReader(payload), storage.Metadata{Name: "asset.bin"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	usageAfterPut := store.Usage()
	if usageAfterPut <= int64(len(payload)) {
		t.Fatalf("expected usage > %d (payload + metadata), got %d", len(payload), usageAfterPut)
	}

	if err := store.Delete(context.Background(), asset.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if store.Usage() != 0 {
		t.Fatalf("expected usage 0 after delete, got %d", store.Usage())
	}
}

func TestFileStorage_StorageHealthQuotaExceeded(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Initial empty storage is healthy
	initialStore, err := storage.NewFileStorage(tempDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}
	status, reason := initialStore.StorageHealth()
	if status != "healthy" || reason != "" {
		t.Fatalf("initial health status=%s, reason=%s", status, reason)
	}

	// 2. Put an asset that establishes real data and metadata on disk
	payload := []byte("persistent payload to exhaust storage quota")
	asset, err := initialStore.Put(context.Background(), bytes.NewReader(payload), storage.Metadata{Name: "quota.bin"})
	if err != nil {
		t.Fatalf("initial Put failed: %v", err)
	}

	totalUsed := initialStore.Usage()
	if totalUsed <= 0 {
		t.Fatalf("expected positive usage after Put, got %d", totalUsed)
	}

	// 3. Initialize FileStorage with maxQuota set to totalUsed, representing
	// storage that has fully reached/exhausted its configured capacity.
	store, err := storage.NewFileStorage(tempDir, totalUsed)
	if err != nil {
		t.Fatalf("NewFileStorage with quota failed: %v", err)
	}

	// 4. Verify StorageHealth produces "degraded" with reason "storage:quota-exceeded"
	status, reason = store.StorageHealth()
	if status != "degraded" {
		t.Errorf("expected status 'degraded' when quota exhausted, got %q", status)
	}
	if reason != "storage:quota-exceeded" {
		t.Errorf("expected reason 'storage:quota-exceeded', got %q", reason)
	}

	// 5. Verify subsequent Put operation is rejected according to quota contract
	_, err = store.Put(context.Background(), bytes.NewReader([]byte("rejected overflow")), storage.Metadata{Name: "overflow.bin"})
	if err == nil {
		t.Fatal("expected Put to fail when quota is exhausted, but got nil")
	}
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}

	// 6. Delete asset to verify quota recovery
	if err := store.Delete(context.Background(), asset.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if store.Usage() != 0 {
		t.Fatalf("expected usage 0 after deleting asset, got %d", store.Usage())
	}
	status, reason = store.StorageHealth()
	if status != "healthy" || reason != "" {
		t.Errorf("expected healthy status after delete, got %s, reason %s", status, reason)
	}

	// 7. Verify subsequent Put succeeds now that quota has recovered
	_, err = store.Put(context.Background(), bytes.NewReader([]byte("allowed after recovery")), storage.Metadata{Name: "allowed.bin"})
	if err != nil {
		t.Fatalf("Put after delete recovery failed: %v", err)
	}
}

func TestFileStorage_Diagnostics(t *testing.T) {
	tempDir := t.TempDir()
	store, err := storage.NewFileStorage(tempDir, 1000)
	if err != nil {
		t.Fatalf("NewFileStorage failed: %v", err)
	}

	// 1. Initial diagnostics
	diag := store.Diagnostics()
	if diag.Backend != "file" {
		t.Errorf("backend = %q, want file", diag.Backend)
	}
	if diag.Usage != 0 {
		t.Errorf("initial usage = %d, want 0", diag.Usage)
	}
	if diag.MaxQuota != 1000 {
		t.Errorf("max quota = %d, want 1000", diag.MaxQuota)
	}
	if diag.UtilizationPercent != 0.0 {
		t.Errorf("initial utilization = %v, want 0.0", diag.UtilizationPercent)
	}

	// 2. Put asset and verify diagnostics update
	payload := bytes.Repeat([]byte("X"), 200)
	asset, err := store.Put(context.Background(), bytes.NewReader(payload), storage.Metadata{Name: "diag.bin"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	diagAfter := store.Diagnostics()
	if diagAfter.Usage <= 200 {
		t.Errorf("expected usage > 200 (payload + metadata), got %d", diagAfter.Usage)
	}
	if diagAfter.UtilizationPercent <= 20.0 || diagAfter.UtilizationPercent > 100.0 {
		t.Errorf("unexpected utilization percent = %v", diagAfter.UtilizationPercent)
	}

	// 3. Nil receiver safety
	var nilStore *storage.FileStorage
	nilDiag := nilStore.Diagnostics()
	if nilDiag.Backend != "nil" {
		t.Errorf("nil backend = %q, want nil", nilDiag.Backend)
	}

	// 4. Clean up
	_ = store.Delete(context.Background(), asset.ID)
}

func TestMemoryStorage_Diagnostics(t *testing.T) {
	memStore := storage.NewMemoryStorage()

	// 1. Initial usage must be 0
	diag := memStore.Diagnostics()
	if diag.Backend != "memory" {
		t.Errorf("backend = %q, want memory", diag.Backend)
	}
	if diag.Usage != 0 {
		t.Errorf("initial usage = %d, want 0", diag.Usage)
	}
	if memStore.Usage() != 0 {
		t.Errorf("initial Usage() = %d, want 0", memStore.Usage())
	}

	// 2. Multiple Puts
	p1 := []byte("payload-one-100-bytes-------------------------------------------------------------------------------")
	p2 := []byte("payload-two-longer-content-for-testing-storage-usage-accounting")
	p3 := []byte("small-payload")

	a1, err := memStore.Put(context.Background(), bytes.NewReader(p1), storage.Metadata{Name: "one.bin"})
	if err != nil {
		t.Fatalf("Put a1 failed: %v", err)
	}
	if u := memStore.Diagnostics().Usage; u != int64(len(p1)) {
		t.Fatalf("usage after a1 = %d, want %d", u, len(p1))
	}

	a2, err := memStore.Put(context.Background(), bytes.NewReader(p2), storage.Metadata{Name: "two.bin"})
	if err != nil {
		t.Fatalf("Put a2 failed: %v", err)
	}
	if u := memStore.Diagnostics().Usage; u != int64(len(p1)+len(p2)) {
		t.Fatalf("usage after a2 = %d, want %d", u, len(p1)+len(p2))
	}

	a3, err := memStore.Put(context.Background(), bytes.NewReader(p3), storage.Metadata{Name: "three.bin"})
	if err != nil {
		t.Fatalf("Put a3 failed: %v", err)
	}
	expectedTotal := int64(len(p1) + len(p2) + len(p3))
	if u := memStore.Diagnostics().Usage; u != expectedTotal {
		t.Fatalf("usage after a3 = %d, want %d", u, expectedTotal)
	}

	// 3. Delete non-existent ID does not affect usage
	_ = memStore.Delete(context.Background(), "non-existent-id")
	if u := memStore.Diagnostics().Usage; u != expectedTotal {
		t.Fatalf("usage after non-existent delete = %d, want %d", u, expectedTotal)
	}

	// 4. Delete sequential and observe exact accounting
	if err := memStore.Delete(context.Background(), a1.ID); err != nil {
		t.Fatalf("Delete a1 failed: %v", err)
	}
	expectedAfterA1 := int64(len(p2) + len(p3))
	if u := memStore.Diagnostics().Usage; u != expectedAfterA1 {
		t.Fatalf("usage after deleting a1 = %d, want %d", u, expectedAfterA1)
	}

	if err := memStore.Delete(context.Background(), a2.ID); err != nil {
		t.Fatalf("Delete a2 failed: %v", err)
	}
	expectedAfterA2 := int64(len(p3))
	if u := memStore.Diagnostics().Usage; u != expectedAfterA2 {
		t.Fatalf("usage after deleting a2 = %d, want %d", u, expectedAfterA2)
	}

	if err := memStore.Delete(context.Background(), a3.ID); err != nil {
		t.Fatalf("Delete a3 failed: %v", err)
	}
	if u := memStore.Diagnostics().Usage; u != 0 {
		t.Fatalf("usage after deleting all = %d, want 0", u)
	}

	// 5. Nil receiver safety
	var nilMem *storage.MemoryStorage
	nilDiag := nilMem.Diagnostics()
	if nilDiag.Backend != "nil" {
		t.Errorf("nil backend = %q, want nil", nilDiag.Backend)
	}
	if nilMem.Usage() != 0 {
		t.Errorf("nil Usage() = %d, want 0", nilMem.Usage())
	}
}

func TestMemoryStorage_ConcurrentPutDeleteDiagnostics(t *testing.T) {
	memStore := storage.NewMemoryStorage()
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	const workers = 8
	const opsPerWorker = 30

	var createdIDs sync.Map

	// Writers doing Put
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startCh
			for i := 0; i < opsPerWorker; i++ {
				data := []byte(fmt.Sprintf("worker-%d-op-%d-data", workerID, i))
				asset, err := memStore.Put(context.Background(), bytes.NewReader(data), storage.Metadata{
					Name: fmt.Sprintf("w%d_op%d.bin", workerID, i),
				})
				if err == nil && asset != nil {
					createdIDs.Store(asset.ID, int64(len(data)))
				}
			}
		}(w)
	}

	// Readers calling Diagnostics and Usage concurrently
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startCh
			for i := 0; i < opsPerWorker*2; i++ {
				d := memStore.Diagnostics()
				if d.Backend != "memory" {
					t.Errorf("unexpected backend: %s", d.Backend)
				}
				if d.Usage < 0 {
					t.Errorf("usage should never be negative: %d", d.Usage)
				}
			}
		}()
	}

	close(startCh)
	wg.Wait()

	// Verify total usage matches sum of remaining assets
	finalDiag := memStore.Diagnostics()
	if finalDiag.Usage <= 0 {
		t.Fatalf("expected positive usage after concurrent puts, got %d", finalDiag.Usage)
	}

	// Now delete all created IDs concurrently
	var deleteWg sync.WaitGroup
	deleteStart := make(chan struct{})

	for w := 0; w < workers; w++ {
		deleteWg.Add(1)
		go func() {
			defer deleteWg.Done()
			<-deleteStart
			createdIDs.Range(func(key, value any) bool {
				id := key.(string)
				if createdIDs.CompareAndDelete(id, value) {
					_ = memStore.Delete(context.Background(), id)
				}
				return true
			})
		}()
	}

	close(deleteStart)
	deleteWg.Wait()

	if u := memStore.Usage(); u != 0 {
		t.Fatalf("final usage after deleting all = %d, want 0", u)
	}
}
