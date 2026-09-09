package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/platform/audit"
)

var (
	// ErrKeyNotFound is returned when the requested key does not exist in the store.
	ErrKeyNotFound = errors.New("storage: key not found")
	// ErrReadOnly is returned when a write operation is attempted without write permission.
	ErrReadOnly = errors.New("storage: write permission denied (requires storage.write)")
	// ErrWriteOnly is returned when a read operation is attempted without read permission.
	ErrWriteOnly = errors.New("storage: read permission denied (requires storage.read)")
)

// KVStore defines the isolated key-value persistence interface for a single plugin owner.
type KVStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) (map[string][]byte, error)
}

// Manager manages namespaced storage for plugins backed by SQLite or an in-memory fallback.
type Manager struct {
	db      *sql.DB
	memMu   sync.RWMutex
	memory  map[string]map[string][]byte // owner -> key -> value
	auditor audit.Auditor
}

// NewManager creates a storage manager backed by *sql.DB.
func NewManager(db *sql.DB) *Manager {
	return &Manager{
		db:     db,
		memory: make(map[string]map[string][]byte),
	}
}

// SetAuditor attaches an audit logger to record storage operations.
func (m *Manager) SetAuditor(a audit.Auditor) {
	m.memMu.Lock()
	defer m.memMu.Unlock()
	m.auditor = a
}

// InitSchema initializes the plugin_storage table in the SQLite database.
func (m *Manager) InitSchema(ctx context.Context) error {
	if m.db == nil {
		return nil
	}
	schema := `
	CREATE TABLE IF NOT EXISTS plugin_storage (
		owner TEXT NOT NULL,
		key TEXT NOT NULL,
		value BLOB NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY (owner, key)
	);
	CREATE INDEX IF NOT EXISTS idx_plugin_storage_owner_key ON plugin_storage (owner, key);
	`
	_, err := m.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("init plugin_storage schema: %w", err)
	}
	return nil
}

// Store returns a scoped KVStore for the given owner with enforced read and write permissions.
func (m *Manager) Store(owner string, canRead, canWrite bool) KVStore {
	cleanOwner := strings.ToLower(strings.TrimSpace(owner))
	return &scopedStore{
		manager:  m,
		owner:    cleanOwner,
		canRead:  canRead,
		canWrite: canWrite,
	}
}

type scopedStore struct {
	manager  *Manager
	owner    string
	canRead  bool
	canWrite bool
}

func (s *scopedStore) Get(ctx context.Context, key string) ([]byte, error) {
	cleanKey := strings.TrimSpace(key)
	if !s.canRead {
		if s.manager.auditor != nil {
			_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
				Action: "storage.read.denied",
				Target: s.owner + "/" + cleanKey,
			})
		}
		return nil, ErrWriteOnly
	}
	if cleanKey == "" {
		return nil, errors.New("key cannot be empty")
	}

	if s.manager.db != nil {
		query := `SELECT value FROM plugin_storage WHERE owner = ? AND key = ?`
		var val []byte
		err := s.manager.db.QueryRowContext(ctx, query, s.owner, cleanKey).Scan(&val)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("storage get error: %w", err)
		}
		return val, nil
	}

	// Memory fallback
	s.manager.memMu.RLock()
	defer s.manager.memMu.RUnlock()
	ownerMap, ok := s.manager.memory[s.owner]
	if !ok {
		return nil, ErrKeyNotFound
	}
	val, ok := ownerMap[cleanKey]
	if !ok {
		return nil, ErrKeyNotFound
	}
	copied := make([]byte, len(val))
	copy(copied, val)
	return copied, nil
}

