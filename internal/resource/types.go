package resource

import (
	"time"
)

// Common resource types recognized across the runtime.
const (
	TypeSubscription = "subscription"
	TypeJob          = "job"
	TypeTask         = "task"
	TypeGoroutine    = "goroutine"
	TypeProcess      = "process"
	TypeTempFile     = "temp_file"
	TypeWebSocket    = "websocket"
	TypeLock         = "lock"
	TypeHTTPSession  = "http_session"
)

// ResourceState indicates the current lifecycle status of a tracked resource.
type ResourceState string

const (
	StateActive    ResourceState = "active"
	StateReleasing ResourceState = "releasing"
	StateReleased  ResourceState = "released"
	StateLeaked    ResourceState = "leaked"
)

// Resource defines a long-lived runtime resource owned by a subsystem or plugin.
type Resource struct {
	ID        string            `json:"id"`
	Owner     string            `json:"owner"` // e.g. "plugin:downloader", "runtime:scheduler"
	Type      string            `json:"type"`  // e.g. TypeTempFile, TypeProcess
	CreatedAt time.Time         `json:"created_at"`
	State     ResourceState     `json:"state"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// OwnerSnapshot groups active resource counts for a single owner.
type OwnerSnapshot struct {
	Owner        string         `json:"owner"`
	TotalActive  int            `json:"total_active"`
	CountsByType map[string]int `json:"counts_by_type"`
	Leaked       int            `json:"leaked"`
}
