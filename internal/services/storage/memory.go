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

type MemoryStorage struct {
	items map[string]*memItem
	mu    sync.RWMutex
}

var _ Storage = (*MemoryStorage)(nil)

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{items: make(map[string]*memItem)}
}

func (m *MemoryStorage) BasePath() string { return "memory://" }

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
		ID: id, Name: name, MIME: meta.MIME, Size: int64(len(data)),
		Path: fmt.Sprintf("memory://%s/%s", id, name), Duration: meta.Duration,
		Width: meta.Width, Height: meta.Height, CreatedAt: time.Now().UTC(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[id] = &memItem{asset: asset, data: data}
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
	if _, exists := m.items[id]; !exists {
		return ErrNotFound
	}
	delete(m.items, id)
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