func (s *scopedStore) Set(ctx context.Context, key string, value []byte) error {
	cleanKey := strings.TrimSpace(key)
	if !s.canWrite {
		if s.manager.auditor != nil {
			_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
				Action: "storage.write.denied",
				Target: s.owner + "/" + cleanKey,
			})
		}
		return ErrReadOnly
	}
	if cleanKey == "" {
		return errors.New("key cannot be empty")
	}
	if value == nil {
		value = []byte{}
	}

	now := time.Now().UTC()
	if s.manager.db != nil {
		query := `
		INSERT INTO plugin_storage (owner, key, value, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at;
		`
		_, err := s.manager.db.ExecContext(ctx, query, s.owner, cleanKey, value, now)
		if err != nil {
			return fmt.Errorf("storage set error: %w", err)
		}
		if s.manager.auditor != nil {
			_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
				Action: "storage.write",
				Target: s.owner + "/" + cleanKey,
				Details: map[string]any{
					"owner": s.owner,
					"key":   cleanKey,
					"size":  len(value),
				},
			})
		}
		return nil
	}

	// Memory fallback
	s.manager.memMu.Lock()
	defer s.manager.memMu.Unlock()
	if s.manager.memory[s.owner] == nil {
		s.manager.memory[s.owner] = make(map[string][]byte)
	}
	copied := make([]byte, len(value))
	copy(copied, value)
	s.manager.memory[s.owner][cleanKey] = copied
	if s.manager.auditor != nil {
		_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
			Action: "storage.write",
			Target: s.owner + "/" + cleanKey,
			Details: map[string]any{
				"owner": s.owner,
				"key":   cleanKey,
				"size":  len(value),
			},
		})
	}
	return nil
}

func (s *scopedStore) Delete(ctx context.Context, key string) error {
	cleanKey := strings.TrimSpace(key)
	if !s.canWrite {
		if s.manager.auditor != nil {
			_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
				Action: "storage.delete.denied",
				Target: s.owner + "/" + cleanKey,
			})
		}
		return ErrReadOnly
	}
	if cleanKey == "" {
		return errors.New("key cannot be empty")
	}

	if s.manager.db != nil {
		query := `DELETE FROM plugin_storage WHERE owner = ? AND key = ?`
		res, err := s.manager.db.ExecContext(ctx, query, s.owner, cleanKey)
		if err != nil {
			return fmt.Errorf("storage delete error: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrKeyNotFound
		}
		if s.manager.auditor != nil {
			_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
				Action: "storage.delete",
				Target: s.owner + "/" + cleanKey,
				Details: map[string]any{
					"owner": s.owner,
					"key":   cleanKey,
				},
			})
		}
		return nil
	}

	// Memory fallback
	s.manager.memMu.Lock()
	defer s.manager.memMu.Unlock()
	ownerMap, ok := s.manager.memory[s.owner]
	if !ok {
		return ErrKeyNotFound
	}
	if _, ok := ownerMap[cleanKey]; !ok {
		return ErrKeyNotFound
	}
	delete(ownerMap, cleanKey)
	if s.manager.auditor != nil {
		_ = s.manager.auditor.Record(ctx, audit.AuditEvent{
			Action: "storage.delete",
			Target: s.owner + "/" + cleanKey,
			Details: map[string]any{
				"owner": s.owner,
				"key":   cleanKey,
			},
		})
	}
	return nil
}

func (s *scopedStore) List(ctx context.Context, prefix string) (map[string][]byte, error) {
	if !s.canRead {
		return nil, ErrWriteOnly
	}

	result := make(map[string][]byte)
	if s.manager.db != nil {
		var query string
		var rows *sql.Rows
		var err error

		if prefix == "" {
			query = `SELECT key, value FROM plugin_storage WHERE owner = ?`
			rows, err = s.manager.db.QueryContext(ctx, query, s.owner)
		} else {
			query = `SELECT key, value FROM plugin_storage WHERE owner = ? AND key LIKE ?`
			rows, err = s.manager.db.QueryContext(ctx, query, s.owner, prefix+"%")
		}
		if err != nil {
			return nil, fmt.Errorf("storage list error: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var k string
			var v []byte
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			result[k] = v
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Memory fallback
	s.manager.memMu.RLock()
	defer s.manager.memMu.RUnlock()
	ownerMap, ok := s.manager.memory[s.owner]
	if !ok {
		return result, nil
	}
	for k, v := range ownerMap {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			copied := make([]byte, len(v))
			copy(copied, v)
			result[k] = copied
		}
	}
	return result, nil
}
