package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

type memItem struct {
	asset Asset
	data  []byte
}

type MemoryStorage struct {
	items map[string]*memItem
	usage atomic.Int64
	mu    sync.RWMutex
}

var _ Storage = (*MemoryStorage)(nil)

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{items: make(map[string]*memItem)}
}

func (m *MemoryStorage) BasePath() string { return "memory://" }

// Diagnostics returns an in-memory snapshot of memory storage consumption in O(1) without acquiring locks.
func (m *MemoryStorage) Diagnostics() Diagnostics {
	if m == nil {
		return Diagnostics{Backend: "nil"}
	}
	return Diagnostics{
		Backend: "memory",
		Usage:   m.Usage(),
	}
}

// Usage returns the current total stored bytes in memory in O(1) without acquiring locks.
func (m *MemoryStorage) Usage() int64 {
	if m == nil {
		return 0
	}
	u := m.usage.Load()
	if u < 0 {
		return 0
	}
	return u
}

// StorageHealth reports the in-memory health state of the storage owner without blocking I/O.
func (m *MemoryStorage) StorageHealth() (string, string) {
	return "healthy", ""
}

func (m *MemoryStorage) Put(ctx context.Context, src io.Reader, meta Metadata) (*Asset, error) {
	if src == nil {
		return nil, fmt.Errorf("source reader cannot be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("failed to read data: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}
	name := core.SanitizeFileName(meta.Name)
	if name == "" {
		name = "media.bin"
	}

	asset := Asset{
		ID: id, Name: name, MIME: meta.MIME, Title: meta.Title, Performer: meta.Performer,
		Size: int64(len(data)), Path: fmt.Sprintf("memory://%s/%s", id, name),
		Duration: meta.Duration, Width: meta.Width, Height: meta.Height, CreatedAt: time.Now().UTC(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if old, exists := m.items[id]; exists && old != nil {
		m.usage.Add(-int64(len(old.data)))
	}
	m.items[id] = &memItem{asset: asset, data: data}
	m.usage.Add(int64(len(data)))
	res := asset
	return &res, nil
}

func (m *MemoryStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, exists := m.items[id]
	if !exists {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(item.data)), nil
}

func (m *MemoryStorage) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.items[id]
	if !exists {
		return ErrNotFound
	}
	delete(m.items, id)
	if item != nil {
		m.usage.Add(-int64(len(item.data)))
	}
	return nil
}

func (m *MemoryStorage) Stat(ctx context.Context, id string) (*Asset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, exists := m.items[id]
	if !exists {
		return nil, ErrNotFound
	}
	res := item.asset
	return &res, nil
}
