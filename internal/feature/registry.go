package feature

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrInvalidOwner      = errors.New("feature: invalid owner")
	ErrAlreadyRegistered = errors.New("feature: already registered")
)

// Owner binds one feature declaration to the runtime lifecycle generation that
// owns it. Scope is intentionally the same identity used by TaskEngine.
type Owner struct {
	ID    string
	Scope tasks.ScopeIdentity
}

// Entry is an immutable snapshot returned by Registry readers.
type Entry struct {
	Owner Owner
	Spec  Spec
}

type registryEntry struct {
	entry Entry
	token uint64
}

// Catalog is the read-only feature discovery surface exposed outside the plugin
// lifecycle owner. Mutation remains private to Registry registrations.
type Catalog interface {
	Get(id string) (Entry, bool)
	All() []Entry
	ForSurface(source execution.Source) []Entry
	FindInteraction(featureID string, kind InteractionKind, id string) (Interaction, bool)
	FindCommand(featureID, name string) (CommandSurface, bool)
	FeatureScope(featureID string) (tasks.ScopeIdentity, bool)
	HasAction(featureID, actionID string) bool
}

var _ Catalog = (*Registry)(nil)

// Registry is the canonical discovery catalog for feature surfaces. It stores
// metadata only; execution remains owned by core.Router and later interaction
// runtimes. Registrations are lifecycle-scoped and safe against stale cleanup.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]registryEntry
	next    uint64
}

// Registration owns one Registry entry.
type Registration struct {
	registry *Registry
	feature  string
	token    uint64
	once     sync.Once
}

// NewRegistry creates an empty feature catalog.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]registryEntry)}
}

// Register validates and atomically publishes a feature specification.
func (r *Registry) Register(owner Owner, spec Spec) (*Registration, error) {
	if r == nil {
		return nil, errors.New("feature: registry is nil")
	}
	owner.ID = normalizeID(owner.ID)
	if !validID(owner.ID) || owner.Scope.IsZero() {
		return nil, ErrInvalidOwner
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if normalizeID(spec.ID) != owner.ID {
		return nil, fmt.Errorf("%w: feature id %q does not match owner %q", ErrInvalidOwner, spec.ID, owner.ID)
	}

	entry := Entry{Owner: owner, Spec: cloneSpec(spec)}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[spec.ID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRegistered, spec.ID)
	}
	r.next++
	token := r.next
	r.entries[spec.ID] = registryEntry{entry: entry, token: token}
	return &Registration{registry: r, feature: spec.ID, token: token}, nil
}

// Close removes the owned entry only if the registration token still matches.
// A stale cleanup can therefore never remove a newer feature generation.
func (r *Registration) Close() {
	if r == nil || r.registry == nil {
		return
	}
	r.once.Do(func() {
		r.registry.mu.Lock()
		defer r.registry.mu.Unlock()
		current, ok := r.registry.entries[r.feature]
		if ok && current.token == r.token {
			delete(r.registry.entries, r.feature)
		}
	})
}

// Get returns a defensive snapshot of one feature declaration.
func (r *Registry) Get(id string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	id = normalizeID(id)
	r.mu.RLock()
	entry, ok := r.entries[id]
	r.mu.RUnlock()
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(entry.entry), true
}

// All returns deterministic snapshots sorted by feature ID.
func (r *Registry) All() []Entry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	result := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		result = append(result, cloneEntry(entry.entry))
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Spec.ID < result[j].Spec.ID })
	return result
}

// ForSurface returns features exposing at least one command or interaction on source.
func (r *Registry) ForSurface(source execution.Source) []Entry {
	all := r.All()
	result := all[:0]
	for _, entry := range all {
		if exposesSource(entry.Spec, source) {
			result = append(result, entry)
		}
	}
	return result
}

// FindInteraction finds one typed interaction by feature, kind, and ID.
func (r *Registry) FindInteraction(featureID string, kind InteractionKind, id string) (Interaction, bool) {
	entry, ok := r.Get(featureID)
	if !ok {
		return Interaction{}, false
	}
	id = normalizeID(id)
	for _, interaction := range entry.Spec.Interactions {
		if interaction.Kind == kind && normalizeID(interaction.ID) == id {
			return interaction, true
		}
	}
	return Interaction{}, false
}

// FeatureScope returns the current lifecycle scope for one registered feature.
func (r *Registry) FeatureScope(featureID string) (tasks.ScopeIdentity, bool) {
	if r == nil {
		return tasks.ScopeIdentity{}, false
	}
	featureID = normalizeID(featureID)
	r.mu.RLock()
	entry, ok := r.entries[featureID]
	r.mu.RUnlock()
	if !ok {
		return tasks.ScopeIdentity{}, false
	}
	return entry.entry.Owner.Scope, true
}

// HasAction reports whether the current feature generation declares an action.
func (r *Registry) HasAction(featureID, actionID string) bool {
	if r == nil {
		return false
	}
	featureID = normalizeID(featureID)
	actionID = normalizeID(actionID)
	r.mu.RLock()
	entry, ok := r.entries[featureID]
	if ok {
		for _, interaction := range entry.entry.Spec.Interactions {
			if interaction.Kind == InteractionAction && normalizeID(interaction.ID) == actionID {
				r.mu.RUnlock()
				return true
			}
		}
	}
	r.mu.RUnlock()
	return false
}

// FindCommand finds one catalog command by canonical name or alias.
func (r *Registry) FindCommand(featureID, name string) (CommandSurface, bool) {
	entry, ok := r.Get(featureID)
	if !ok {
		return CommandSurface{}, false
	}
	name = normalizeID(name)
	for _, command := range entry.Spec.Commands {
		if command.Name == name {
			return cloneCommandSurface(command), true
		}
		for _, alias := range command.Aliases {
			if alias == name {
				return cloneCommandSurface(command), true
			}
		}
	}
	return CommandSurface{}, false
}

func exposesSource(spec Spec, source execution.Source) bool {
	for _, command := range spec.Commands {
		if command.Surfaces.Supports(source) {
			return true
		}
	}
	for _, interaction := range spec.Interactions {
		if interaction.Surfaces.Supports(source) {
			return true
		}
	}
	return false
}

func cloneEntry(entry Entry) Entry {
	entry.Spec = cloneSpec(entry.Spec)
	return entry
}

func cloneCommandSurface(command CommandSurface) CommandSurface {
	command.Aliases = append([]string(nil), command.Aliases...)
	return command
}
