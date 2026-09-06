package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// VoiceSessionRecord represents the persisted state of a voice chat session.
type VoiceSessionRecord struct {
	ChatID     int64     `json:"chat_id"`
	State      string    `json:"state"`
	Volume     int       `json:"volume"`
	RepeatMode string    `json:"repeat_mode"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// VoiceQueueRecord represents a queued audio/video track for voice chat playback.
type VoiceQueueRecord struct {
	ID              int64     `json:"id"`
	ChatID          int64     `json:"chat_id"`
	Position        int       `json:"position"`
	Title           string    `json:"title"`
	Artist          string    `json:"artist"`
	SourceURL       string    `json:"source_url"`
	FilePath        string    `json:"file_path"`
	DurationSeconds int       `json:"duration_seconds"`
	SourceType      string    `json:"source_type"`
	RequesterID     int64     `json:"requester_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// GetVoiceSession fetches the session state for a given chat ID.
func (d *DB) GetVoiceSession(ctx context.Context, chatID int64) (*VoiceSessionRecord, error) {
	query := `SELECT chat_id, state, volume, repeat_mode, updated_at FROM voice_sessions WHERE chat_id = ?`
	row := d.QueryRowContext(ctx, query, chatID)

	var rec VoiceSessionRecord
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
func (d *DB) UpsertVoiceSession(ctx context.Context, rec *VoiceSessionRecord) error {
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
	_, err := d.ExecContext(ctx, query, rec.ChatID, rec.State, rec.Volume, rec.RepeatMode, now)
	if err != nil {
		return fmt.Errorf("failed to upsert voice session: %w", err)
	}
	return nil
}

// GetVoiceQueue returns all queued tracks for a chat, ordered by position ascending.
func (d *DB) GetVoiceQueue(ctx context.Context, chatID int64) ([]*VoiceQueueRecord, error) {
	query := `
	SELECT id, chat_id, position, title, artist, source_url, file_path, duration_seconds, source_type, requester_id, created_at
	FROM voice_queue
	WHERE chat_id = ?
	ORDER BY position ASC, id ASC
	`
	rows, err := d.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to query voice queue: %w", err)
	}
	defer rows.Close()

	var records []*VoiceQueueRecord
	for rows.Next() {
		var r VoiceQueueRecord
		if err := rows.Scan(
			&r.ID, &r.ChatID, &r.Position, &r.Title, &r.Artist,
			&r.SourceURL, &r.FilePath, &r.DurationSeconds, &r.SourceType,
			&r.RequesterID, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan voice queue row: %w", err)
		}
		records = append(records, &r)
	}
	return records, rows.Err()
}

// AddVoiceQueueTrack appends a new track to the end of the voice queue.
func (d *DB) AddVoiceQueueTrack(ctx context.Context, track *VoiceQueueRecord) error {
	var maxPos sql.NullInt64
	posQuery := `SELECT MAX(position) FROM voice_queue WHERE chat_id = ?`
	if err := d.QueryRowContext(ctx, posQuery, track.ChatID).Scan(&maxPos); err != nil && !errors.Is(err, sql.ErrNoRows) {
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

	res, err := d.ExecContext(
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
func (d *DB) PopVoiceQueueTrack(ctx context.Context, chatID int64) (*VoiceQueueRecord, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin tx for pop track: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
	SELECT id, chat_id, position, title, artist, source_url, file_path, duration_seconds, source_type, requester_id, created_at
	FROM voice_queue
	WHERE chat_id = ?
	ORDER BY position ASC, id ASC
	LIMIT 1
	`
	var r VoiceQueueRecord
	err = tx.QueryRowContext(ctx, query, chatID).Scan(
		&r.ID, &r.ChatID, &r.Position, &r.Title, &r.Artist,
		&r.SourceURL, &r.FilePath, &r.DurationSeconds, &r.SourceType,
		&r.RequesterID, &r.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan next voice track: %w", err)
	}

	delQuery := `DELETE FROM voice_queue WHERE id = ?`
	if _, err := tx.ExecContext(ctx, delQuery, r.ID); err != nil {
		return nil, fmt.Errorf("failed to delete popped voice track: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit pop voice track: %w", err)
	}
	return &r, nil
}

// ClearVoiceQueue removes all queued tracks for a chat.
func (d *DB) ClearVoiceQueue(ctx context.Context, chatID int64) error {
	query := `DELETE FROM voice_queue WHERE chat_id = ?`
	if _, err := d.ExecContext(ctx, query, chatID); err != nil {
		return fmt.Errorf("failed to clear voice queue: %w", err)
	}
	return nil
}

// DeleteVoiceQueueTrack removes a single track from the queue by ID.
func (d *DB) DeleteVoiceQueueTrack(ctx context.Context, id int64) error {
	query := `DELETE FROM voice_queue WHERE id = ?`
	if _, err := d.ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("failed to delete track from queue: %w", err)
	}
	return nil
}
