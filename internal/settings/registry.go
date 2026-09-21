package settings

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var ErrDefinitionChanged = errors.New("settings: definition changed")

// Registry manages schema definitions for all settings across GoUltroid modules and plugins.
type Registry struct {
	mu          sync.RWMutex
	definitions map[string]*SettingDefinition
	versions    map[string]uint64
	nextVersion uint64
}

// NewRegistry initializes an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		definitions: make(map[string]*SettingDefinition),
		versions:    make(map[string]uint64),
	}
}

func makeDefKey(namespace, key string) string {
	return strings.ToLower(strings.TrimSpace(namespace)) + ":" + strings.ToLower(strings.TrimSpace(key))
}

// Register adds a new setting definition. It returns an error if the definition is invalid.
func (r *Registry) Register(def SettingDefinition) error {
	ns := strings.TrimSpace(def.Namespace)
	key := strings.TrimSpace(def.Key)
	if ns == "" {
		return errors.New("setting namespace cannot be empty")
	}
	if key == "" {
		return errors.New("setting key cannot be empty")
	}

	switch def.Type {
	case TypeBool, TypeInt, TypeString, TypeDuration, TypeEnum:
	default:
		return fmt.Errorf("invalid setting type %q for %s:%s", def.Type, ns, key)
	}

	if def.Type == TypeEnum && len(def.AllowedValues) == 0 {
		return fmt.Errorf("enum setting %s:%s must have at least one allowed value", ns, key)
	}

	if def.DefaultValue != "" {
		if err := def.Validate(def.DefaultValue); err != nil {
			return fmt.Errorf("default value for %s:%s is invalid: %w", ns, key, err)
		}
	}

	if def.Category == "" {
		def.Category = CategoryGeneral
	}
	def.Namespace = strings.ToLower(ns)
	def.Key = strings.ToLower(key)

	r.mu.Lock()
	defer r.mu.Unlock()

	lookupKey := makeDefKey(def.Namespace, def.Key)
	copyDef := def
	r.nextVersion++
	r.definitions[lookupKey] = &copyDef
	r.versions[lookupKey] = r.nextVersion
	return nil
}

// Get finds a setting definition by namespace and key.
// SetDefault replaces the schema fallback for an already registered setting.
// The value is canonicalized using the setting definition before publication.
func (r *Registry) SetDefault(namespace, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	lookupKey := makeDefKey(namespace, key)
	def, exists := r.definitions[lookupKey]
	if !exists || def == nil {
		return fmt.Errorf("setting definition not found: %s:%s", namespace, key)
	}
	copyDef := *def
	canonical, err := copyDef.Canonicalize(value)
	if err != nil {
		return fmt.Errorf("invalid default for %s:%s: %w", copyDef.Namespace, copyDef.Key, err)
	}
	copyDef.DefaultValue = canonical
	r.nextVersion++
	r.definitions[lookupKey] = &copyDef
	r.versions[lookupKey] = r.nextVersion
	return nil
}

func (r *Registry) Get(namespace, key string) (*SettingDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, exists := r.definitions[makeDefKey(namespace, key)]
	if !exists {
		return nil, false
	}
	copyDef := *def
	return &copyDef, true
}

// GetVersioned returns an immutable definition snapshot plus its per-definition
// revision. The revision changes whenever Register or SetDefault replaces that
// same namespace:key; unrelated definitions do not invalidate it.
func (r *Registry) GetVersioned(namespace, key string) (*SettingDefinition, uint64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lookupKey := makeDefKey(namespace, key)
	def, exists := r.definitions[lookupKey]
	if !exists || def == nil {
		return nil, 0, false
	}
	copyDef := *def
	return &copyDef, r.versions[lookupKey], true
}

// ListByCategory returns all setting definitions in a specified category, sorted by namespace then key.
func (r *Registry) ListByCategory(category string) []SettingDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []SettingDefinition
	for _, def := range r.definitions {
		if strings.EqualFold(def.Category, category) {
			result = append(result, *def)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace == result[j].Namespace {
			return result[i].Key < result[j].Key
		}
		return result[i].Namespace < result[j].Namespace
	})
	return result
}

// ListByNamespace returns all setting definitions in a specified namespace.
func (r *Registry) ListByNamespace(namespace string) []SettingDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nsLower := strings.ToLower(strings.TrimSpace(namespace))
	var result []SettingDefinition
	for _, def := range r.definitions {
		if def.Namespace == nsLower {
			result = append(result, *def)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}

// ListAll returns all registered setting definitions, sorted by category, namespace, then key.
func (r *Registry) ListAll() []SettingDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]SettingDefinition, 0, len(r.definitions))
	for _, def := range r.definitions {
		result = append(result, *def)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Key < result[j].Key
	})
	return result
}

// Categories returns the distinct categories currently present in the registry,
// with standard categories presented first in conventional order.
func (r *Registry) Categories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	for _, def := range r.definitions {
		seen[strings.ToLower(def.Category)] = true
	}

	standardOrder := []string{
		CategoryGeneral,
		CategorySecurity,
		CategoryModeration,
		CategoryAutomation,
		CategoryUI,
		CategoryAdvanced,
	}

	var ordered []string
	for _, cat := range standardOrder {
		if seen[cat] {
			ordered = append(ordered, cat)
			delete(seen, cat)
		}
	}

	var remaining []string
	for cat := range seen {
		remaining = append(remaining, cat)
	}
	sort.Strings(remaining)

	return append(ordered, remaining...)
}
