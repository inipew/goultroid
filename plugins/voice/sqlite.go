package voice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
	voiceSvc "github.com/inipew/goultroid/internal/voice"
)

type txBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// SQLiteRepository implements voice.Repository using SQLite.
type SQLiteRepository struct {
	db database.SQLExecutor
}

// NewSQLiteRepository creates a new SQLite-backed voice repository.
func NewSQLiteRepository(db database.SQLExecutor) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

var _ voiceSvc.Repository = (*SQLiteRepository)(nil)

// GetVoiceSession fetches the session state for a given chat ID.
func (r *SQLiteRepository) GetVoiceSession(ctx context.Context, chatID int64) (*voiceSvc.VoiceSessionRecord, error) {
	query := `SELECT chat_id, state, volume, repeat_mode, updated_at FROM voice_sessions WHERE chat_id = ?`
	row := r.db.QueryRowContext(ctx, query, chatID)

	var rec voiceSvc.VoiceSessionRecord
	err := row.Scan(&rec.ChatID, &rec.State, &rec.Volume, &rec.RepeatMode, &rec.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get voice session: %w", err)
	}
	return &rec, nil
}

// UpsertVoiceSession saves or updates the voice chat session.
func (r *SQLiteRepository) UpsertVoiceSession(ctx context.Context, rec *voiceSvc.VoiceSessionRecord) error {
	query := `
	INSERT INTO voice_sessions (chat_id, state, volume, repeat_mode, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(chat_id) DO UPDATE SET
		state = excluded.state,
		volume = excluded.volume,
		repeat_mode = excluded.repeat_mode,
		updated_at = excluded.updated_at
	`
	now := time.Now().UTC()
	if !rec.UpdatedAt.IsZero() {
		now = rec.UpdatedAt.UTC()
	}
	_, err := r.db.ExecContext(ctx, query, rec.ChatID, rec.State, rec.Volume, rec.RepeatMode, now)
	if err != nil {
		return fmt.Errorf("failed to upsert voice session: %w", err)
	}
	return nil
}

// GetVoiceQueue returns all queued tracks for a chat, ordered by position ascending.
func (r *SQLiteRepository) GetVoiceQueue(ctx context.Context, chatID int64) ([]*voiceSvc.VoiceQueueRecord, error) {
	query := `
	SELECT id, chat_id, position, title, artist, source_url, file_path, duration_seconds, source_type, requester_id, created_at
	FROM voice_queue
	WHERE chat_id = ?
	ORDER BY position ASC, id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to query voice queue: %w", err)
	}
	defer rows.Close()

	var records []*voiceSvc.VoiceQueueRecord
	for rows.Next() {
		var rec voiceSvc.VoiceQueueRecord
		if err := rows.Scan(
			&rec.ID, &rec.ChatID, &rec.Position, &rec.Title, &rec.Artist,
			&rec.SourceURL, &rec.FilePath, &rec.DurationSeconds, &rec.SourceType,
			&rec.RequesterID, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan voice queue row: %w", err)
		}
		records = append(records, &rec)
	}
	return records, rows.Err()
}

// AddVoiceQueueTrack appends a new track to the end of the voice queue.
func (r *SQLiteRepository) AddVoiceQueueTrack(ctx context.Context, track *voiceSvc.VoiceQueueRecord) error {
	var maxPos sql.NullInt64
	posQuery := `SELECT MAX(position) FROM voice_queue WHERE chat_id = ?`
	if err := r.db.QueryRowContext(ctx, posQuery, track.ChatID).Scan(&maxPos); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to determine next queue position: %w", err)
	}

	nextPos := 1
	if maxPos.Valid {
		nextPos = int(maxPos.Int64) + 1
	}
	track.Position = nextPos

	insertQuery := `
	INSERT INTO voice_queue (chat_id, position, title, artist, source_url, file_path, duration_seconds, source_type, requester_id, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	now := time.Now().UTC()
	if !track.CreatedAt.IsZero() {
		now = track.CreatedAt.UTC()
	}

	res, err := r.db.ExecContext(
		ctx, insertQuery,
		track.ChatID, track.Position, track.Title, track.Artist,
		track.SourceURL, track.FilePath, track.DurationSeconds,
		track.SourceType, track.RequesterID, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert voice queue track: %w", err)
	}

	id, err := res.LastInsertId()
	if err == nil {
		track.ID = id
	}
	return nil
}

// PopVoiceQueueTrack retrieves and removes the first track in the queue.
func (r *SQLiteRepository) PopVoiceQueueTrack(ctx context.Context, chatID int64) (*voiceSvc.VoiceQueueRecord, error) {
	if txb, ok := r.db.(txBeginner); ok {
		tx, err := txb.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to begin tx for pop track: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		rec, err := r.popTrackWithExec(ctx, tx, chatID)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, nil
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit pop voice track: %w", err)
		}
		return rec, nil
	}

	return r.popTrackWithExec(ctx, r.db, chatID)
}

func (r *SQLiteRepository) popTrackWithExec(ctx context.Context, exec database.SQLExecutor, chatID int64) (*voiceSvc.VoiceQueueRecord, error) {
	query := `
	SELECT id, chat_id, position, title, artist, source_url, file_path, duration_seconds, source_type, requester_id, created_at
	FROM voice_queue
	WHERE chat_id = ?
	ORDER BY position ASC, id ASC
	LIMIT 1
	`
	var rec voiceSvc.VoiceQueueRecord
	err := exec.QueryRowContext(ctx, query, chatID).Scan(
		&rec.ID, &rec.ChatID, &rec.Position, &rec.Title, &rec.Artist,
		&rec.SourceURL, &rec.FilePath, &rec.DurationSeconds, &rec.SourceType,
		&rec.RequesterID, &rec.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan next voice track: %w", err)
	}

	delQuery := `DELETE FROM voice_queue WHERE id = ?`
	if _, err := exec.ExecContext(ctx, delQuery, rec.ID); err != nil {
		return nil, fmt.Errorf("failed to delete popped voice track: %w", err)
	}
	return &rec, nil
}

// ClearVoiceQueue removes all queued tracks for a chat.
func (r *SQLiteRepository) ClearVoiceQueue(ctx context.Context, chatID int64) error {
	query := `DELETE FROM voice_queue WHERE chat_id = ?`
	if _, err := r.db.ExecContext(ctx, query, chatID); err != nil {
		return fmt.Errorf("failed to clear voice queue: %w", err)
	}
	return nil
}

// DeleteVoiceQueueTrack removes a single track from the queue by ID.
func (r *SQLiteRepository) DeleteVoiceQueueTrack(ctx context.Context, id int64) error {
	query := `DELETE FROM voice_queue WHERE id = ?`
	if _, err := r.db.ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("failed to delete track from queue: %w", err)
	}
	return nil
}
