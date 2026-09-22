package callback

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"time"
)

// StateWriter is the capability exposed to callback producers. Producers may
// create opaque state, but only the canonical callback router may read or claim it.
type StateWriter interface {
	Store(data any, allowedUserID int64, ttl time.Duration) string
	StoreWithScope(data any, scope StateScope, ttl time.Duration) string
}

// StateStore is the canonical thread-safe in-memory owner of temporary callback state.
type StateStore struct {
	mu            sync.RWMutex
	items         map[string]stateItem
	retainedBytes int64
}

const (
	maxStateStoreEntries = 5000
	maxStateStoreBytes   = 8 * 1024 * 1024 // 8MB budget
	maxStateItemBytes    = 64 * 1024       // 64KB max per entry
)

var errUnsupportedState = errors.New("callback state contains unsupported or cyclic data")

func cloneStateData(data any) (any, int64, error) {
	clone, size, err := cloneStateValue(reflect.ValueOf(data), make(map[uintptr]bool))
	if err != nil {
		return nil, 0, err
	}
	if !clone.IsValid() {
		return nil, 64, nil
	}
	return clone.Interface(), size + 64, nil
}

func cloneStateValue(value reflect.Value, visiting map[uintptr]bool) (reflect.Value, int64, error) {
	if !value.IsValid() {
		return reflect.Value{}, 0, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), 16, nil
		}
		cloned, size, err := cloneStateValue(value.Elem(), visiting)
		if err != nil {
			return reflect.Value{}, 0, err
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloned)
		return out, size + 16, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), 8, nil
		}
		ptr := value.Pointer()
		if visiting[ptr] {
			return reflect.Value{}, 0, errUnsupportedState
		}
		visiting[ptr] = true
		defer delete(visiting, ptr)
		cloned, size, err := cloneStateValue(value.Elem(), visiting)
		if err != nil {
			return reflect.Value{}, 0, err
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloned)
		return out, size + 8, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), 24, nil
		}
		ptr := value.Pointer()
		if ptr != 0 && visiting[ptr] {
			return reflect.Value{}, 0, errUnsupportedState
		}
		if ptr != 0 {
			visiting[ptr] = true
			defer delete(visiting, ptr)
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		size := int64(24)
		for i := 0; i < value.Len(); i++ {
			cloned, itemSize, err := cloneStateValue(value.Index(i), visiting)
			if err != nil {
				return reflect.Value{}, 0, err
			}
			out.Index(i).Set(cloned)
			size += itemSize
		}
		return out, size, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), 8, nil
		}
		ptr := value.Pointer()
		if visiting[ptr] {
			return reflect.Value{}, 0, errUnsupportedState
		}
		visiting[ptr] = true
		defer delete(visiting, ptr)
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		size := int64(48)
		iter := value.MapRange()
		for iter.Next() {
			key, keySize, err := cloneStateValue(iter.Key(), visiting)
			if err != nil {
				return reflect.Value{}, 0, err
			}
			item, itemSize, err := cloneStateValue(iter.Value(), visiting)
			if err != nil {
				return reflect.Value{}, 0, err
			}
			out.SetMapIndex(key, item)
			size += keySize + itemSize
		}
		return out, size, nil
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			return value, int64(value.Type().Size()), nil
		}
		out := reflect.New(value.Type()).Elem()
		size := int64(0)
		for i := 0; i < value.NumField(); i++ {
			if !out.Field(i).CanSet() || !value.Field(i).CanInterface() {
				return reflect.Value{}, 0, errUnsupportedState
			}
			cloned, fieldSize, err := cloneStateValue(value.Field(i), visiting)
			if err != nil {
				return reflect.Value{}, 0, err
			}
			out.Field(i).Set(cloned)
			size += fieldSize
		}
		return out, size, nil
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		size := int64(0)
		for i := 0; i < value.Len(); i++ {
			cloned, itemSize, err := cloneStateValue(value.Index(i), visiting)
			if err != nil {
				return reflect.Value{}, 0, err
			}
			out.Index(i).Set(cloned)
			size += itemSize
		}
		return out, size, nil
	case reflect.String:
		return value, int64(len(value.String())) + 16, nil
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return value, int64(value.Type().Size()), nil
	default:
		return reflect.Value{}, 0, errUnsupportedState
	}
}

// NewStateStore creates an initialized StateStore.
func NewStateStore() *StateStore {
	return &StateStore{
		items: make(map[string]stateItem),
	}
}

