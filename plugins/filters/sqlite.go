package filters

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type SQLiteRepository struct {
	db *database.DB
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) SaveFilter(ctx context.Context, chatID int64, keyword string, response savedresponse.Response) error {
	media := savedresponse.MediaRef{}
	if response.Media != nil {
		media = *response.Media
	}
	format := response.Format
	if format == "" {
		format = savedresponse.FormatHTML
	}
	query := `
	INSERT INTO filters (
		chat_id, keyword, reply_text, response_format,
		media_asset_id, media_type, media_name, media_mime, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(chat_id, keyword) DO UPDATE SET
		reply_text = excluded.reply_text,
		response_format = excluded.response_format,
		media_asset_id = excluded.media_asset_id,
		media_type = excluded.media_type,
		media_name = excluded.media_name,
		media_mime = excluded.media_mime,
		created_at = excluded.created_at;
	`
	_, err := r.db.ExecContext(ctx, query,
		chatID, strings.ToLower(keyword), response.Text, string(format),
		media.AssetID, media.MediaType, media.Name, media.MIMEType, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("failed to save filter: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error) {
	query := `SELECT chat_id, keyword, reply_text, response_format,
		media_asset_id, media_type, media_name, media_mime, created_at
		FROM filters WHERE chat_id = ? AND keyword = ?`
	row := r.db.QueryRowContext(ctx, query, chatID, strings.ToLower(keyword))
	var f Filter
	var format, assetID, mediaType, mediaName, mediaMIME string
	if err := row.Scan(
		&f.ChatID, &f.Keyword, &f.Response.Text, &format,
		&assetID, &mediaType, &mediaName, &mediaMIME, &f.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get filter: %w", err)
	}
	f.Response.Format = savedresponse.Format(format)
	if f.Response.Format == "" {
		f.Response.Format = savedresponse.FormatHTML
	}
	if assetID != "" {
		f.Response.Media = &savedresponse.MediaRef{AssetID: assetID, MediaType: mediaType, Name: mediaName, MIMEType: mediaMIME}
	}
	return &f, nil
}

func (r *SQLiteRepository) ListActiveChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT DISTINCT chat_id FROM filters ORDER BY chat_id ASC")
	if err != nil {
		return nil, fmt.Errorf("failed to list active filter chats: %w", err)
	}
	defer rows.Close()
	var chatIDs []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, fmt.Errorf("failed to scan active filter chat: %w", err)
		}
		chatIDs = append(chatIDs, chatID)
	}
	return chatIDs, rows.Err()
}

func (r *SQLiteRepository) ListFilters(ctx context.Context, chatID int64) ([]Filter, error) {
	query := `SELECT chat_id, keyword, reply_text, response_format,
		media_asset_id, media_type, media_name, media_mime, created_at
		FROM filters WHERE chat_id = ? ORDER BY keyword ASC`
	rows, err := r.db.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list filters: %w", err)
	}
	defer rows.Close()

	var result []Filter
	for rows.Next() {
		var f Filter
		var format, assetID, mediaType, mediaName, mediaMIME string
		if err := rows.Scan(
			&f.ChatID, &f.Keyword, &f.Response.Text, &format,
			&assetID, &mediaType, &mediaName, &mediaMIME, &f.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan filter: %w", err)
		}
		f.Response.Format = savedresponse.Format(format)
		if f.Response.Format == "" {
			f.Response.Format = savedresponse.FormatHTML
		}
		if assetID != "" {
			f.Response.Media = &savedresponse.MediaRef{AssetID: assetID, MediaType: mediaType, Name: mediaName, MIMEType: mediaMIME}
		}
		result = append(result, f)
	}
	return result, rows.Err()
}

func (r *SQLiteRepository) DeleteFilter(ctx context.Context, chatID int64, keyword string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM filters WHERE chat_id = ? AND keyword = ?", chatID, strings.ToLower(keyword))
	if err != nil {
		return fmt.Errorf("failed to delete filter: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("filter not found")
	}
	return nil
}

var _ Repository = (*SQLiteRepository)(nil)
var _ ActiveChatRepository = (*SQLiteRepository)(nil)
