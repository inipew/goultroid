package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type SQLiteRepository struct {
	db            *database.DB
	registryReady bool
	registryErr   error
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	ready, err := mediaregistry.SchemaReady(context.Background(), db)
	return &SQLiteRepository{db: db, registryReady: ready, registryErr: err}
}

func (r *SQLiteRepository) SaveNote(ctx context.Context, chatID int64, name string, response savedresponse.Response) error {
	if r.registryErr != nil {
		return fmt.Errorf("failed to inspect media registry schema: %w", r.registryErr)
	}

	now := time.Now().UTC()
	media := savedresponse.MediaRef{}
	if response.Media != nil {
		media = *response.Media
	}
	format := response.Format
	if format == "" {
		format = savedresponse.FormatHTML
	}
	query := `
	INSERT INTO notes (
		chat_id, name, content, response_format,
		media_asset_id, media_type, media_name, media_mime,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(chat_id, name) DO UPDATE SET
		content = excluded.content,
		response_format = excluded.response_format,
		media_asset_id = excluded.media_asset_id,
		media_type = excluded.media_type,
		media_name = excluded.media_name,
		media_mime = excluded.media_mime,
		updated_at = excluded.updated_at
	`
	if !r.registryReady {
		if _, err := r.db.ExecContext(ctx, query,
			chatID, name, response.Text, string(format),
			media.AssetID, media.MediaType, media.Name, media.MIMEType,
			now, now,
		); err != nil {
			return fmt.Errorf("failed to save note: %w", err)
		}
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin note save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var previousAssetID string
	err = tx.QueryRowContext(ctx,
		"SELECT media_asset_id FROM notes WHERE chat_id = ? AND name = ?",
		chatID,
		name,
	).Scan(&previousAssetID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to inspect previous note media: %w", err)
	}

	if _, err := tx.ExecContext(ctx, query,
		chatID, name, response.Text, string(format),
		media.AssetID, media.MediaType, media.Name, media.MIMEType,
		now, now,
	); err != nil {
		return fmt.Errorf("failed to save note: %w", err)
	}

	if media.AssetID != "" {
		if err := mediaregistry.RegisterAssetWithExecutor(
			ctx,
			tx,
			savedresponse.MediaRegistryAssetRegistration(media.AssetID),
			savedresponse.NoteMediaRegistryReference(media.AssetID, chatID, name),
		); err != nil {
			return fmt.Errorf("failed to mirror note media registry: %w", err)
		}
	}
	if previousAssetID != "" && previousAssetID != media.AssetID {
		if err := mediaregistry.RemoveReferenceWithExecutor(
			ctx,
			tx,
			savedresponse.NoteMediaRegistryReference(previousAssetID, chatID, name),
		); err != nil {
			return fmt.Errorf("failed to remove previous note media reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit note save: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) GetNote(ctx context.Context, chatID int64, name string) (*Note, error) {
	query := `SELECT chat_id, name, content, response_format,
		media_asset_id, media_type, media_name, media_mime,
		created_at, updated_at
		FROM notes WHERE chat_id = ? AND name = ?`
	row := r.db.QueryRowContext(ctx, query, chatID, name)

	var n Note
	var format, assetID, mediaType, mediaName, mediaMIME string
	if err := row.Scan(
		&n.ChatID, &n.Name, &n.Response.Text, &format,
		&assetID, &mediaType, &mediaName, &mediaMIME,
		&n.CreatedAt, &n.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get note: %w", err)
	}
	n.Response.Format = savedresponse.Format(format)
	if n.Response.Format == "" {
		n.Response.Format = savedresponse.FormatHTML
	}
	if assetID != "" {
		n.Response.Media = &savedresponse.MediaRef{AssetID: assetID, MediaType: mediaType, Name: mediaName, MIMEType: mediaMIME}
	}
	return &n, nil
}

func (r *SQLiteRepository) ListNotes(ctx context.Context, chatID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT name FROM notes WHERE chat_id = ? ORDER BY name ASC", chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list notes: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan note name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func (r *SQLiteRepository) ListNoteDetails(ctx context.Context, chatID int64) ([]Note, error) {
	query := `SELECT chat_id, name, content, response_format,
		media_asset_id, media_type, media_name, media_mime,
		created_at, updated_at
		FROM notes WHERE chat_id = ? ORDER BY name ASC`
	rows, err := r.db.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list note details: %w", err)
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		var n Note
		var format, assetID, mediaType, mediaName, mediaMIME string
		if err := rows.Scan(
			&n.ChatID, &n.Name, &n.Response.Text, &format,
			&assetID, &mediaType, &mediaName, &mediaMIME,
			&n.CreatedAt, &n.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan note details: %w", err)
		}
		n.Response.Format = savedresponse.Format(format)
		if n.Response.Format == "" {
			n.Response.Format = savedresponse.FormatHTML
		}
		if assetID != "" {
			n.Response.Media = &savedresponse.MediaRef{AssetID: assetID, MediaType: mediaType, Name: mediaName, MIMEType: mediaMIME}
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *SQLiteRepository) DeleteNote(ctx context.Context, chatID int64, name string) error {
	if r.registryErr != nil {
		return fmt.Errorf("failed to inspect media registry schema: %w", r.registryErr)
	}

	if !r.registryReady {
		res, err := r.db.ExecContext(ctx, "DELETE FROM notes WHERE chat_id = ? AND name = ?", chatID, name)
		if err != nil {
			return fmt.Errorf("failed to delete note: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return errors.New("note not found")
		}
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin note delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var assetID string
	if err := tx.QueryRowContext(ctx,
		"SELECT media_asset_id FROM notes WHERE chat_id = ? AND name = ?",
		chatID,
		name,
	).Scan(&assetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("note not found")
		}
		return fmt.Errorf("failed to inspect note media before delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM notes WHERE chat_id = ? AND name = ?", chatID, name); err != nil {
		return fmt.Errorf("failed to delete note: %w", err)
	}
	if assetID != "" {
		if err := mediaregistry.RemoveReferenceWithExecutor(
			ctx,
			tx,
			savedresponse.NoteMediaRegistryReference(assetID, chatID, name),
		); err != nil {
			return fmt.Errorf("failed to remove note media reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit note delete: %w", err)
	}
	return nil
}

var _ Repository = (*SQLiteRepository)(nil)
var _ DetailRepository = (*SQLiteRepository)(nil)
