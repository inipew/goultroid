package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const enumerationReadChunk = 128

func normalizeEnumerationLimit(limit int) int {
	if limit <= 0 {
		return DefaultEnumerationLimit
	}
	return min(limit, MaxEnumerationLimit)
}

func insertBoundedKey(keys []string, key, cursor string, capacity int) []string {
	if capacity <= 0 || key <= cursor {
		return keys
	}
	idx := sort.SearchStrings(keys, key)
	if idx < len(keys) && keys[idx] == key {
		return keys
	}
	if len(keys) >= capacity && idx >= capacity {
		return keys
	}

	keys = append(keys, "")
	copy(keys[idx+1:], keys[idx:])
	keys[idx] = key
	if len(keys) > capacity {
		keys = keys[:capacity]
	}
	return keys
}

func pageCursor(keys []string, limit int) ([]string, string) {
	if len(keys) <= limit {
		return keys, ""
	}
	return keys[:limit], keys[limit-1]
}

func (f *FileStorage) Enumerate(ctx context.Context, opts EnumerationOptions) (EnumerationPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EnumerationPage{}, err
	}
	limit := normalizeEnumerationLimit(opts.Limit)
	cursor := opts.Cursor

	dir, err := os.Open(f.baseDir)
	if err != nil {
		return EnumerationPage{}, fmt.Errorf("storage: open root for enumeration: %w", err)
	}
	defer dir.Close()

	keys := make([]string, 0, limit+1)
	for {
		if err := ctx.Err(); err != nil {
			return EnumerationPage{}, err
		}
		entries, readErr := dir.ReadDir(enumerationReadChunk)
		for _, entry := range entries {
			keys = insertBoundedKey(keys, entry.Name(), cursor, limit+1)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return EnumerationPage{}, fmt.Errorf("storage: enumerate root: %w", readErr)
		}
	}

	keys, next := pageCursor(keys, limit)
	page := EnumerationPage{Entries: make([]EnumerationEntry, 0, len(keys)), NextCursor: next}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return EnumerationPage{}, err
		}
		entry, err := f.enumerateEntry(ctx, key)
		if err != nil {
			return EnumerationPage{}, err
		}
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

func (f *FileStorage) enumerateEntry(ctx context.Context, key string) (EnumerationEntry, error) {
	path := filepath.Join(f.baseDir, key)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EnumerationEntry{Key: key, State: EnumerationMalformed, Detail: "entry_disappeared"}, nil
		}
		return EnumerationEntry{}, fmt.Errorf("storage: inspect enumerated entry %q: %w", key, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EnumerationEntry{Key: key, State: EnumerationMalformed, Detail: "symlink_root_entry"}, nil
	}
	if !info.IsDir() {
		return EnumerationEntry{
			Key: key, State: EnumerationUnmanaged, Size: info.Size(), Detail: "unmanaged_root_file",
		}, nil
	}
	if !validIDRegex.MatchString(key) {
		return EnumerationEntry{Key: key, State: EnumerationUnmanaged, Detail: "unmanaged_root_directory"}, nil
	}

	asset, err := f.Stat(ctx, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidPath) {
			return EnumerationEntry{Key: key, State: EnumerationMalformed, Detail: "invalid_asset_directory"}, nil
		}
		return EnumerationEntry{}, fmt.Errorf("storage: inspect asset %q during enumeration: %w", key, err)
	}
	if asset == nil || strings.TrimSpace(asset.ID) != key {
		return EnumerationEntry{Key: key, State: EnumerationMalformed, Detail: "asset_id_mismatch"}, nil
	}
	copyAsset := *asset
	return EnumerationEntry{Key: key, State: EnumerationManaged, Asset: &copyAsset, Size: copyAsset.Size}, nil
}

func (m *MemoryStorage) Enumerate(ctx context.Context, opts EnumerationOptions) (EnumerationPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EnumerationPage{}, err
	}
	limit := normalizeEnumerationLimit(opts.Limit)
	cursor := opts.Cursor

	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]string, 0, limit+1)
	for key := range m.items {
		keys = insertBoundedKey(keys, key, cursor, limit+1)
	}
	keys, next := pageCursor(keys, limit)
	page := EnumerationPage{Entries: make([]EnumerationEntry, 0, len(keys)), NextCursor: next}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return EnumerationPage{}, err
		}
		item := m.items[key]
		if item == nil {
			page.Entries = append(page.Entries, EnumerationEntry{
				Key: key, State: EnumerationMalformed, Detail: "nil_memory_item",
			})
			continue
		}
		asset := item.asset
		if strings.TrimSpace(asset.ID) != key {
			page.Entries = append(page.Entries, EnumerationEntry{
				Key: key, State: EnumerationMalformed, Detail: "asset_id_mismatch",
			})
			continue
		}
		page.Entries = append(page.Entries, EnumerationEntry{
			Key: key, State: EnumerationManaged, Asset: &asset, Size: asset.Size,
		})
	}
	return page, nil
}
