package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Save(ctx context.Context, row interaction.DurableSession) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("interaction sqlite: database required")
	}
	session := row.Session
	state := session.State
	if state == nil {
		state = []byte{}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO interaction_sessions
		(id, feature_id, version, actor_id, chat_id, message_id, inline_message_id, state, revision, created_at, expires_at, input_expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET feature_id=excluded.feature_id, version=excluded.version,
		actor_id=excluded.actor_id, chat_id=excluded.chat_id, message_id=excluded.message_id,
		inline_message_id=excluded.inline_message_id, state=excluded.state, revision=excluded.revision,
		created_at=excluded.created_at, expires_at=excluded.expires_at, input_expires_at=excluded.input_expires_at`,
		session.ID, session.FeatureID, row.Version, session.Binding.ActorID, session.Binding.ChatID,
		session.Binding.MessageID, session.Binding.InlineMessageID, state, session.Revision,
		session.CreatedAt.UnixNano(), session.ExpiresAt.UnixNano(), nullableTime(row.InputExpires))
	return err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("interaction sqlite: database required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM interaction_sessions WHERE id = ?`, id)
	return err
}

func (s *Store) Load(ctx context.Context) ([]interaction.DurableSession, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("interaction sqlite: database required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, feature_id, version, actor_id, chat_id,
		message_id, inline_message_id, state, revision, created_at, expires_at, input_expires_at
		FROM interaction_sessions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []interaction.DurableSession
	for rows.Next() {
		var row interaction.DurableSession
		var created, expires, inputExpires int64
		err = rows.Scan(&row.Session.ID, &row.Session.FeatureID, &row.Version,
			&row.Session.Binding.ActorID, &row.Session.Binding.ChatID, &row.Session.Binding.MessageID,
			&row.Session.Binding.InlineMessageID, &row.Session.State, &row.Session.Revision,
			&created, &expires, &inputExpires)
		if err != nil {
			return nil, err
		}
		row.Session.CreatedAt = time.Unix(0, created)
		row.Session.ExpiresAt = time.Unix(0, expires)
		if inputExpires != 0 {
			row.InputExpires = time.Unix(0, inputExpires)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func nullableTime(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixNano()
}

var _ interaction.DurableStore = (*Store)(nil)
