package resource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

type LeakPolicy string

const (
	LeakPolicyWarn         LeakPolicy = "warn"
	LeakPolicyDegraded     LeakPolicy = "degraded"
	LeakPolicyForceCleanup LeakPolicy = "force_cleanup"
)

// Manager tracks, inspects, and audits the ownership and lifecycle of long-lived resources.
const DefaultMaxTrackedResources = 16 * 1024

// CleanupFunc is a bounded force-cleanup callback. Implementations should honor
// ctx cancellation; Manager also enforces the caller deadline around callbacks.
type CleanupFunc func(context.Context) error

type cleanupRun struct {
	done chan struct{}
	err  error
}

type Manager struct {
	mu           sync.RWMutex
	policy       LeakPolicy
	maxResources int
	resources    map[string]Resource
	cleanups        map[string]CleanupFunc
	cleanupRuns     map[string]*cleanupRun
	cleanupExecutor *runtime.CallbackExecutor
}

// NewManager initializes a new thread-safe resource manager with a hard
// cardinality bound so resource tracking itself cannot become an unbounded leak.
func NewManager() *Manager {
	return NewManagerWithLimit(DefaultMaxTrackedResources)
}

func NewManagerWithLimit(maxResources int) *Manager {
	if maxResources <= 0 {
		maxResources = DefaultMaxTrackedResources
	}
	return &Manager{
		policy:       LeakPolicyWarn,
		maxResources: maxResources,
		resources:       make(map[string]Resource),
		cleanups:        make(map[string]CleanupFunc),
		cleanupRuns:     make(map[string]*cleanupRun),
		cleanupExecutor: runtime.NewCallbackExecutor(runtime.DefaultLifecycleCallbackConcurrency),
	}
}

// SetCleanupExecutor shares the application lifecycle callback budget with
// resource force-cleanup.
func (m *Manager) SetCleanupExecutor(executor *runtime.CallbackExecutor) {
	if executor == nil {
		return
	}
	m.mu.Lock()
	m.cleanupExecutor = executor
	m.mu.Unlock()
}

// SetLeakPolicy sets the policy applied when leaks are detected.
func (m *Manager) SetLeakPolicy(p LeakPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = p
}

// LeakPolicy returns the active leak policy.
func (m *Manager) LeakPolicy() LeakPolicy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policy
}

// Register adds a resource to tracking. Every resource must have an ID, Owner, and Type.
func (m *Manager) Register(r Resource) error {
	return m.RegisterWithCleanup(r, nil)
}

// RegisterWithCleanup adds a resource with a legacy contextless force-cleanup
// callback. New callers should prefer RegisterWithCleanupContext.
func (m *Manager) RegisterWithCleanup(r Resource, cleanup func() error) error {
	if cleanup == nil {
		return m.RegisterWithCleanupContext(r, nil)
	}
	return m.RegisterWithCleanupContext(r, func(context.Context) error {
		return cleanup()
	})
}

// RegisterWithCleanupContext adds a resource to tracking along with an optional
// context-aware force-cleanup function.
func (m *Manager) RegisterWithCleanupContext(r Resource, cleanup CleanupFunc) error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("resource ID cannot be empty")
	}
	if strings.TrimSpace(r.Owner) == "" {
		return errors.New("resource Owner cannot be empty")
	}
	if strings.TrimSpace(r.Type) == "" {
		return errors.New("resource Type cannot be empty")
	}

	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.State == "" {
		r.State = StateActive
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.resources[r.ID]; exists {
		return fmt.Errorf("resource with ID %q already registered", r.ID)
	}
	if len(m.resources) >= m.maxResources {
		return fmt.Errorf("resource tracking capacity reached (%d/%d)", len(m.resources), m.maxResources)
	}

	m.resources[r.ID] = r
	if cleanup != nil {
		m.cleanups[r.ID] = cleanup
	}
	return nil
}

// Release marks a resource as released and removes it from active tracking.
func (m *Manager) Release(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, exists := m.resources[id]
	if !exists {
		return fmt.Errorf("resource with ID %q not found", id)
	}

	r.State = StateReleased
	delete(m.resources, id)
	delete(m.cleanups, id)
	delete(m.cleanupRuns, id)
	return nil
}

// Get returns the resource by ID, if present.
func (m *Manager) Get(id string) (Resource, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, exists := m.resources[id]
	return r, exists
}

// ByOwner returns all currently tracked active resources for a given owner.
func (m *Manager) ByOwner(owner string) []Resource {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Resource
	for _, r := range m.resources {
		if r.Owner == owner {
			result = append(result, r)
		}
	}
	return result
}

// ByType returns all currently tracked active resources of a given type.
func (m *Manager) ByType(rType string) []Resource {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Resource
	for _, r := range m.resources {
		if r.Type == rType {
			result = append(result, r)
		}
	}
	return result
}

// All returns a slice of all currently tracked active resources.
func (m *Manager) All() []Resource {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Resource, 0, len(m.resources))
	for _, r := range m.resources {
		result = append(result, r)
	}
	return result
}

