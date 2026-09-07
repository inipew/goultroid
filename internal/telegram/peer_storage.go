package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
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
	if s.db == nil {
		return errors.New("database is nil")
	}
	s.mu.RLock()
	old, ok := s.peers[key]
	s.mu.RUnlock()
	if ok && old == value.AccessHash {
		return nil
	}

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
	s.mu.Lock()
	s.peers[key] = value.AccessHash
	s.mu.Unlock()
	return nil
}

func (s *PeerStorage) Find(ctx context.Context, key peers.Key) (peers.Value, bool, error) {
	if s.db == nil {
		return peers.Value{}, false, errors.New("database is nil")
	}
	s.mu.RLock()
	cached, ok := s.peers[key]
	s.mu.RUnlock()
	if ok {
		return peers.Value{AccessHash: cached}, true, nil
	}
	var accessHash int64
	err := s.db.QueryRowContext(ctx, `SELECT access_hash FROM peers_storage WHERE prefix = ? AND id = ?;`, key.Prefix, key.ID).Scan(&accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return peers.Value{}, false, nil
		}
		return peers.Value{}, false, fmt.Errorf("failed to find peer (%s:%d): %w", key.Prefix, key.ID, err)
	}
	s.mu.Lock()
	s.peers[key] = accessHash
	s.mu.Unlock()
	return peers.Value{AccessHash: accessHash}, true, nil
}

func (s *PeerStorage) SavePhone(ctx context.Context, phone string, key peers.Key) error {
	if s.db == nil {
		return errors.New("database is nil")
	}
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
	if s.db == nil {
		return peers.Key{}, peers.Value{}, false, errors.New("database is nil")
	}
	var prefix string
	var id, accessHash int64
	err := s.db.QueryRowContext(ctx, `SELECT p.prefix, p.id, COALESCE(s.access_hash, p.access_hash, 0) FROM peers_phones p LEFT JOIN peers_storage s ON p.prefix = s.prefix AND p.id = s.id WHERE p.phone = ?;`, phone).Scan(&prefix, &id, &accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return peers.Key{}, peers.Value{}, false, nil
		}
		return peers.Key{}, peers.Value{}, false, fmt.Errorf("failed to find phone %q: %w", phone, err)
	}
	return peers.Key{Prefix: prefix, ID: id}, peers.Value{AccessHash: accessHash}, true, nil
}

func (s *PeerStorage) GetContactsHash(ctx context.Context) (int64, error) {
	if s.db == nil {
		return 0, errors.New("database is nil")
	}
	var hash int64
	err := s.db.QueryRowContext(ctx, `SELECT int_val FROM peers_metadata WHERE key = 'contacts_hash';`).Scan(&hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get contacts hash: %w", err)
	}
	return hash, nil
}