// Store records arbitrary state data with an optional authorized user restriction and TTL.
// Returns a short opaque ID safe for compact Telegram callback data payloads.
func (s *StateStore) Store(data any, allowedUserID int64, ttl time.Duration) string {
	scope := StateScope{UserID: allowedUserID}
	return s.StoreWithScope(data, scope, ttl)
}

// StoreWithScope records state with full scope metadata.
func (s *StateStore) StoreWithScope(data any, scope StateScope, ttl time.Duration) string {
	cloned, size, err := cloneStateData(data)
	if err != nil || size > maxStateItemBytes {
		return ""
	}

	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	opaqueID := hex.EncodeToString(b)

	expiresAt := time.Now().Add(ttl)
	scope.ExpiresAt = expiresAt

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.items) >= maxStateStoreEntries || s.retainedBytes+size > maxStateStoreBytes {
		now := time.Now()
		for id, item := range s.items {
			if now.After(item.expiresAt) {
				s.retainedBytes -= item.sizeBytes
				delete(s.items, id)
			}
		}
		for len(s.items) >= maxStateStoreEntries || (len(s.items) > 0 && s.retainedBytes+size > maxStateStoreBytes) {
			var oldestID string
			var oldestTime time.Time
			first := true
			for id, item := range s.items {
				if first || item.expiresAt.Before(oldestTime) {
					oldestID = id
					oldestTime = item.expiresAt
					first = false
				}
			}
			if oldestID != "" {
				s.retainedBytes -= s.items[oldestID].sizeBytes
				delete(s.items, oldestID)
			} else {
				break
			}
		}
	}

	s.items[opaqueID] = stateItem{
		data:      cloned,
		scope:     scope,
		expiresAt: expiresAt,
		sizeBytes: size,
	}
	s.retainedBytes += size
	return opaqueID
}

// Len returns current entry count (for metrics).
func (s *StateStore) Len() int {
	if s == nil {
		return 0
	}
	s.Prune()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// RetainedBytes returns current retained byte size (for metrics/testing).
func (s *StateStore) RetainedBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retainedBytes
}

// getEntry resolves an entry without claiming single-use state. It is kept
// package-private for diagnostics and invariant tests; production execution
// must use claimEntry through Router.Prepare/PreparedCallback.Dispatch.
func (s *StateStore) getEntry(opaqueID string) (stateEntry, error) {
	if s == nil {
		return stateEntry{}, ErrStateNotFound
	}

	s.mu.RLock()
	item, exists := s.items[opaqueID]
	s.mu.RUnlock()

	if !exists {
		return stateEntry{}, ErrStateNotFound
	}
	if time.Now().After(item.expiresAt) {
		s.deleteEntry(opaqueID)
		return stateEntry{}, ErrStateExpired
	}
	if item.consumed {
		return stateEntry{}, ErrStateConsumed
	}
	cloned, _, err := cloneStateData(item.data)
	if err != nil {
		return stateEntry{}, ErrStateNotFound
	}
	return stateEntry{Data: cloned, Scope: item.scope}, nil
}

// claimEntry atomically validates and resolves a callback state entry, then
// claims it when it is single-use. Validation runs before the consumed bit is
// changed so an unauthorized click cannot burn another user's token.
//
// The stored payload is immutable, so cloning can happen after releasing the
// lock without allowing a second consumer to win the single-use claim.
func (s *StateStore) claimEntry(opaqueID string, validate func(StateScope) error) (stateEntry, error) {
	if s == nil {
		return stateEntry{}, ErrStateNotFound
	}

	s.mu.Lock()
	item, exists := s.items[opaqueID]
	if !exists {
		s.mu.Unlock()
		return stateEntry{}, ErrStateNotFound
	}
	if time.Now().After(item.expiresAt) {
		s.retainedBytes -= item.sizeBytes
		delete(s.items, opaqueID)
		s.mu.Unlock()
		return stateEntry{}, ErrStateExpired
	}
	if item.consumed {
		s.mu.Unlock()
		return stateEntry{}, ErrStateConsumed
	}
	if validate != nil {
		if err := validate(item.scope); err != nil {
			s.mu.Unlock()
			return stateEntry{}, err
		}
	}
	if item.scope.SingleUse {
		item.consumed = true
		s.items[opaqueID] = item
	}
	s.mu.Unlock()

	cloned, _, err := cloneStateData(item.data)
	if err != nil {
		return stateEntry{}, ErrStateNotFound
	}
	return stateEntry{Data: cloned, Scope: item.scope}, nil
}

func (s *StateStore) deleteEntry(opaqueID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, exists := s.items[opaqueID]; exists {
		s.retainedBytes -= item.sizeBytes
		delete(s.items, opaqueID)
	}
}
