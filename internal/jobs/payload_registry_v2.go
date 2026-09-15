package jobs

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/tasks"
)

// PayloadDescriptor defines one accepted durable payload schema version.
type PayloadDescriptor struct {
	Kind     string
	Version  uint16
	MaxBytes int
}

type payloadKey struct {
	kind    string
	version uint16
}

// PayloadRegistry validates versioned durable payload envelopes. It stores no
// feature handler and performs no I/O.
type PayloadRegistry struct {
	mu      sync.RWMutex
	entries map[payloadKey]PayloadDescriptor
}

func NewPayloadRegistry() *PayloadRegistry {
	return &PayloadRegistry{entries: make(map[payloadKey]PayloadDescriptor)}
}

func (r *PayloadRegistry) Register(d PayloadDescriptor) error {
	d.Kind = strings.TrimSpace(d.Kind)
	if d.Kind == "" || d.Version == 0 || d.MaxBytes < 0 {
		return errors.New("payload descriptor is invalid")
	}
	key := payloadKey{kind: d.Kind, version: d.Version}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[key]; exists {
		return fmt.Errorf("payload schema already registered: %s@%d", d.Kind, d.Version)
	}
	r.entries[key] = d
	return nil
}

func (r *PayloadRegistry) Validate(p tasks.PayloadRef) error {
	if p.IsZero() {
		return errors.New("payload reference is required")
	}
	key := payloadKey{kind: p.Kind(), version: p.Version()}
	r.mu.RLock()
	d, ok := r.entries[key]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown payload schema: %s@%d", p.Kind(), p.Version())
	}
	if d.MaxBytes > 0 && p.Size() > d.MaxBytes {
		return fmt.Errorf("payload exceeds schema limit: %d > %d", p.Size(), d.MaxBytes)
	}
	return nil
}
