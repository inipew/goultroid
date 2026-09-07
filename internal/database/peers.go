package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PeerRepository is the minimal contract for peer metadata.
type PeerRepository interface {
	SavePeerEntity(ctx context.Context, prefix string, id int64, username, phone, firstName, lastName, title string) error
	FindPeerByUsername(ctx context.Context, username string) (prefix string, id int64, accessHash int64, found bool, err error)
}

// PeerEntity stores metadata for Telegram peers (users, chats, channels).
type PeerEntity struct {
	Prefix    string    `json:"prefix"`
	ID        int64     `json:"id"`
	Username  string    `json:"username,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	FirstName string    `json:"first_name,omitempty"`
	LastName  string    `json:"last_name,omitempty"`
	Title     string    `json:"title,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SavePeerEntity creates or updates entity metadata for a peer (user or channel/chat).
func (d *DB) SavePeerEntity(ctx context.Context, prefix string, id int64, username, phone, firstName, lastName, title string) error {
	query := `
		INSERT INTO peers_entities (prefix, id, username, phone, first_name, last_name, title, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(prefix, id) DO UPDATE SET
			username = CASE WHEN excluded.username != '' THEN excluded.username ELSE peers_entities.username END,
			phone = CASE WHEN excluded.phone != '' THEN excluded.phone ELSE peers_entities.phone END,
			first_name = CASE WHEN excluded.first_name != '' THEN excluded.first_name ELSE peers_entities.first_name END,
			last_name = CASE WHEN excluded.last_name != '' THEN excluded.last_name ELSE peers_entities.last_name END,
			title = CASE WHEN excluded.title != '' THEN excluded.title ELSE peers_entities.title END,
			updated_at = excluded.updated_at`
	_, err := d.ExecContext(ctx, query, prefix, id, strings.TrimPrefix(username, "@"), phone, firstName, lastName, title, time.Now())
	if err != nil {
		return fmt.Errorf("failed to save peer entity (%s:%d): %w", prefix, id, err)
	}
	return nil
}

// FindPeerByUsername searches local SQLite cache for a peer by its username (case-insensitive)
// and returns its prefix, id, and access hash from peers_storage.
func (d *DB) FindPeerByUsername(ctx context.Context, username string) (string, int64, int64, bool, error) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return "", 0, 0, false, nil
	}
	query := `
		SELECT e.prefix, e.id, COALESCE(s.access_hash, 0)
		FROM peers_entities e
		LEFT JOIN peers_storage s ON e.prefix = s.prefix AND e.id = s.id
		WHERE e.username = ? COLLATE NOCASE
		LIMIT 1`
	var prefix string
	var id int64
	var accessHash int64
	err := d.QueryRowContext(ctx, query, username).Scan(&prefix, &id, &accessHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, 0, false, nil
		}
		return "", 0, 0, false, fmt.Errorf("failed to find peer by username %q: %w", username, err)
	}
	return prefix, id, accessHash, true, nil
}