// DetectLeaks checks if there are any remaining active resources for an owner that is
// expected to be stopped. It marks those resources as StateLeaked.
func (m *Manager) DetectLeaks(owner string) []Resource {
	m.mu.Lock()
	defer m.mu.Unlock()

	var leaks []Resource
	for id, r := range m.resources {
		if r.Owner == owner && r.State == StateActive {
			r.State = StateLeaked
			m.resources[id] = r
			leaks = append(leaks, r)
		}
	}
	return leaks
}

// OwnerSnapshot returns an aggregated view of active resources for the specified owner.
func (m *Manager) OwnerSnapshot(owner string) OwnerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := OwnerSnapshot{
		Owner:        owner,
		CountsByType: make(map[string]int),
	}

	for _, r := range m.resources {
		if r.Owner == owner {
			snapshot.TotalActive++
			snapshot.CountsByType[r.Type]++
			if r.State == StateLeaked {
				snapshot.Leaked++
			}
		}
	}
	return snapshot
}

// AllSnapshots returns aggregated views for all owners with active resources.
func (m *Manager) AllSnapshots() []OwnerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	byOwner := make(map[string]*OwnerSnapshot)
	for _, r := range m.resources {
		snap, ok := byOwner[r.Owner]
		if !ok {
			snap = &OwnerSnapshot{
				Owner:        r.Owner,
				CountsByType: make(map[string]int),
			}
			byOwner[r.Owner] = snap
		}
		snap.TotalActive++
		snap.CountsByType[r.Type]++
		if r.State == StateLeaked {
			snap.Leaked++
		}
	}

	result := make([]OwnerSnapshot, 0, len(byOwner))
	for _, s := range byOwner {
		result = append(result, *s)
	}
	return result
}

func (m *Manager) cleanupResource(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	if _, exists := m.resources[id]; !exists {
		m.mu.Unlock()
		return nil
	}
	if run := m.cleanupRuns[id]; run != nil {
		m.mu.Unlock()
		select {
		case <-run.done:
			return run.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	cleanup := m.cleanups[id]
	if cleanup == nil {
		delete(m.resources, id)
		delete(m.cleanups, id)
		m.mu.Unlock()
		return nil
	}
	executor := m.cleanupExecutor
	if executor == nil {
		executor = runtime.NewCallbackExecutor(runtime.DefaultLifecycleCallbackConcurrency)
		m.cleanupExecutor = executor
	}
	run := &cleanupRun{done: make(chan struct{})}
	m.cleanupRuns[id] = run
	m.mu.Unlock()

	result, startErr := executor.Start(ctx, func() error { return cleanup(ctx) })
	if startErr != nil {
		m.mu.Lock()
		run.err = startErr
		if m.cleanupRuns[id] == run {
			delete(m.cleanupRuns, id)
		}
		close(run.done)
		m.mu.Unlock()
		return startErr
	}

	go func() {
		cleanupErr := <-result
		m.mu.Lock()
		run.err = cleanupErr
		if m.cleanupRuns[id] == run {
			delete(m.cleanupRuns, id)
			if cleanupErr == nil {
				delete(m.resources, id)
				delete(m.cleanups, id)
			}
		}
		close(run.done)
		m.mu.Unlock()
	}()

	select {
	case <-run.done:
		return run.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ForceCleanupOwner executes cleanup functions for all tracked resources belonging
// to owner. A resource is removed only after its cleanup completes successfully;
// timed-out or failed resources stay tracked as leaked for diagnostics/retry.
func (m *Manager) ForceCleanupOwner(owner string) []error {
	return m.ForceCleanupOwnerContext(context.Background(), owner)
}

// ForceCleanupOwnerContext is the deadline-aware force-cleanup variant.
func (m *Manager) ForceCleanupOwnerContext(ctx context.Context, owner string) []error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	ids := make([]string, 0)
	for id, resource := range m.resources {
		if resource.Owner == owner {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := m.cleanupResource(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("cleanup %s: %w", id, err))
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errs
}

// HandleLeaks checks if owner has active resources, marks them leaked, and applies the LeakPolicy.
func (m *Manager) HandleLeaks(owner string) ([]Resource, error) {
	return m.HandleLeaksContext(context.Background(), owner)
}

// HandleLeaksContext is the deadline-aware leak-policy variant.
func (m *Manager) HandleLeaksContext(ctx context.Context, owner string) ([]Resource, error) {
	leaks := m.DetectLeaks(owner)
	if len(leaks) == 0 {
		return nil, nil
	}

	policy := m.LeakPolicy()
	switch policy {
	case LeakPolicyForceCleanup:
		errs := m.ForceCleanupOwnerContext(ctx, owner)
		if len(errs) > 0 {
			return leaks, fmt.Errorf("force cleanup encountered errors: %w", errors.Join(errs...))
		}
		return leaks, nil
	case LeakPolicyDegraded:
		return leaks, fmt.Errorf("resource leaks detected under degraded policy: %d resources", len(leaks))
	default: // LeakPolicyWarn
		return leaks, nil
	}
}
