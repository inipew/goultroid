package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/inipew/goultroid/internal/database"
)

// PeerStorage implements gotd's peers.Storage interface backed by SQLite.
// It ensures that all peer access hashes, phone mappings, and contact hashes
// persist across application restarts.
type PeerStorage struct {
	db *database.DB

	// The dispatcher receives the same entities repeatedly. Keep a small
	// process-local dirty cache so repeated background writes do not contend
	// for SQLite's single connection.
	mu       sync.RWMutex
	peers    map[peers.Key]int64
	entities map[string]peerEntitySnapshot
}

type peerEntitySnapshot struct {
	username, phone, firstName, lastName, title string
}

var _ peers.Storage = (*PeerStorage)(nil)

func NewPeerStorage(db *database.DB) *PeerStorage {
	return &PeerStorage{db: db, peers: make(map[peers.Key]int64), entities: make(map[string]peerEntitySnapshot)}
}

func (s *PeerStorage) Save(ctx context.Context, key peers.Key, value peers.Value) error {
	if s.db == nil { return errors.New("database is nil") }
	s.mu.RLock(); old, ok := s.peers[key]; s.mu.RUnlock()
	if ok && old == value.AccessHash { return nil }

	query := `
	INSERT INTO peers_storage (prefix, id, access_hash, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(prefix, id) DO UPDATE SET
		access_hash = excluded.access_hash,
		updated_at = excluded.updated_at;
	`
	if _, err := s.db.ExecContext(ctx, query, key.Prefix, key.ID, value.AccessHash, time.Now()); err != nil {
		return fmt.Errorf("failed to save peer (%s:%d): %w", key.Prefix, key.ID, err)
	}
	s.mu.Lock(); s.peers[key] = value.AccessHash; s.mu.Unlock()
	return nil
}

func (s *PeerStorage) Find(ctx context.Context, key peers.Key) (peers.Value, bool, error) {
	if s.db == nil { return peers.Value{}, false, errors.New("database is nil") }
	s.mu.RLock(); cached, ok := s.peers[key]; s.mu.RUnlock()
	if ok { return peers.Value{AccessHash: cached}, true, nil }
	var accessHash int64
	err := s.db.QueryRowContext(ctx, `SELECT access_hash FROM peers_storage WHERE prefix = ? AND id = ?;`, key.Prefix, key.ID).Scan(&accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { return peers.Value{}, false, nil }
		return peers.Value{}, false, fmt.Errorf("failed to find peer (%s:%d): %w", key.Prefix, key.ID, err)
	}
	s.mu.Lock(); s.peers[key] = accessHash; s.mu.Unlock()
	return peers.Value{AccessHash: accessHash}, true, nil
}

func (s *PeerStorage) SavePhone(ctx context.Context, phone string, key peers.Key) error {
	if s.db == nil { return errors.New("database is nil") }
	query := `
	INSERT INTO peers_phones (phone, prefix, id, access_hash, updated_at)
	VALUES (?, ?, ?, COALESCE((SELECT access_hash FROM peers_storage WHERE prefix = ? AND id = ?), 0), ?)
	ON CONFLICT(phone) DO UPDATE SET
		prefix = excluded.prefix,
		id = excluded.id,
		access_hash = excluded.access_hash,
		updated_at = excluded.updated_at;
	`
	if _, err := s.db.ExecContext(ctx, query, phone, key.Prefix, key.ID, key.Prefix, key.ID, time.Now()); err != nil {
		return fmt.Errorf("failed to save phone %q: %w", phone, err)
	}
	return nil
}

func (s *PeerStorage) FindPhone(ctx context.Context, phone string) (peers.Key, peers.Value, bool, error) {
	if s.db == nil { return peers.Key{}, peers.Value{}, false, errors.New("database is nil") }
	var prefix string; var id, accessHash int64
	err := s.db.QueryRowContext(ctx, `SELECT p.prefix, p.id, COALESCE(s.access_hash, p.access_hash, 0) FROM peers_phones p LEFT JOIN peers_storage s ON p.prefix = s.prefix AND p.id = s.id WHERE p.phone = ?;`, phone).Scan(&prefix, &id, &accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { return peers.Key{}, peers.Value{}, false, nil }
		return peers.Key{}, peers.Value{}, false, fmt.Errorf("failed to find phone %q: %w", phone, err)
	}
	return peers.Key{Prefix: prefix, ID: id}, peers.Value{AccessHash: accessHash}, true, nil
}

func (s *PeerStorage) GetContactsHash(ctx context.Context) (int64, error) {
	if s.db == nil { return 0, errors.New("database is nil") }
	var hash int64
	err := s.db.QueryRowContext(ctx, `SELECT int_val FROM peers_metadata WHERE key = 'contacts_hash';`).Scan(&hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { return 0, nil }
		return 0, fmt.Errorf("failed to get contacts hash: %w", err)
	}
	return hash, nil
}

func (s *PeerStorage) SaveContactsHash(ctx context.Context, hash int64) error {
	if s.db == nil { return errors.New("database is nil") }
	_, err := s.db.ExecContext(ctx, `INSERT INTO peers_metadata (key, int_val) VALUES ('contacts_hash', ?) ON CONFLICT(key) DO UPDATE SET int_val = excluded.int_val;`, hash)
	if err != nil { return fmt.Errorf("failed to save contacts hash: %w", err) }
	return nil
}

func (s *PeerStorage) DB() *database.DB { return s.db }

func (s *PeerStorage) SaveEntity(ctx context.Context, prefix string, id int64, username, phone, firstName, lastName, title string) error {
	if s.db == nil { return errors.New("database is nil") }
	key := fmt.Sprintf("%s:%d", prefix, id)
	snapshot := peerEntitySnapshot{username: username, phone: phone, firstName: firstName, lastName: lastName, title: title}
	s.mu.RLock(); old, ok := s.entities[key]; s.mu.RUnlock()
	if ok && old == snapshot { return nil }
	if err := s.db.SavePeerEntity(ctx, prefix, id, username, phone, firstName, lastName, title); err != nil { return err }
	s.mu.Lock(); s.entities[key] = snapshot; s.mu.Unlock()
	return nil
}

func (s *PeerStorage) FindByUsername(ctx context.Context, username string) (peers.Key, peers.Value, bool, error) {
	if s.db == nil { return peers.Key{}, peers.Value{}, false, errors.New("database is nil") }
	prefix, id, accessHash, found, err := s.db.FindPeerByUsername(ctx, username)
	if err != nil || !found { return peers.Key{}, peers.Value{}, false, err }
	return peers.Key{Prefix: prefix, ID: id}, peers.Value{AccessHash: accessHash}, true, nil
}
