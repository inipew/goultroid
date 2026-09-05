package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/inipew/goultroid/internal/database"
)

// PeerStorage implements gotd's peers.Storage interface backed by SQLite.
// It ensures that all peer access hashes, phone mappings, and contact hashes
// persist across application restarts.
type PeerStorage struct {
	db *database.DB
}

var _ peers.Storage = (*PeerStorage)(nil)

// NewPeerStorage creates a new PeerStorage instance.
func NewPeerStorage(db *database.DB) *PeerStorage {
	return &PeerStorage{db: db}
}

// Save stores or updates a peer's access hash in SQLite.
func (s *PeerStorage) Save(ctx context.Context, key peers.Key, value peers.Value) error {
	if s.db == nil {
		return errors.New("database is nil")
	}

	query := `
	INSERT INTO peers_storage (prefix, id, access_hash, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(prefix, id) DO UPDATE SET
		access_hash = excluded.access_hash,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query, key.Prefix, key.ID, value.AccessHash, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save peer (%s:%d): %w", key.Prefix, key.ID, err)
	}
	return nil
}

// Find retrieves a peer's access hash from SQLite by key.
func (s *PeerStorage) Find(ctx context.Context, key peers.Key) (peers.Value, bool, error) {
	if s.db == nil {
		return peers.Value{}, false, errors.New("database is nil")
	}

	query := `SELECT access_hash FROM peers_storage WHERE prefix = ? AND id = ?;`
	var accessHash int64
	err := s.db.QueryRowContext(ctx, query, key.Prefix, key.ID).Scan(&accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return peers.Value{}, false, nil
		}
		return peers.Value{}, false, fmt.Errorf("failed to find peer (%s:%d): %w", key.Prefix, key.ID, err)
	}

	return peers.Value{AccessHash: accessHash}, true, nil
}

// SavePhone associates a phone number with a peer key.
func (s *PeerStorage) SavePhone(ctx context.Context, phone string, key peers.Key) error {
	if s.db == nil {
		return errors.New("database is nil")
	}

	query := `
	INSERT INTO peers_phones (phone, prefix, id, access_hash, updated_at)
	VALUES (?, ?, ?, 0, ?)
	ON CONFLICT(phone) DO UPDATE SET
		prefix = excluded.prefix,
		id = excluded.id,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query, phone, key.Prefix, key.ID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save phone %q: %w", phone, err)
	}
	return nil
}

// FindPhone retrieves a peer key and value associated with a phone number.
func (s *PeerStorage) FindPhone(ctx context.Context, phone string) (peers.Key, peers.Value, bool, error) {
	if s.db == nil {
		return peers.Key{}, peers.Value{}, false, errors.New("database is nil")
	}

	query := `
	SELECT p.prefix, p.id, COALESCE(s.access_hash, 0)
	FROM peers_phones p
	LEFT JOIN peers_storage s ON p.prefix = s.prefix AND p.id = s.id
	WHERE p.phone = ?;
	`
	var prefix string
	var id int64
	var accessHash int64

	err := s.db.QueryRowContext(ctx, query, phone).Scan(&prefix, &id, &accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return peers.Key{}, peers.Value{}, false, nil
		}
		return peers.Key{}, peers.Value{}, false, fmt.Errorf("failed to find phone %q: %w", phone, err)
	}

	key := peers.Key{Prefix: prefix, ID: id}
	val := peers.Value{AccessHash: accessHash}
	return key, val, true, nil
}

// GetContactsHash retrieves the contact list synchronization hash.
func (s *PeerStorage) GetContactsHash(ctx context.Context) (int64, error) {
	if s.db == nil {
		return 0, errors.New("database is nil")
	}

	query := `SELECT int_val FROM peers_metadata WHERE key = 'contacts_hash';`
	var hash int64
	err := s.db.QueryRowContext(ctx, query).Scan(&hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get contacts hash: %w", err)
	}
	return hash, nil
}

// SaveContactsHash persists the contact list synchronization hash.
func (s *PeerStorage) SaveContactsHash(ctx context.Context, hash int64) error {
	if s.db == nil {
		return errors.New("database is nil")
	}

	query := `
	INSERT INTO peers_metadata (key, int_val)
	VALUES ('contacts_hash', ?)
	ON CONFLICT(key) DO UPDATE SET int_val = excluded.int_val;
	`
	_, err := s.db.ExecContext(ctx, query, hash)
	if err != nil {
		return fmt.Errorf("failed to save contacts hash: %w", err)
	}
	return nil
}
