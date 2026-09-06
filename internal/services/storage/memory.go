package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

type memItem struct {
	asset Asset
	data  []byte
}

// MemoryStorage implements in-memory Storage for fast testing.
type MemoryStorage struct {
	items map[string]*memItem
	mu    sync.RWMutex
}

// Ensure MemoryStorage implements Storage.
var _ Storage = (*MemoryStorage)(nil)

// NewMemoryStorage creates a new in-memory storage manager.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		items: make(map[string]*memItem),
	}
}

// BasePath returns a virtual memory identifier.
func (m *MemoryStorage) BasePath() string {
	return "memory://"
}

// Put writes data into memory map.
func (m *MemoryStorage) Put(ctx context.Context, src io.Reader, meta Metadata) (*Asset, error) {
	if src == nil {
		return nil, fmt.Errorf("source reader cannot be nil")
	}

	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("failed to read data: %w", err)
	}

	id := generateID()
	name := core.SanitizeFileName(meta.Name)
	if name == "" {
		name = "media.bin"
	}

	asset := Asset{
		ID:        id,
		Name:      name,
		MIME:      meta.MIME,
		Size:      int64(len(data)),
		Path:      fmt.Sprintf("memory://%s/%s", id, name),
		Duration:  meta.Duration,
		Width:     meta.Width,
		Height:    meta.Height,
		CreatedAt: time.Now(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[id] = &memItem{
		asset: asset,
		data:  data,
	}

	res := asset
	return &res, nil
}

// Open returns a read closer over the in-memory data buffer.
func (m *MemoryStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, exists := m.items[id]
	if !exists {
		return nil, ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(item.data)), nil
}

// Delete removes the asset from memory.
func (m *MemoryStorage) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.items[id]; !exists {
		return ErrNotFound
	}
	delete(m.items, id)
	return nil
}

// Stat returns the asset descriptor.
func (m *MemoryStorage) Stat(ctx context.Context, id string) (*Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, exists := m.items[id]
	if !exists {
		return nil, ErrNotFound
	}

	res := item.asset
	return &res, nil
}
