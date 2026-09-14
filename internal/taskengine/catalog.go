package taskengine

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/tasks"
)

// ValidationCode is a stable machine-readable reason for rejecting a WorkSpec
// before it can enter the TaskEngine registry.
type ValidationCode string

const (
	ValidationUnknownPool      ValidationCode = "unknown_pool"
	ValidationUnknownHandler   ValidationCode = "unknown_handler"
	ValidationUnknownPayload   ValidationCode = "unknown_payload_version"
	ValidationPayloadTooLarge  ValidationCode = "payload_too_large"
	ValidationClassNotAllowed  ValidationCode = "class_not_allowed"
	ValidationPoolNotAllowed   ValidationCode = "pool_not_allowed"
	ValidationUnknownResource  ValidationCode = "unknown_resource"
	ValidationResourceTooLarge ValidationCode = "resource_exceeds_capacity"
)

// ValidationError describes a catalog/policy validation failure without
// requiring callers to parse an error string.
type ValidationError struct {
	Code ValidationCode
}

func (e *ValidationError) Error() string { return fmt.Sprintf("invalid work spec: %s", e.Code) }

// HandlerDescriptor contains only metadata. The executable handler function is
// intentionally not part of the P1 model and will be owned by TaskEngine in P2.
type HandlerDescriptor struct {
	Ref             tasks.HandlerRef
	PayloadKind     string
	PayloadVersions []uint16
	MaxPayloadBytes int
	AllowedPools    []tasks.PoolID
	AllowedClasses  []tasks.PriorityClass
}

type handlerKey struct {
	name    string
	version uint16
}

type handlerMetadata struct {
	payloadKind     string
	payloadVersions map[uint16]struct{}
	maxPayloadBytes int
	allowedPools    map[tasks.PoolID]struct{}
	allowedClasses  map[tasks.PriorityClass]struct{}
}

// Catalog is immutable from the perspective of Submit after startup. Register
// is concurrency-safe so composition code can build it deterministically before
// TaskEngine starts without leaking executable callbacks into WorkSpec.
type Catalog struct {
	mu        sync.RWMutex
	pools     map[tasks.PoolID]struct{}
	resources map[string]uint32
	handlers  map[handlerKey]handlerMetadata
}

func NewCatalog(cfg Config) (*Catalog, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	catalog := &Catalog{
		pools:     make(map[tasks.PoolID]struct{}, len(cfg.Pools)),
		resources: make(map[string]uint32, len(cfg.ResourceCapacity)),
		handlers:  make(map[handlerKey]handlerMetadata),
	}
	for pool := range cfg.Pools {
		catalog.pools[pool] = struct{}{}
	}
	for name, units := range cfg.ResourceCapacity {
		catalog.resources[name] = units
	}
	return catalog, nil
}

func (c *Catalog) RegisterHandler(d HandlerDescriptor) error {
	if d.Ref.IsZero() || strings.TrimSpace(d.PayloadKind) == "" || len(d.PayloadVersions) == 0 || d.MaxPayloadBytes <= 0 {
		return errors.New("handler descriptor is invalid")
	}
	metadata := handlerMetadata{
		payloadKind:     strings.TrimSpace(d.PayloadKind),
		payloadVersions: make(map[uint16]struct{}, len(d.PayloadVersions)),
		maxPayloadBytes: d.MaxPayloadBytes,
		allowedPools:    make(map[tasks.PoolID]struct{}, len(d.AllowedPools)),
		allowedClasses:  make(map[tasks.PriorityClass]struct{}, len(d.AllowedClasses)),
	}
	for _, version := range d.PayloadVersions {
		if version == 0 {
			return errors.New("handler payload version must be positive")
		}
		metadata.payloadVersions[version] = struct{}{}
	}
	for _, pool := range d.AllowedPools {
		if strings.TrimSpace(string(pool)) == "" {
			return errors.New("handler allowed pool cannot be empty")
		}
		if _, ok := c.pools[pool]; !ok {
			return fmt.Errorf("handler references unknown pool %q", pool)
		}
		metadata.allowedPools[pool] = struct{}{}
	}
	for _, class := range d.AllowedClasses {
		if !class.Valid() {
			return errors.New("handler allowed class is invalid")
		}
		metadata.allowedClasses[class] = struct{}{}
	}
	if len(metadata.allowedPools) == 0 || len(metadata.allowedClasses) == 0 {
		return errors.New("handler must allow at least one pool and priority class")
	}

	key := handlerKey{name: d.Ref.Name(), version: d.Ref.Version()}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.handlers[key]; exists {
		return fmt.Errorf("handler already registered: %s@%d", d.Ref.Name(), d.Ref.Version())
	}
	c.handlers[key] = metadata
	return nil
}

// ValidateSpec validates references whose existence is a runtime composition
// concern. Syntactic identity/class/resource checks remain in tasks.NewWorkSpec.
func (c *Catalog) ValidateSpec(spec tasks.WorkSpec) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if _, ok := c.pools[spec.Pool()]; !ok {
		return &ValidationError{Code: ValidationUnknownPool}
	}
	ref := spec.Handler()
	metadata, ok := c.handlers[handlerKey{name: ref.Name(), version: ref.Version()}]
	if !ok {
		return &ValidationError{Code: ValidationUnknownHandler}
	}
	input := spec.Input()
	if input.Kind() != metadata.payloadKind {
		return &ValidationError{Code: ValidationUnknownPayload}
	}
	if _, ok := metadata.payloadVersions[input.Version()]; !ok {
		return &ValidationError{Code: ValidationUnknownPayload}
	}
	if input.Size() > metadata.maxPayloadBytes {
		return &ValidationError{Code: ValidationPayloadTooLarge}
	}
	if _, ok := metadata.allowedPools[spec.Pool()]; !ok {
		return &ValidationError{Code: ValidationPoolNotAllowed}
	}
	if _, ok := metadata.allowedClasses[spec.Class()]; !ok {
		return &ValidationError{Code: ValidationClassNotAllowed}
	}
	for _, request := range spec.Resources() {
		capacity, ok := c.resources[request.Name()]
		if !ok {
			return &ValidationError{Code: ValidationUnknownResource}
		}
		if request.Units() > capacity {
			return &ValidationError{Code: ValidationResourceTooLarge}
		}
	}
	return nil
}