func (s *PeerStorage) SaveContactsHash(ctx context.Context, hash int64) error {
	if s.db == nil {
		return errors.New("database is nil")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO peers_metadata (key, int_val) VALUES ('contacts_hash', ?) ON CONFLICT(key) DO UPDATE SET int_val = excluded.int_val;`, hash)
	if err != nil {
		return fmt.Errorf("failed to save contacts hash: %w", err)
	}
	return nil
}

// Invalidate removes a cached access hash from memory and persistent SQLite storage.
func (s *PeerStorage) Invalidate(key peers.Key) error {
	s.mu.Lock()
	delete(s.peers, key)
	s.mu.Unlock()
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := s.db.ExecContext(ctx, `DELETE FROM peers_storage WHERE prefix = ? AND id = ?;`, key.Prefix, key.ID)
		return err
	}
	return nil
}

func (s *PeerStorage) DB() *database.DB { return s.db }

func (s *PeerStorage) SaveEntity(ctx context.Context, prefix string, id int64, username, phone, firstName, lastName, title string) error {
	if s.db == nil {
		return errors.New("database is nil")
	}
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	key := fmt.Sprintf("%s:%d", prefix, id)
	snapshot := peerEntitySnapshot{username: username, phone: phone, firstName: firstName, lastName: lastName, title: title}
	s.mu.RLock()
	old, ok := s.entities[key]
	s.mu.RUnlock()
	if ok && old == snapshot {
		return nil
	}
	if err := s.db.SavePeerEntity(ctx, prefix, id, username, phone, firstName, lastName, title); err != nil {
		return err
	}
	s.mu.Lock()
	s.entities[key] = snapshot
	s.mu.Unlock()
	return nil
}

func (s *PeerStorage) FindByUsername(ctx context.Context, username string) (peers.Key, peers.Value, bool, error) {
	if s.db == nil {
		return peers.Key{}, peers.Value{}, false, errors.New("database is nil")
	}
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	prefix, id, accessHash, found, err := s.db.FindPeerByUsername(ctx, username)
	if err != nil || !found {
		return peers.Key{}, peers.Value{}, false, err
	}
	return peers.Key{Prefix: prefix, ID: id}, peers.Value{AccessHash: accessHash}, true, nil
}

// SaveEntitiesBatch writes multiple users, channels, and chats in a single SQLite transaction.
// It skips entities whose state has not changed in the process-local cache.
func (s *PeerStorage) SaveEntitiesBatch(ctx context.Context, users []*tg.User, channels []*tg.Channel, chats []*tg.Chat) error {
	if s.db == nil {
		return errors.New("database is nil")
	}

	type storageItem struct {
		key   peers.Key
		value int64
	}
	type entityItem struct {
		prefix, idStr string
		id            int64
		snapshot      peerEntitySnapshot
	}

	var toStore []storageItem
	var toEntity []entityItem

	s.mu.RLock()
	for _, u := range users {
		if u == nil {
			continue
		}
		key := peers.Key{Prefix: "user", ID: u.ID}
		if old, ok := s.peers[key]; !ok || old != u.AccessHash {
			toStore = append(toStore, storageItem{key: key, value: u.AccessHash})
		}
		uname := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u.Username), "@"))
		snap := peerEntitySnapshot{username: uname, phone: u.Phone, firstName: u.FirstName, lastName: u.LastName}
		eKey := fmt.Sprintf("user:%d", u.ID)
		if oldSnap, ok := s.entities[eKey]; !ok || oldSnap != snap {
			toEntity = append(toEntity, entityItem{prefix: "user", id: u.ID, idStr: eKey, snapshot: snap})
		}
	}
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		key := peers.Key{Prefix: "channel", ID: ch.ID}
		if old, ok := s.peers[key]; !ok || old != ch.AccessHash {
			toStore = append(toStore, storageItem{key: key, value: ch.AccessHash})
		}
		uname := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ch.Username), "@"))
		snap := peerEntitySnapshot{username: uname, title: ch.Title}
		eKey := fmt.Sprintf("channel:%d", ch.ID)
		if oldSnap, ok := s.entities[eKey]; !ok || oldSnap != snap {
			toEntity = append(toEntity, entityItem{prefix: "channel", id: ch.ID, idStr: eKey, snapshot: snap})
		}
	}
	for _, c := range chats {
		if c == nil {
			continue
		}
		key := peers.Key{Prefix: "chat", ID: c.ID}
		if old, ok := s.peers[key]; !ok || old != 0 {
			toStore = append(toStore, storageItem{key: key, value: 0})
		}
		snap := peerEntitySnapshot{title: c.Title}
		eKey := fmt.Sprintf("chat:%d", c.ID)
		if oldSnap, ok := s.entities[eKey]; !ok || oldSnap != snap {
			toEntity = append(toEntity, entityItem{prefix: "chat", id: c.ID, idStr: eKey, snapshot: snap})
		}
	}
	s.mu.RUnlock()

	if len(toStore) == 0 && len(toEntity) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin batch tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	if len(toStore) > 0 {
		queryStorage := `
		INSERT INTO peers_storage (prefix, id, access_hash, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(prefix, id) DO UPDATE SET
			access_hash = excluded.access_hash,
			updated_at = excluded.updated_at;`
		stmtStorage, err := tx.PrepareContext(ctx, queryStorage)
		if err != nil {
			return fmt.Errorf("prepare storage stmt: %w", err)
		}
		defer stmtStorage.Close()

		for _, item := range toStore {
			if _, err := stmtStorage.ExecContext(ctx, item.key.Prefix, item.key.ID, item.value, now); err != nil {
				return fmt.Errorf("exec storage stmt (%s:%d): %w", item.key.Prefix, item.key.ID, err)
			}
		}
	}

	if len(toEntity) > 0 {
		queryEntity := `
		INSERT INTO peers_entities (prefix, id, username, phone, first_name, last_name, title, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(prefix, id) DO UPDATE SET
			username = CASE WHEN excluded.username != '' THEN excluded.username ELSE peers_entities.username END,
			phone = CASE WHEN excluded.phone != '' THEN excluded.phone ELSE peers_entities.phone END,
			first_name = CASE WHEN excluded.first_name != '' THEN excluded.first_name ELSE peers_entities.first_name END,
			last_name = CASE WHEN excluded.last_name != '' THEN excluded.last_name ELSE peers_entities.last_name END,
			title = CASE WHEN excluded.title != '' THEN excluded.title ELSE peers_entities.title END,
			updated_at = excluded.updated_at;`
		stmtEntity, err := tx.PrepareContext(ctx, queryEntity)
		if err != nil {
			return fmt.Errorf("prepare entity stmt: %w", err)
		}
		defer stmtEntity.Close()

		for _, item := range toEntity {
			if _, err := stmtEntity.ExecContext(ctx, item.prefix, item.id, item.snapshot.username, item.snapshot.phone, item.snapshot.firstName, item.snapshot.lastName, item.snapshot.title, now); err != nil {
				return fmt.Errorf("exec entity stmt (%s:%d): %w", item.prefix, item.id, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch tx: %w", err)
	}

	s.mu.Lock()
	for _, item := range toStore {
		s.peers[item.key] = item.value
	}
	for _, item := range toEntity {
		s.entities[item.idStr] = item.snapshot
	}
	s.mu.Unlock()

	return nil
}
