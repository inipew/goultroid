package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrNotFound indicates that the requested asset does not exist in storage.
	ErrNotFound = errors.New("storage: asset not found")
	// ErrInvalidPath indicates an invalid or dangerous storage path (e.g. traversal attempt).
	ErrInvalidPath = errors.New("storage: invalid path or directory traversal detected")
	// ErrQuotaExceeded indicates that storage capacity or directory quota has been exceeded.
	ErrQuotaExceeded = errors.New("storage: quota exceeded")
)

const (
	DefaultEnumerationLimit = 64
	MaxEnumerationLimit     = 256
)

// Metadata provides descriptive metadata when storing a new asset.
type Metadata struct {
	Name     string        `json:"name"`
	MIME     string        `json:"mime"`
	Duration time.Duration `json:"duration,omitempty"`
	Width    int           `json:"width,omitempty"`
	Height   int           `json:"height,omitempty"`
}

// Asset represents a stored media or data file.
type Asset struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	MIME      string        `json:"mime"`
	Size      int64         `json:"size"`
	Path      string        `json:"path"`
	Duration  time.Duration `json:"duration,omitempty"`
	Width     int           `json:"width,omitempty"`
	Height    int           `json:"height,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

// EnumerationState describes how a physical storage root entry relates to the
// managed asset layout. Enumeration never mutates or reclaims an entry.
type EnumerationState string

const (
	EnumerationManaged   EnumerationState = "managed"
	EnumerationUnmanaged EnumerationState = "unmanaged"
	EnumerationMalformed EnumerationState = "malformed"
)

// EnumerationEntry is one physical root entry discovered during a bounded
// storage scan. Asset is populated only for managed entries. Detail is a
// stable, machine-readable reason for unmanaged or malformed entries.
type EnumerationEntry struct {
	Key    string           `json:"key"`
	State  EnumerationState `json:"state"`
	Asset  *Asset           `json:"asset,omitempty"`
	Size   int64            `json:"size,omitempty"`
	Detail string           `json:"detail,omitempty"`
}

// EnumerationOptions controls one bounded page. Cursor is an opaque value
// returned by the previous page and Limit is clamped to MaxEnumerationLimit.
type EnumerationOptions struct {
	Cursor string
	Limit  int
}

// EnumerationPage is a bounded physical-storage page. NextCursor is empty
// when the current scan reached the end of the storage root.
type EnumerationPage struct {
	Entries    []EnumerationEntry
	NextCursor string
}

// Storage provides an abstraction for asset persistence.
type Storage interface {
	// Put writes the stream from src to storage under a generated asset ID.
	Put(ctx context.Context, src io.Reader, meta Metadata) (*Asset, error)
	// Open opens the asset data stream for reading.
	Open(ctx context.Context, id string) (io.ReadCloser, error)
	// Delete removes the asset from storage.
	Delete(ctx context.Context, id string) error
	// Stat returns the Asset descriptor without opening the stream.
	Stat(ctx context.Context, id string) (*Asset, error)
	// Enumerate returns one bounded, non-destructive page of physical entries.
	Enumerate(ctx context.Context, opts EnumerationOptions) (EnumerationPage, error)
	// BasePath returns the underlying base storage directory (if filesystem-backed).
	BasePath() string
}
